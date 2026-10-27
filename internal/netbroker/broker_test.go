package netbroker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeGuest is a second userspace stack attached to the broker socket with
// the same framing a VMM uses.
func fakeGuest(t *testing.T, ctx context.Context, sock string) *stack.Stack {
	t.Helper()
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	link := channel.New(256, 1514, tcpip.LinkAddress("\x02\x00\x00\x00\x00\x02"))
	if err := s.CreateNIC(1, ethernet.New(link)); err != nil {
		t.Fatalf("guest nic: %s", err)
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4([4]byte{192, 168, 127, 2}), PrefixLen: 24},
	}, stack.AddressProperties{}); err != nil {
		t.Fatalf("guest addr: %s", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: headerIPv4Any(), Gateway: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}), NIC: 1}})
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	go func() {
		for {
			pkt := link.ReadContext(ctx)
			if pkt == nil {
				return
			}
			v := pkt.ToView()
			f := v.AsSlice()
			hdr := binary.BigEndian.AppendUint32(nil, uint32(len(f))) //nolint:gosec // test frame
			_, err := c.Write(append(hdr, f...))
			v.Release()
			pkt.DecRef()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		var hdr [4]byte
		for {
			if _, err := io.ReadFull(c, hdr[:]); err != nil {
				return
			}
			f := make([]byte, binary.BigEndian.Uint32(hdr[:]))
			if _, err := io.ReadFull(c, f); err != nil {
				return
			}
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(f)})
			link.InjectInbound(0, pkt)
			pkt.DecRef()
		}
	}()
	return s
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return uint16(l.Addr().(*net.TCPAddr).Port) //nolint:gosec // test port
}

func TestBrokerEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Fake DNS upstream on host loopback: echoes the query with QR set.
	up, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	t.Cleanup(func() { _ = up.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, a, err := up.ReadFrom(buf)
			if err != nil {
				return
			}
			buf[2] |= 0x80
			_, _ = up.WriteTo(buf[:n], a)
		}
	}()

	ingress := freePort(t)
	pol, err := ParsePolicy([]byte(`{"dns_upstreams":["`+up.LocalAddr().String()+`"]}`), "test")
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	log := &syncBuf{}
	status := &syncBuf{}
	fwdSock := filepath.Join(t.TempDir(), "fwd.sock")
	svcSock := filepath.Join(t.TempDir(), "svc.sock")
	svc, err := net.Listen("unix", svcSock)
	if err != nil {
		t.Fatalf("service socket: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	go func() {
		for {
			c, err := svc.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("hello from host"))
			_ = c.Close()
		}
	}()
	b, err := New(Config{
		Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
		Gateway: netip.MustParseAddr("192.168.127.1"),
		GuestIP: netip.MustParseAddr("192.168.127.2"),
		Forwards: []Forward{
			{Host: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ingress), GuestPort: 4096},
			{UnixPath: fwdSock, GuestPort: 4096},
		},
		Services: []Service{{GuestPort: 7022, UnixPath: svcSock}},
		Log:      log,
		Status:   status,
	}, pol)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sock := filepath.Join(t.TempDir(), "nic.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = b.Run(ctx, l) }()
	g := fakeGuest(t, ctx, sock)

	dial := func(ip [4]byte, port uint16) error {
		dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
		defer dcancel()
		c, err := gonet.DialContextTCP(dctx, g, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(ip), Port: port}, ipv4.ProtocolNumber)
		if err == nil {
			_ = c.Close()
		}
		return err
	}

	t.Run("private destination gets RST", func(t *testing.T) {
		err := dial([4]byte{10, 9, 9, 9}, 80)
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("dial 10.9.9.9:80 = %v, want connection refused", err)
		}
	})
	t.Run("gateway port other than DNS gets RST", func(t *testing.T) {
		if err := dial([4]byte{192, 168, 127, 1}, 22); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("dial gateway:22 = %v, want connection refused", err)
		}
	})
	t.Run("DNS on gateway is forwarded to the upstream", func(t *testing.T) {
		c, err := gonet.DialUDP(g, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}), Port: 53}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatalf("dial dns: %v", err)
		}
		defer func() { _ = c.Close() }()
		q := []byte{0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
		if _, err := c.Write(q); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 512)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if n != len(q) || buf[0] != 0xbe || buf[2]&0x80 == 0 {
			t.Fatalf("unexpected answer % x", buf[:n])
		}
	})
	t.Run("ingress reaches the guest listener", func(t *testing.T) {
		gl, err := gonet.ListenTCP(g, tcpip.FullAddress{NIC: 1, Port: 4096}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatalf("guest listen: %v", err)
		}
		defer func() { _ = gl.Close() }()
		go func() {
			for range 2 {
				c, err := gl.Accept()
				if err != nil {
					return
				}
				_, _ = c.Write([]byte("hello from guest"))
				_ = c.Close()
			}
		}()
		for _, target := range [][2]string{{"tcp", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ingress).String()}, {"unix", fwdSock}} {
			c, err := net.DialTimeout(target[0], target[1], 5*time.Second)
			if err != nil {
				t.Fatalf("dial ingress %s: %v", target[0], err)
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			got, err := io.ReadAll(c)
			_ = c.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("read %s: %v", target[0], err)
			}
			if string(got) != "hello from guest" {
				t.Fatalf("ingress %s got %q", target[0], got)
			}
		}
	})
	t.Run("gateway service reaches the host unix socket", func(t *testing.T) {
		dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
		defer dcancel()
		c, err := gonet.DialContextTCP(dctx, g, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}), Port: 7022}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatalf("dial service: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, _ := io.ReadAll(c)
		_ = c.Close()
		if string(got) != "hello from host" {
			t.Fatalf("service got %q", got)
		}
	})
	t.Run("blocked DNS zone gets NXDOMAIN", func(t *testing.T) {
		filtered, err := ParsePolicy([]byte(`{"dns_upstreams":["`+up.LocalAddr().String()+`"],"dns_blocked_zones":["Example.COM."]}`), "filtered")
		if err != nil {
			t.Fatalf("policy: %v", err)
		}
		b.SetPolicy(filtered)
		defer b.SetPolicy(pol)
		c, err := gonet.DialUDP(g, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}), Port: 53}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatalf("dial dns: %v", err)
		}
		defer func() { _ = c.Close() }()
		q := []byte{0xca, 0xfe, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'w', 'w', 'w', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
		if _, err := c.Write(q); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 512)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if n < 12 || buf[0] != 0xca || buf[2]&0x80 == 0 || buf[3]&0x0f != 3 {
			t.Fatalf("want NXDOMAIN, got % x", buf[:n])
		}
	})
	t.Run("fail-closed policy denies public destinations", func(t *testing.T) {
		b.SetPolicy(DenyAll("test"))
		defer b.SetPolicy(pol)
		if err := dial([4]byte{1, 1, 1, 1}, 443); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("dial 1.1.1.1:443 under deny-all = %v, want connection refused", err)
		}
	})

	for _, want := range []string{`"reason":"denied-range"`, `"reason":"gateway"`, `"reason":"fail-closed"`, `"event":"dns"`, `"event":"ingress"`} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("decision log missing %s:\n%s", want, log.String())
		}
	}
	if got := status.String(); !strings.HasPrefix(got, "ready\nnic connected\n") {
		t.Errorf("status lines = %q, want ready then nic connected", got)
	}
}
