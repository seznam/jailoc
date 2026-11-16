package netbroker

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-request HTTP policy and proxying for inspected flows.

// maxEventPath caps the request path in an l7 event.
const maxEventPath = 2048

// maxUpstreamConns bounds the upstream sockets of one inspected flow, per
// transport.
const maxUpstreamConns = 16

// httpScope is the http rules that apply to one flow: those whose target
// matches the flow's peer name or destination address on tcp and its port.
type httpScope struct {
	allow, deny []*Rule
}

func (s httpScope) empty() bool { return len(s.allow) == 0 && len(s.deny) == 0 }

// httpScope returns the http rules covering a flow to dst whose TLS SNI or
// HTTP Host is peerName. An empty peerName leaves the rules that match dst by
// address (IP, CIDR or "*") alone.
func (p *Policy) httpScope(peerName string, dst netip.AddrPort) httpScope {
	var s httpScope
	if p == nil || !p.hasHTTP() {
		return s
	}
	addr := dst.Addr()
	if embedded, ok := embeddedIPv4(addr); ok {
		addr = embedded
		dst = netip.AddrPortFrom(addr, dst.Port())
	}
	isPublic := !inAny(addr, deniedV4)
	if addr.Is6() {
		isPublic = !addr.IsPrivate() && public6.Contains(addr)
	}
	covers := func(r *Rule) bool {
		return (peerName != "" && r.matchesName(ProtoTCP, dst.Port(), peerName)) || r.matchesAddr(ProtoTCP, dst, isPublic)
	}
	for i := range p.allow {
		if r := &p.allow[i]; r.HTTP != nil && covers(r) {
			s.allow = append(s.allow, r)
		}
	}
	for i := range p.httpDeny {
		if r := &p.httpDeny[i]; covers(r) {
			s.deny = append(s.deny, r)
		}
	}
	return s
}

// httpVerdict is the outcome for one request.
type httpVerdict struct {
	allow  bool
	rule   string
	reason string
}

// evaluate decides a request: a matching http deny rule denies it; when the
// scope has http allow rules one of them must match (plain allow rules for
// the host are ignored, so an http rule narrows its host); without http allow
// rules a plain rule allowed the flow, so the request is allowed. flowRule is
// the plain rule that allowed the flow, for events.
func (s httpScope) evaluate(method string, segs []string, flowRule string) httpVerdict {
	for _, r := range s.deny {
		if r.HTTP.matches(method, segs, true) {
			reason := "rule"
			if r.Name != "" {
				reason = "rule " + r.Name
			}
			return httpVerdict{rule: r.Name, reason: reason}
		}
	}
	if len(s.allow) == 0 {
		return httpVerdict{allow: true, rule: flowRule}
	}
	for _, r := range s.allow {
		if r.HTTP.matches(method, segs, false) {
			return httpVerdict{allow: true, rule: r.Name}
		}
	}
	return httpVerdict{reason: "http-no-match"}
}

// inspectFlow serves the HTTP requests of one inspected guest flow.
type inspectFlow struct {
	b        *Broker
	p        *Policy
	env      Env
	insp     *inspector
	scope    httpScope
	conn     uint64
	dst      netip.AddrPort
	peerName string
	scheme   string // "https" or "http"
	flowRule string // the plain rule that allowed the flow, if any
	// byIP marks a flow that showed no name: peerName is the destination's IP
	// literal and scope the rules that match the address.
	byIP bool
	// h2c marks a cleartext HTTP/2 prior-knowledge flow. It has no flow-level
	// peer name: the :authority of each request is decided like one.
	h2c bool

	proxy *httputil.ReverseProxy
	// h2 is the upstream transport for ordinary requests (HTTP/2 when the
	// server offers it); h1 is for Upgrade requests, which need HTTP/1.1.
	h2, h1 *http.Transport

	mu       sync.Mutex
	closed   bool
	upstream map[*upstreamConn]struct{}
}

// upstreamConn is an upstream connection the flow tracks until it is closed,
// so that closing the flow ends the live ones and closed ones do not pile up.
type upstreamConn struct {
	net.Conn
	f    *inspectFlow
	once sync.Once
}

func (c *upstreamConn) Close() error {
	c.once.Do(func() {
		c.f.mu.Lock()
		delete(c.f.upstream, c)
		c.f.mu.Unlock()
	})
	return c.Conn.Close()
}

func (c *upstreamConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// trackedUpstream counts the upstream connections still open.
func (f *inspectFlow) trackedUpstream() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upstream)
}

