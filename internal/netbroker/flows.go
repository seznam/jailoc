package netbroker

import (
	"context"
	"net"
	"net/netip"
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
	if !b.decide(ProtoTCP, src, dst).Allow {
		r.Complete(true)
		return
	}
	d := net.Dialer{Timeout: dialTimeout}
	out, err := d.Dial("tcp", dst.String())
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
	go splice(gonet.NewTCPConn(&wq, ep), out)
}

// handleUDP runs for every new guest UDP flow. Returning false makes the
// stack answer with ICMP port unreachable, so denials fail fast.
func (b *Broker) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	src, dst := addrPort(id.RemoteAddress, id.RemotePort), addrPort(id.LocalAddress, id.LocalPort)
	if !b.decide(ProtoUDP, src, dst).Allow {
		return false
	}
	out, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(dst))
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

// startForward publishes guest port f.GuestPort on the host loopback address.
func (b *Broker) startForward(ctx context.Context, f Forward) error {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", f.Host.String())
	if err != nil {
		return err
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
				b.record(map[string]any{"event": "ingress", "host": f.Host.String(), "guest_port": f.GuestPort, "peer": c.RemoteAddr().String()})
				splice(c, g)
			}()
		}
	}()
	return nil
}

func guestAddr(a netip.Addr, port uint16) *tcpip.FullAddress {
	return &tcpip.FullAddress{NIC: nicID, Addr: tcpip.AddrFrom4(a.As4()), Port: port}
}
