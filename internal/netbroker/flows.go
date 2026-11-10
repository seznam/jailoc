package netbroker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	dialTimeout = 10 * time.Second
	udpIdle     = 60 * time.Second
	// maxTCPFlows and maxUDPFlows cap the guest flows relayed at once; each
	// holds a host socket and goroutines. New flows beyond them are refused.
	maxTCPFlows = 4096
	maxUDPFlows = 1024
)

// flowLimit counts active flows of one kind against a cap.
type flowLimit struct {
	n   atomic.Int64
	max int64
}

// acquire reserves a flow, or reports false at the cap.
func (l *flowLimit) acquire() bool {
	if l.n.Add(1) > l.max {
		l.n.Add(-1)
		return false
	}
	return true
}

func (l *flowLimit) release() { l.n.Add(-1) }

// handleTCP runs for every new guest TCP connection that no local listener
// accepted. The host socket is opened only after the policy allows the real
// destination; denied or unreachable destinations get a RST. When name rules
// apply, the broker completes the guest handshake and inspects the TLS SNI or
// HTTP Host before dialing upstream.
func (b *Broker) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	src, dst := addrPort(id.RemoteAddress, id.RemotePort), addrPort(id.LocalAddress, id.LocalPort)
	conn := b.connSeq.Add(1)
	p := b.policy.Load()
	env := b.env(dst.Addr())
	d := Decide(p, env, ProtoTCP, dst)
	if !d.Allow {
		b.recordFlow(conn, ProtoTCP, src, dst, d)
		r.Complete(true)
		return
	}
	if !b.tcpFlows.acquire() {
		b.recordFlow(conn, ProtoTCP, src, dst, d)
		b.record(map[string]any{"event": "flow-limit", "conn": conn, "proto": ProtoTCP, "dst": dst.String()})
		r.Complete(true)
		return
	}
	if p.needsNameInspection(dst, d.Name) {
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			b.tcpFlows.release()
			r.Complete(true)
			return
		}
		r.Complete(false)
		go func() {
			defer b.tcpFlows.release()
			b.handleInspectedTCP(conn, src, dst, p, env, d, ep, &wq)
		}()
		return
	}
	b.recordFlow(conn, ProtoTCP, src, dst, d)
	out, err := b.dialTCP("tcp", dst.String())
	if err != nil {
		b.tcpFlows.release()
		b.record(map[string]any{"event": "dial-error", "proto": ProtoTCP, "dst": dst.String(), "error": err.Error()})
		r.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		b.tcpFlows.release()
		_ = out.Close()
		r.Complete(true)
		return
	}
	r.Complete(false)
	guest := &sniffConn{Conn: gonet.NewTCPConn(&wq, ep), report: func(info map[string]any) {
		info["event"], info["conn"], info["dst"] = "l7", conn, dst.String()
		b.record(info)
	}}
	go func() {
		defer b.tcpFlows.release()
		splice(guest, out)
	}()
}

const sniffBeforeDialTimeout = 2 * time.Second

func (b *Broker) handleInspectedTCP(conn uint64, src, dst netip.AddrPort, p *Policy, env Env, d Decision, ep tcpip.Endpoint, wq *waiter.Queue) {
	rawConn := gonet.NewTCPConn(wq, ep)
	replayed, info, incomplete := sniffBeforeDial(rawConn, sniffBeforeDialTimeout)
	peerName := sniffedPeerName(info)
	switch {
	case peerName != "":
		// Check whether the sniffed SNI/Host belongs to the destination IP in
		// the DNS cache (unless a CIDR or wildcard rule allows the IP directly).
		inCache := false
		for _, n := range env.Names {
			if n == peerName {
				inCache = true
				break
			}
		}
		envWithPeer := env
		envWithPeer.Names = []string{peerName}
		dPeer := Decide(p, envWithPeer, ProtoTCP, dst)
		switch {
		case !dPeer.Allow:
			d = dPeer
		case !inCache && dPeer.Name != "":
			// Matched by name rule, but the guest never resolved this name to dst.Addr().
			// Check if an address/wildcard rule allows dst without any name.
			envNoNames := env
			envNoNames.Names = nil
			if dAddr := Decide(p, envNoNames, ProtoTCP, dst); dAddr.Allow {
				d = dAddr
				d.Name = peerName
			} else {
				d = Decision{Allow: false, Reason: "sni-mismatch", Name: peerName}
			}
		default:
			d = dPeer
		}
	case incomplete || info != nil:
		// Timed out waiting for TLS ClientHello / HTTP headers, or received
		// TLS/HTTP without an SNI/Host header: fail closed unless an address
		// or wildcard rule allows the destination without a hostname.
		envNoNames := env
		envNoNames.Names = nil
		if dAddr := Decide(p, envNoNames, ProtoTCP, dst); !dAddr.Allow {
			d = Decision{Allow: false, Reason: "sni-required", Name: d.Name}
		}
	}
	b.recordFlow(conn, ProtoTCP, src, dst, d)
	if info != nil {
		info["event"], info["conn"], info["dst"] = "l7", conn, dst.String()
		b.record(info)
	}
	if !d.Allow {
		ep.Abort()
		_ = rawConn.Close()
		return
	}
	out, err := b.dialTCP("tcp", dst.String())
	if err != nil {
		b.record(map[string]any{"event": "dial-error", "proto": ProtoTCP, "dst": dst.String(), "error": err.Error()})
		ep.Abort()
		_ = rawConn.Close()
		return
	}
	splice(replayed, out)
}