func (b *Broker) newInspectFlow(p *Policy, env Env, d Decision, scope httpScope, conn uint64, dst netip.AddrPort, peerName string, tlsFlow, h2c bool) *inspectFlow {
	f := &inspectFlow{
		b: b, p: p, env: env, insp: p.inspect, scope: scope, conn: conn, dst: dst,
		peerName: peerName, scheme: "http", flowRule: d.Rule, h2c: h2c,
		upstream: map[*upstreamConn]struct{}{},
	}
	if tlsFlow {
		f.scheme = "https"
	}
	// A prior-knowledge HTTP/2 guest gets prior-knowledge HTTP/2 upstream, so
	// gRPC and trailers survive; Upgrade requests still go over HTTP/1.1.
	f.h2 = f.newTransport([]string{"h2", "http/1.1"}, h2c)
	f.h1 = f.newTransport([]string{"http/1.1"}, false)
	f.proxy = &httputil.ReverseProxy{
		// Rewrite, unlike Director, leaves out X-Forwarded-* headers and
		// drops those the guest sent.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = f.scheme
			pr.Out.URL.Host = f.dst.String()
			if f.peerName != "" {
				pr.Out.URL.Host = net.JoinHostPort(f.peerName, strconv.Itoa(int(f.dst.Port())))
			}
			pr.Out.Host = pr.In.Host
		},
		Transport: roundTripperFunc(f.roundTrip),
		// Flush every write: streaming and server-sent event responses.
		FlushInterval:  -1,
		ModifyResponse: f.proxyResponse,
		ErrorHandler:   f.proxyError,
	}
	return f
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func (f *inspectFlow) roundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Upgrade") != "" {
		return f.h1.RoundTrip(r)
	}
	return f.h2.RoundTrip(r)
}

// newTransport builds an upstream transport that dials only the flow's
// original destination, whatever address the request names, and verifies the
// server certificate for the flow's peer name.
func (f *inspectFlow) newTransport(protos []string, h2c bool) *http.Transport {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return f.dialTCP(ctx)
		},
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return f.dialTLS(ctx, protos)
		},
		ForceAttemptHTTP2:   slices.Contains(protos, "h2"),
		DisableCompression:  true, // pass Content-Encoding through untouched
		MaxIdleConnsPerHost: 4,
		// A guest connection may open many HTTP/2 streams at once; without a
		// cap each would get its own upstream socket and the flow would use
		// far more than its one slot in the broker's flow limit.
		MaxConnsPerHost:       maxUpstreamConns,
		IdleConnTimeout:       upstreamIdleTimeout,
		ExpectContinueTimeout: time.Second,
	}
	if h2c {
		tr.Protocols = new(http.Protocols)
		tr.Protocols.SetUnencryptedHTTP2(true)
	}
	return tr
}

// dialTCP opens the upstream connection to the flow's original destination.
func (f *inspectFlow) dialTCP(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dial %s: %w", f.dst, err)
	}
	c, err := f.b.dialTCP("tcp", f.dst.String())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", f.dst, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = c.Close()
		return nil, fmt.Errorf("dial %s: flow closed", f.dst)
	}
	uc := &upstreamConn{Conn: c, f: f}
	f.upstream[uc] = struct{}{}
	return uc, nil
}

// dialTLS opens the upstream connection and verifies its certificate against
// the system roots and upstream_ca for the flow's peer name.
func (f *inspectFlow) dialTLS(ctx context.Context, protos []string) (net.Conn, error) {
	raw, err := f.dialTCP(ctx)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, &tls.Config{
		ServerName: f.peerName,
		RootCAs:    f.insp.upstream,
		NextProtos: protos,
		MinVersion: tls.VersionTLS12,
	})
	hctx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("upstream tls handshake with %s: %w", f.peerName, err)
	}
	return tc, nil
}

// close ends the flow: every upstream connection is closed, including those
// of upgraded requests the proxy has taken over.
func (f *inspectFlow) close() {
	f.mu.Lock()
	f.closed = true
	conns := make([]*upstreamConn, 0, len(f.upstream))
	for c := range f.upstream {
		conns = append(conns, c)
	}
	f.mu.Unlock()
	f.h2.CloseIdleConnections()
	f.h1.CloseIdleConnections()
	for _, c := range conns {
		_ = c.Close()
	}
}

// reqInfo travels in the request context to the proxy callbacks.
type reqInfo struct {
	method, path string
	// host is the name the request was decided for: the flow's peer name, or
	// the authority of an h2c request.
	host string
	rule string
}

type reqInfoKey struct{}

func reqInfoOf(ctx context.Context) *reqInfo {
	info, _ := ctx.Value(reqInfoKey{}).(*reqInfo)
	if info == nil {
		return &reqInfo{}
	}
	return info
}

// decideAuthority decides the authority of a prior-knowledge h2c request the
// way the flow-level decision treats a peer name: a name rule only counts when
// the guest resolved the name to the destination (see peerDecision), and an IP
// literal must be the destination itself.
func (f *inspectFlow) decideAuthority(name string) Decision {
	if ip, err := netip.ParseAddr(name); err == nil && ip.Unmap() != f.dst.Addr().Unmap() {
		return Decision{Reason: "host-mismatch", Name: name}
	}
	return f.b.peerDecision(f.p, f.env, f.dst, name)
}

