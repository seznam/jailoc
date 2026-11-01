package netbroker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
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
)

// handleTCP runs for every new guest TCP connection that no local listener
// accepted. The host socket is opened only after the policy allows the real
// destination; denied or unreachable destinations get a RST.
func (b *Broker) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	src, dst := addrPort(id.RemoteAddress, id.RemotePort), addrPort(id.LocalAddress, id.LocalPort)
	conn := b.connSeq.Add(1)
	if !b.decide(ProtoTCP, src, dst, conn).Allow {
		r.Complete(true)
		return
	}
	out, err := b.dialTCP("tcp", dst.String())
	if err != nil {
		b.record(map[string]any{"event": "dial-error", "proto": ProtoTCP, "dst": dst.String(), "error": err.Error()})
		r.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		_ = out.Close()
		r.Complete(true)
		return
	}
	r.Complete(false)
	guest := &sniffConn{Conn: gonet.NewTCPConn(&wq, ep), report: func(info map[string]any) {
		info["event"], info["conn"], info["dst"] = "l7", conn, dst.String()
		b.record(info)
	}}
	go splice(guest, out)
}

// handleUDP runs for every new guest UDP flow. Returning false makes the
// stack answer a denied or unreachable destination with ICMP port
// unreachable, so the guest fails fast instead of timing out.
func (b *Broker) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	src, dst := addrPort(id.RemoteAddress, id.RemotePort), addrPort(id.LocalAddress, id.LocalPort)
	if !b.decide(ProtoUDP, src, dst, b.connSeq.Add(1)).Allow {
		return false
	}
	out, err := b.dialUDP("udp", nil, net.UDPAddrFromAddrPort(dst))
	if err != nil {
		b.record(map[string]any{"event": "dial-error", "proto": ProtoUDP, "dst": dst.String(), "error": err.Error()})
		return false
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		_ = out.Close()
		return true
	}
	in := gonet.NewUDPConn(&wq, ep)
	go relayUDP(in, out)
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
			go func() {
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
