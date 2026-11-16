package netbroker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
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
// HTTP Host before dialing upstream; a flow denied there gets a TLS alert or
// HTTP 403 instead of a RST (see denyReply). When an http rule covers the
// flow's host, the flow is inspected instead of spliced: the broker terminates
// the guest's TLS with a certificate from the workspace inspection CA (or reads
// plain HTTP) and applies the rules to every request (see inspectFlow); other
// protocols to that host are decided by the plain rules alone. Allowed DNS to
// another resolver is answered by the broker itself (see serveDNSTCP).
func (b *Broker) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	src, dst := addrPort(id.RemoteAddress, id.RemotePort), addrPort(id.LocalAddress, id.LocalPort)
	conn := b.connSeq.Add(1)
	p := b.policy.Load()
	env := b.env(p, dst.Addr())
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
	if p.inspectsDNS(dst) {
		if !tryAcquire(b.dnsConns) {
			b.tcpFlows.release()
			b.recordFlow(conn, ProtoTCP, src, dst, d)
			b.record(map[string]any{"event": "flow-limit", "conn": conn, "proto": ProtoTCP, "dst": dst.String()})
			r.Complete(true)
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			<-b.dnsConns
			b.tcpFlows.release()
			r.Complete(true)
			return
		}
		r.Complete(false)
		b.recordFlow(conn, ProtoTCP, src, dst, d)
		go func() {
			defer b.tcpFlows.release()
			defer func() { <-b.dnsConns }()
			b.serveDNSTCP(context.Background(), gonet.NewTCPConn(&wq, ep), dst)
		}()
		return
	}
	if p.needsNameInspection(dst, d.byName) {
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

// h2PrefaceHead is the part of the HTTP/2 client preface that parses as an
// HTTP/1 request head: what a cleartext prior-knowledge (h2c) flow starts with.
const h2PrefaceHead = "PRI * HTTP/2.0\r\n\r\n"

func (b *Broker) handleInspectedTCP(conn uint64, src, dst netip.AddrPort, p *Policy, env Env, d Decision, ep tcpip.Endpoint, wq *waiter.Queue) {
	rawConn := gonet.NewTCPConn(wq, ep)
	timeout := 200 * time.Millisecond
	if isHTTPOrTLSPort(dst.Port()) {
		timeout = sniffBeforeDialTimeout
	}
	replayed, info, incomplete := sniffBeforeDial(rawConn, timeout)
	peerName, badAuthority := sniffedPeerNameStrict(info)
	// httpOnly: only http rules asked for the name. Name rules (a flow allowed
	// by name, a name deny rule) fail closed without one; http rules do so only
	// where one covers the destination's address, because the host of a flow
	// they do not cover is of no use to them.
	httpOnly := !p.needsNameForRules(dst, d.byName)
	// A cleartext prior-knowledge HTTP/2 flow has no name to decide at flow
	// level; every request carries its own :authority (see inspectFlow).
	h2c := peerName == "" && !badAuthority && info != nil && info["proto"] == "http" &&
		bytes.HasPrefix(replayed.buf, []byte(h2PrefaceHead))
	var addrScope httpScope
	if httpOnly && !h2c {
		addrScope = p.httpScope("", dst)
	}
	ipInspect := false
	// dp decides the flow. A flow that cannot be HTTP (no HTTP or TLS bytes,
	// or TLS with only non-HTTP ALPN) is decided without the http allow
	// rules, so they never open other protocols.
	dp := p
	if p.hasHTTP() && !httpCapable(info) {
		dp = p.plain()
	}
	switch {
	case badAuthority:
		d = Decision{Allow: false, Reason: "sni-mismatch", Name: d.Name}
	case h2c:
		// Decided per request; the L4 decision stands.
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
		dPeer := Decide(dp, envWithPeer, ProtoTCP, dst)
		switch {
		case !dPeer.Allow:
			d = dPeer
		case !inCache && dPeer.byName:
			// Matched by name rule, but the guest never resolved this name to dst.Addr().
			// Check if an address/wildcard rule allows dst without any name.
			envNoNames := env
			envNoNames.Names = nil
			if dAddr := Decide(dp, envNoNames, ProtoTCP, dst); dAddr.Allow {
				d = dAddr
				d.Name = peerName
			} else {
				d = Decision{Allow: false, Reason: "sni-mismatch", Name: peerName}
			}
		default:
			d = dPeer
		}
	case httpOnly && addrScope.empty():
		// No http rule covers the address and no name rule needs a name:
		// decided as without http rules, and not inspected.
		if dp != p {
			d = Decide(dp, env, ProtoTCP, dst)
		}
	case httpOnly && info != nil && httpCapable(info):
		// An http rule covers the address but the flow names no host: inspect
		// it under the destination's IP literal.
		ipInspect = true
	case httpOnly && info != nil:
		// TLS offering no HTTP protocol: the plain rules decide.
		d = Decide(dp, env, ProtoTCP, dst)
	case incomplete || info != nil || isHTTPOrTLSPort(dst.Port()):
		// Timed out waiting for TLS ClientHello / HTTP headers on an HTTP/TLS
		// port (or after partial TLS/HTTP bytes), or received TLS/HTTP without
		// an SNI/Host header: fail closed even when a wildcard or CIDR rule
		// matched at L4, so a guest cannot wait out the 2s deadline and then
		// send a denied SNI/Host over an unchecked splice.
		d = Decision{Allow: false, Reason: "sni-required", Name: d.Name}
	case dp != p:
		// Nothing identified on a non-HTTP port: the earlier decision may
		// rest on an http allow rule.
		d = Decide(dp, env, ProtoTCP, dst)
	}
	b.recordFlow(conn, ProtoTCP, src, dst, d)
	if info != nil {
		info["event"], info["conn"], info["dst"] = "l7", conn, dst.String()
		b.record(info)
	}
	if !d.Allow {
		// Answer in the protocol the guest spoke, so the client reports a
		// policy denial instead of a reset; without one, reset.
		if reply := denyReply(replayed.buf, info, d); reply != nil {
			replyAndClose(rawConn, reply)
			return
		}
		ep.Abort()
		_ = rawConn.Close()
		return
	}
	if info != nil && httpCapable(info) {
		switch {
		case h2c:
			f := b.newInspectFlow(p, env, d, httpScope{}, conn, dst, "", false, true)
			f.serveInspected(replayed)
			return
		case peerName != "":
			if scope := p.httpScope(peerName, dst); !scope.empty() {
				f := b.newInspectFlow(p, env, d, scope, conn, dst, peerName, info["proto"] == "tls", false)
				f.serveInspected(replayed)
				return
			}
		case ipInspect:
			f := b.newInspectFlow(p, env, d, addrScope, conn, dst, dst.Addr().Unmap().String(), info["proto"] == "tls", false)
			f.byIP = true
			f.serveInspected(replayed)
			return
		}
	}
	out, err := b.dialTCP("tcp", dst.String())
	if err != nil {
		b.record(map[string]any{"event": "dial-error", "proto": ProtoTCP, "dst": dst.String(), "error": err.Error()})
		ep.Abort()
		_ = rawConn.Close()
		return
	}
	// If the connection is plain HTTP, or if the client sent 0 bytes before the
	// short deadline on a non-standard port, keep enforcing hostname policy on
	// the stream so neither a delayed TLS/HTTP handshake nor a pipelined/keep-alive
	// second HTTP request can switch to a denied or mismatched Host/SNI.
	// Wrapping replayed directly causes the first splice Read to feed the
	// buffered bytes through enforceConn.inspect once, setting up HTTP body
	// framing (Content-Length / chunked) before subsequent reads from rawConn.
	var stream net.Conn = replayed
	if peerName == "" || info["proto"] == "http" {
		stream = &enforceConn{
			Conn: replayed,
			// Without http rules asking for it, a later request without a name
			// is as welcome as before they existed. So is a handshake that is
			// conclusively not HTTP (TLS offering no HTTP protocol): http rules
			// never apply to it, and the plain rules already decided it.
			allowNoName: httpOnly && (addrScope.empty() || info != nil && !httpCapable(info)),
			check: func(name string) Decision {
				d := b.peerDecision(dp, env, dst, name)
				if d.Allow && !p.httpScope(name, dst).empty() {
					// A host an http rule covers cannot be reached over a
					// connection that is not inspected: a later request or
					// handshake would skip the rule.
					return Decision{Reason: "inspection-required", Name: name}
				}
				return d
			},
			closeUpstream: func() { _ = out.Close() },
			abort: func() {
				ep.Abort()
				_ = rawConn.Close()
				_ = out.Close()
			},
		}
	}
	splice(stream, out)
}

// peerDecision decides a TLS SNI or HTTP Host seen on a spliced stream. A
// name rule only counts when the guest resolved peerName to dst through the
// broker, unless an address rule allows dst on its own.
func (b *Broker) peerDecision(p *Policy, env Env, dst netip.AddrPort, peerName string) Decision {
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
	if !dPeer.Allow || inCache || !dPeer.byName {
		return dPeer
	}
	envNoNames := env
	envNoNames.Names = nil
	if dAddr := Decide(p, envNoNames, ProtoTCP, dst); dAddr.Allow {
		return dAddr
	}
	return Decision{Reason: "sni-mismatch", Name: peerName}
}

// handleUDP runs for every new guest UDP flow. Returning false makes the
// stack answer a denied or unreachable destination with ICMP port
// unreachable, so the guest fails fast instead of timing out.
func (b *Broker) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	src, dst := addrPort(id.RemoteAddress, id.RemotePort), addrPort(id.LocalAddress, id.LocalPort)
	conn := b.connSeq.Add(1)
	p := b.policy.Load()
	if !b.decide(p, ProtoUDP, src, dst, conn).Allow {
		return false
	}
	if !b.udpFlows.acquire() {
		b.record(map[string]any{"event": "flow-limit", "conn": conn, "proto": ProtoUDP, "dst": dst.String()})
		return false
	}
	if p.inspectsDNS(dst) {
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			b.udpFlows.release()
			return true
		}
		go func() {
			defer b.udpFlows.release()
			b.serveDNSUDP(gonet.NewUDPConn(&wq, ep), dst)
		}()
		return true
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

// maxDNSFlowQueries caps the queries one direct DNS flow resolves at once, on
// top of the broker-wide maxDNSQueries.
const maxDNSFlowQueries = 16

// serveDNSUDP answers the guest's queries on one UDP flow to the resolver
// server, filtered like the gateway resolver, until the flow is idle.
func (b *Broker) serveDNSUDP(c *gonet.UDPConn, server netip.AddrPort) {
	defer func() { _ = c.Close() }()
	var wg sync.WaitGroup
	defer wg.Wait()
	queries := make(chan struct{}, maxDNSFlowQueries)
	buf := make([]byte, 65535)
	for {
		_ = c.SetReadDeadline(time.Now().Add(udpIdle))
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		if !tryAcquire(queries) {
			// Drop the query; the guest resolver retries.
			continue
		}
		if !tryAcquire(b.dnsQueries) {
			<-queries
			continue
		}
		q := append([]byte(nil), buf[:n]...)
		wg.Go(func() {
			defer func() { <-queries }()
			defer func() { <-b.dnsQueries }()
			if resp := b.resolve(context.Background(), "udp", q, server); resp != nil {
				_, _ = c.Write(resp)
			}
		})
	}
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