// h2cAuthority normalizes the :authority of an h2c request to a bare host, or
// returns "" for a missing or malformed one.
func h2cAuthority(authority string) string {
	host := normalizeAuthority(authority)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host
}

// ServeHTTP applies the policy to one guest request and proxies it upstream.
func (f *inspectFlow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	info := &reqInfo{method: r.Method, path: truncate(escaped, maxEventPath), host: f.peerName}
	scope, flowRule := f.scope, f.flowRule
	if f.h2c {
		// No flow-level name: decide the request's authority like one.
		name := h2cAuthority(r.Host)
		info.host = name
		if name == "" {
			f.reject(w, r, info, http.StatusForbidden, httpVerdict{reason: "sni-required"}, "")
			return
		}
		d := f.decideAuthority(name)
		if !d.Allow {
			status := http.StatusForbidden
			if d.Reason == "host-mismatch" {
				status = http.StatusMisdirectedRequest
			}
			f.reject(w, r, info, status, httpVerdict{rule: d.Rule, reason: d.Reason}, "")
			return
		}
		scope, flowRule = f.p.httpScope(name, f.dst), d.Rule
	} else if host := requestHost(r.Host); host != f.peerName && (!f.byIP || r.Host != "") {
		// An HTTP/1.0 request without Host belongs to the flow's address.
		f.reject(w, r, info, http.StatusMisdirectedRequest, httpVerdict{reason: "host-mismatch"}, host)
		return
	}
	if r.Method == http.MethodConnect {
		f.reject(w, r, info, http.StatusForbidden, httpVerdict{reason: "connect-denied"}, "")
		return
	}
	segs, ok := canonicalPath(escaped)
	if !ok {
		f.reject(w, r, info, http.StatusForbidden, httpVerdict{reason: "bad-path"}, "")
		return
	}
	v := scope.evaluate(r.Method, segs, flowRule)
	if !v.allow {
		f.reject(w, r, info, http.StatusForbidden, v, "")
		return
	}
	info.rule = v.rule
	f.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, info)))
}

// reject answers a request the policy denies with the status and the
// "blocked by network policy" body of the connection-level denials, and
// records it first. The connection stays open for later requests.
func (f *inspectFlow) reject(w http.ResponseWriter, r *http.Request, info *reqInfo, status int, v httpVerdict, host string) {
	ev := f.l7Event(info)
	if host != "" {
		ev["host"] = truncate(host, 255)
	}
	ev["status"], ev["denied"], ev["reason"] = status, true, v.reason
	if v.rule != "" {
		ev["rule"] = v.rule
	}
	f.b.record(ev)

	body := denyBody(Decision{Reason: v.reason, Rule: v.rule})
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}

func (f *inspectFlow) l7Event(info *reqInfo) map[string]any {
	return map[string]any{
		"event": "l7", "conn": f.conn, "dst": f.dst.String(), "proto": f.scheme,
		"method": info.method, "host": info.host, "path": info.path,
	}
}

// proxyResponse records an allowed request once its response status is known,
// before the body streams.
func (f *inspectFlow) proxyResponse(res *http.Response) error {
	info := reqInfoOf(res.Request.Context())
	ev := f.l7Event(info)
	ev["status"] = res.StatusCode
	if info.rule != "" {
		ev["rule"] = info.rule
	}
	f.b.record(ev)
	return nil
}

// proxyError answers a request whose upstream failed with a 502 and records
// it; a certificate that fails verification adds a hint.
func (f *inspectFlow) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	ev := f.upstreamErrorEvent(reqInfoOf(r.Context()), err)
	f.b.record(ev)
	body := "bad gateway: upstream unreachable\n"
	if isCertError(err) {
		body = "bad gateway: upstream certificate failed verification\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusBadGateway)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}

// upstreamErrorEvent describes an upstream failure: an l7 event with status
// 502 for a request, or an inspect-error event when info is nil.
func (f *inspectFlow) upstreamErrorEvent(info *reqInfo, err error) map[string]any {
	var ev map[string]any
	if info != nil {
		ev = f.l7Event(info)
		ev["status"] = http.StatusBadGateway
		if info.rule != "" {
			ev["rule"] = info.rule
		}
	} else {
		ev = map[string]any{"event": "inspect-error", "conn": f.conn, "dst": f.dst.String(), "name": f.peerName}
	}
	ev["error"] = err.Error()
	if isCertError(err) {
		ev["hint"] = hintUpstreamCert
	}
	return ev
}

// requestHost returns the normalized host of a Host header or :authority,
// without its port.
func requestHost(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		host = hostport[1 : len(hostport)-1]
	}
	return normalizeName(host)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
