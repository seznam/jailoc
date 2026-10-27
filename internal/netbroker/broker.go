package netbroker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const (
	nicID       tcpip.NICID = 1
	gatewayMAC              = tcpip.LinkAddress("\x5a\x94\xef\xe4\x0c\xdd")
	maxFrame                = 65535
	hostAddrTTL             = 2 * time.Second
)

// Forward publishes a guest TCP port on a host loopback address, or on a host
// unix socket (mode 0600) when UnixPath is set.
type Forward struct {
	Host      netip.AddrPort
	UnixPath  string
	GuestPort uint16
}

// Service relays guest TCP connections to the gateway's GuestPort to the
// host unix socket UnixPath.
type Service struct {
	GuestPort uint16
	UnixPath  string
}

// Config describes the virtual network served to one guest.
type Config struct {
	Subnet   netip.Prefix
	Gateway  netip.Addr
	GuestIP  netip.Addr
	MTU      uint32
	Forwards []Forward
	// Services are gateway TCP ports relayed to host unix sockets, such as
	// the SSH agent. They are the only gateway ports besides DNS.
	Services    []Service
	DNSFallback []netip.AddrPort // used when the policy names no upstream
	// HostDNS, when set, supplies the upstreams at query time if neither the
	// policy nor DNSFallback names any, so host network changes apply.
	HostDNS func() []netip.AddrPort
	Log     io.Writer
	// Status, when set, receives one line per lifecycle change for a
	// supervisor: "ready" once the NIC socket is served, then
	// "nic connected" and "nic disconnected".
	Status io.Writer
}

// Broker owns the userspace stack for one guest NIC.
type Broker struct {
	cfg     Config
	stack   *stack.Stack
	link    *channel.Endpoint
	policy  atomic.Pointer[Policy]
	hosts   addrCache
	dialTCP func(string, string) (net.Conn, error)
	dialUDP func(string, *net.UDPAddr, *net.UDPAddr) (*net.UDPConn, error)

	logMu sync.Mutex
	log   *json.Encoder

	connMu sync.Mutex
	active bool

	statusMu sync.Mutex

	runtime runtimeState

	// connSeq numbers guest flows so l7 events refer to their flow event.
	connSeq atomic.Uint64
}

// New builds the stack. It does not open any socket.
func New(cfg Config, p *Policy) (*Broker, error) {
	if !cfg.Gateway.Is4() || !cfg.GuestIP.Is4() || !cfg.Subnet.Contains(cfg.Gateway) || !cfg.Subnet.Contains(cfg.GuestIP) || cfg.Gateway == cfg.GuestIP {
		return nil, fmt.Errorf("gateway %s and guest %s must be distinct addresses in %s", cfg.Gateway, cfg.GuestIP, cfg.Subnet)
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1500
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	b := &Broker{cfg: cfg, log: json.NewEncoder(cfg.Log)}
	b.dialTCP = (&net.Dialer{Timeout: dialTimeout}).Dial
	b.dialUDP = net.DialUDP
	b.policy.Store(p)

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	b.link = channel.New(512, cfg.MTU+14, gatewayMAC)
	// VMMs negotiate checksum offload with the guest, so guest packets carry
	// partial checksums; validating them would drop every guest TCP segment.
	b.link.LinkEPCapabilities |= stack.CapabilityRXChecksumOffload
	if err := s.CreateNIC(nicID, ethernet.New(b.link)); err != nil {
		return nil, fmt.Errorf("create nic: %s", err)
	}
	gw := tcpip.AddrFrom4(cfg.Gateway.As4())
	if err := s.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: gw, PrefixLen: cfg.Subnet.Bits()},
	}, stack.AddressProperties{}); err != nil {
		return nil, fmt.Errorf("add gateway address: %s", err)
	}
	for _, address := range []netip.Addr{gateway6, linkLocal6} {
		if err := s.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
			Protocol:          ipv6.ProtocolNumber,
			AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom16(address.As16()), PrefixLen: 64},
		}, stack.AddressProperties{}); err != nil {
			return nil, fmt.Errorf("add IPv6 gateway address: %s", err)
		}
	}
	s.SetRouteTable([]tcpip.Route{
		{Destination: headerIPv4Any(), NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("promiscuous mode: %s", err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("spoofing: %s", err)
	}
	b.stack = s
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcp.NewForwarder(s, 0, 1024, b.handleTCP).HandlePacket)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udp.NewForwarder(s, b.handleUDP).HandlePacket)
	return b, nil
}