// handleUDP runs for every new guest UDP flow. Returning false makes the
// stack answer a denied or unreachable destination with ICMP port
// unreachable, so the guest fails fast instead of timing out.
func (b *Broker) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	src, dst := addrPort(id.RemoteAddress, id.RemotePort), addrPort(id.LocalAddress, id.LocalPort)
	conn := b.connSeq.Add(1)
	if !b.decide(ProtoUDP, src, dst, conn).Allow {
		return false
	}
	if !b.udpFlows.acquire() {
		b.record(map[string]any{"event": "flow-limit", "conn": conn, "proto": ProtoUDP, "dst": dst.String()})
		return false
	}
	out, err := b.dialUDP("udp", nil, net.UDPAddrFromAddrPort(dst))
	if err != nil {
		b.udpFlows.release()
		b.record(map[string]any{"event": "dial-error", "proto": ProtoUDP, "dst": dst.String(), "error": err.Error()})
		return false
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		b.udpFlows.release()
		_ = out.Close()
		return true
	}
	in := gonet.NewUDPConn(&wq, ep)
	go func() {
		defer b.udpFlows.release()
		relayUDP(in, out)
	}()
	return true
}

// relayUDP copies datagrams both ways until the flow is idle.
func relayUDP(in *gonet.UDPConn, out *net.UDPConn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 65535)
		for {
			_ = src.SetReadDeadline(time.Now().Add(udpIdle))
			n, err := src.Read(buf)
			if err != nil {
				break
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go cp(out, in)
	go cp(in, out)
	<-done
	_ = in.Close()
	_ = out.Close()
	<-done
}

// startForward publishes guest port f.GuestPort on the host loopback address,
// or on a host unix socket when f.UnixPath is set.
func (b *Broker) startForward(ctx context.Context, f Forward) (func(), error) {
	var lc net.ListenConfig
	network, addr := "tcp", f.Host.String()
	if f.UnixPath != "" {
		network, addr = "unix", f.UnixPath
		if err := os.Remove(addr); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale forward socket: %w", err)
		}
	}
	l, err := lc.Listen(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if f.UnixPath != "" {
		if err := os.Chmod(addr, 0o600); err != nil {
			_ = l.Close()
			return nil, fmt.Errorf("chmod forward socket: %w", err)
		}
	}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	guest := tcpip.FullAddress{NIC: nicID, Addr: tcpip.AddrFrom4(b.cfg.GuestIP.As4()), Port: f.GuestPort}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				dctx, cancel := context.WithTimeout(ctx, dialTimeout)
				defer cancel()
				g, err := gonet.DialContextTCP(dctx, b.stack, guest, ipv4.ProtocolNumber)
				if err != nil {
					_ = c.Close()
					return
				}
				b.record(map[string]any{"event": "ingress", "host": addr, "guest_port": f.GuestPort})
				splice(c, g)
			}()
		}
	}()
	return func() { _ = l.Close() }, nil
}

// startService accepts guest connections to the gateway's s.GuestPort and
// relays each to the host unix socket s.UnixPath or TCP address s.TCP.
func (b *Broker) startService(ctx context.Context, s Service) error {
	t := serviceTarget{network: "unix", addr: s.UnixPath}
	if s.TCP.IsValid() {
		t = serviceTarget{network: "tcp", addr: s.TCP.String()}
	}
	_, err := b.startServiceTarget(ctx, s.GuestPort, func() serviceRoute { return serviceRoute{target: t, ctx: ctx} })
	return err
}

// serviceRoute is where a gateway service sends a new connection: the host
// target to dial and a context that closes the connection when cancelled.
type serviceRoute struct {
	target serviceTarget
	ctx    context.Context
}

// startServiceTarget accepts guest connections to the gateway's port and
// relays each along the route current at accept time. Cancelling ctx closes the
// listener and every open connection, and cancelling a route's ctx closes the
// connections made along it, so that a removed or retargeted service, such as
// SSH agent forwarding, is revoked at once.
func (b *Broker) startServiceTarget(ctx context.Context, port uint16, route func() serviceRoute) (func(), error) {
	s := Service{GuestPort: port}
	l, err := gonet.ListenTCP(b.stack, *guestAddr(b.cfg.Gateway, s.GuestPort), ipv4.ProtocolNumber)
	if err != nil {
		return nil, fmt.Errorf("service listener on port %d: %w", s.GuestPort, err)
	}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if !b.tcpFlows.acquire() {
				b.record(map[string]any{"event": "flow-limit", "proto": ProtoTCP, "guest_port": s.GuestPort})
				_ = c.Close()
				continue
			}
			go func() {
				defer b.tcpFlows.release()
				r := route()
				var d net.Dialer
				dctx, cancel := context.WithTimeout(r.ctx, dialTimeout)
				h, err := d.DialContext(dctx, r.target.network, r.target.addr)
				cancel()
				if err != nil {
					b.record(map[string]any{"event": "service-error", "guest_port": s.GuestPort, "network": r.target.network, "target": r.target.addr, "error": err.Error()})
					_ = c.Close()
					return
				}
				b.record(map[string]any{"event": "service", "guest_port": s.GuestPort, "network": r.target.network, "target": r.target.addr})
				stop := context.AfterFunc(r.ctx, func() {
					_ = c.Close()
					_ = h.Close()
				})
				defer stop()
				splice(c, h)
			}()
		}
	}()
	return func() { _ = l.Close() }, nil
}

func guestAddr(a netip.Addr, port uint16) *tcpip.FullAddress {
	return &tcpip.FullAddress{NIC: nicID, Addr: tcpip.AddrFrom4(a.As4()), Port: port}
}