func headerIPv4Any() tcpip.Subnet {
	sn, _ := tcpip.NewSubnet(tcpip.AddrFrom4([4]byte{}), tcpip.MaskFromBytes([]byte{0, 0, 0, 0}))
	return sn
}

// SetPolicy atomically replaces the policy for new flows.
func (b *Broker) SetPolicy(p *Policy) {
	b.policy.Store(p)
	b.record(map[string]any{"event": "policy", "source": p.source, "deny_all": p.denyAll})
}

func (b *Broker) env() Env {
	return Env{Gateway: b.cfg.Gateway, Subnet: b.cfg.Subnet, HostAddrs: b.hosts.get()}
}

func (b *Broker) decide(proto string, src, dst netip.AddrPort, conn uint64) Decision {
	d := Decide(b.policy.Load(), b.env(), proto, dst)
	b.record(map[string]any{"event": "flow", "conn": conn, "proto": proto, "src": src.String(), "dst": dst.String(), "allow": d.Allow, "reason": d.Reason})
	return d
}

func (b *Broker) record(v map[string]any) {
	v["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	b.logMu.Lock()
	defer b.logMu.Unlock()
	_ = b.log.Encode(v)
}

func (b *Broker) status(line string) {
	if b.cfg.Status == nil {
		return
	}
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	_, _ = io.WriteString(b.cfg.Status, line+"\n")
}

// Run starts the gateway services and ingress forwards, then serves guest
// NIC connections from l until ctx ends.
func (b *Broker) Run(ctx context.Context, l net.Listener) error {
	if err := b.startDNS(ctx); err != nil {
		return err
	}
	if err := b.startDHCP(ctx); err != nil {
		return err
	}
	for _, f := range b.cfg.Forwards {
		if err := b.startForward(ctx, f); err != nil {
			return err
		}
	}
	for _, s := range b.cfg.Services {
		if err := b.startService(ctx, s); err != nil {
			return err
		}
	}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	b.status("ready")
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept guest nic: %w", err)
		}
		go b.serveNIC(ctx, c)
	}
}

// serveNIC bridges one VMM connection (QEMU stream framing: 4-byte big-endian
// length, then an Ethernet frame) to the stack. Only one guest NIC is served
// at a time.
func (b *Broker) serveNIC(ctx context.Context, c net.Conn) {
	defer func() { _ = c.Close() }()
	b.connMu.Lock()
	if b.active {
		b.connMu.Unlock()
		b.record(map[string]any{"event": "nic", "state": "rejected-second-connection"})
		return
	}
	b.active = true
	b.connMu.Unlock()
	defer func() {
		b.connMu.Lock()
		b.active = false
		b.connMu.Unlock()
	}()
	b.record(map[string]any{"event": "nic", "state": "connected"})
	b.status("nic connected")

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		for {
			pkt := b.link.ReadContext(cctx)
			if pkt == nil {
				return
			}
			v := pkt.ToView()
			frame := v.AsSlice()
			hdr := make([]byte, 4, 4+len(frame))
			binary.BigEndian.PutUint32(hdr, uint32(len(frame))) //nolint:gosec // G115: frame length is bounded by the MTU
			_, err := c.Write(append(hdr, frame...))
			v.Release()
			pkt.DecRef()
			if err != nil {
				return
			}
		}
	}()

	var hdr [4]byte
	for {
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			break
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > maxFrame {
			break
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(c, frame); err != nil {
			break
		}
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
		b.link.InjectInbound(0, pkt)
		pkt.DecRef()
	}
	b.record(map[string]any{"event": "nic", "state": "disconnected"})
	b.status("nic disconnected")
}

// addrCache keeps the set of host interface addresses, refreshed lazily.
type addrCache struct {
	mu  sync.Mutex
	at  time.Time
	set map[netip.Addr]bool
}

func (c *addrCache) get() map[netip.Addr]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set != nil && time.Since(c.at) < hostAddrTTL {
		return c.set
	}
	set := map[netip.Addr]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addrs {
			if ipnet, ok := address.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipnet.IP); ok {
					set[ip.WithZone("").Unmap()] = true
				}
			}
		}
	}
	c.set, c.at = set, time.Now()
	return set
}

func addrPort(a tcpip.Address, port uint16) netip.AddrPort {
	ip, _ := netip.AddrFromSlice(a.AsSlice())
	return netip.AddrPortFrom(ip.Unmap(), port)
}

type closeWriter interface{ CloseWrite() error }

// splice copies both directions and half-closes as each side finishes.
func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

var errNoUpstream = errors.New("no dns upstream configured")
