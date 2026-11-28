package netbroker

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/sntp"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestUpstreamPolicy(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, proxy string
		valid       bool
	}{
		{"http", `{"workspace":"test","url":"http://127.0.0.1:3128"}`, true},
		{"unix", `{"workspace":"builder","url":"unix:///tmp/proxy.sock"}`, true},
		{"command", `{"workspace":"test","command":"helper","direct":["*.example.com:22","[fd00::/64]:443"]}`, true},
		{"empty", `{}`, false},
		{"both", `{"workspace":"test","url":"http://proxy","command":"helper"}`, false},
		{"credentials URL", `{"workspace":"test","url":"http://user:secret@proxy"}`, false},
		{"https", `{"workspace":"test","url":"https://proxy"}`, false},
		{"relative socket", `{"workspace":"test","url":"unix:relative"}`, false},
		{"workspace injection", `{"workspace":"x\r\nX: y","url":"http://proxy"}`, false},
		{"auth command", `{"workspace":"test","command":"helper","auth":{"username":"user","from_env":"PASS"}}`, false},
		{"multiple auth sources", `{"workspace":"test","url":"http://proxy","auth":{"username":"user","from_env":"PASS","from_file":"/tmp/pass"}}`, false},
		{"TTL env", `{"workspace":"test","url":"http://proxy","auth":{"username":"user","from_env":"PASS","ttl":"1h"}}`, false},
		{"unknown secret value", `{"workspace":"test","url":"http://proxy","auth":{"username":"user","password":"secret"}}`, false},
		{"bad direct", `{"workspace":"test","url":"http://proxy","direct":["example.com:0"]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParsePolicy([]byte(`{"upstream_proxy":`+tt.proxy+`}`), "upstream-test")
			if (err == nil) != tt.valid {
				t.Fatalf("ParsePolicy = %v, valid=%v", err, tt.valid)
			}
		})
	}
}

func TestUpstreamConnectHead(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, head string
		code       int
		fail       bool
	}{
		{"buffered tunnel", "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nearly-data", 200, false},
		{"non 2xx", "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n", 407, false},
		{"invalid", "garbage\r\n\r\n", 0, true},
		{"long line", "HTTP/1.1 200 OK\r\nX: " + strings.Repeat("x", maxConnectHead) + "\r\n\r\n", 0, true},
		{"total cap", "HTTP/1.1 200 OK\r\n" + strings.Repeat("X: abcdefghijklmnopqrstuvwxyz\r\n", 1000) + "\r\n", 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			br := bufio.NewReaderSize(strings.NewReader(tt.head), maxConnectHead)
			_, code, err := readConnectHead(br)
			if (err != nil) != tt.fail || code != tt.code {
				t.Fatalf("code %d error %v", code, err)
			}
			if tt.name == "buffered tunnel" {
				data, _ := io.ReadAll(br)
				if string(data) != "early-data" {
					t.Fatalf("lost buffered data %q", data)
				}
			}
		})
	}
}

func TestUpstreamDirect(t *testing.T) {
	t.Parallel()
	rules, err := compileUpstreamDirect([]string{"*.example.com:22", "10.0.0.0/8:443", "[fd00::/64]:443", "93.184.216.34:9999"})
	if err != nil {
		t.Fatal(err)
	}
	r := &upstreamRoute{direct: rules}
	for _, tt := range []struct {
		name, dst, host, proto string
		want                   bool
	}{
		{"name", "8.8.8.8:22", "git.example.com", ProtoTCP, true},
		{"name wrong port", "8.8.8.8:443", "git.example.com", ProtoTCP, false},
		{"name apex", "8.8.8.8:22", "example.com", ProtoTCP, false},
		{"CIDR private", "10.1.2.3:443", "", ProtoTCP, true},
		{"IPv6 CIDR", "[fd00::1234]:443", "", ProtoTCP, true},
		{"UDP", "93.184.216.34:9999", "", ProtoUDP, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := r.isDirect(tt.proto, netip.MustParseAddrPort(tt.dst), []string{tt.host})
			if got != tt.want {
				t.Fatalf("direct %v want %v", got, tt.want)
			}
		})
	}
}

func TestUpstreamDirectWildcardIsRouteOnly(t *testing.T) {
	t.Parallel()
	rules, err := compileUpstreamDirect([]string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	r := &upstreamRoute{direct: rules}
	for _, tt := range []struct{ name, proto, dst string }{
		{"DoT", ProtoTCP, "8.8.8.8:853"},
		{"QUIC", ProtoUDP, "8.8.8.8:443"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !r.isDirect(tt.proto, netip.MustParseAddrPort(tt.dst), nil) {
				t.Fatal("direct wildcard reapplied allow restrictions")
			}
		})
	}
}

func TestUpstreamInspectedTLS(t *testing.T) {
	t.Parallel()
	inspection := newTestCA(t, "inspection")
	proxyCA := newTestCA(t, "proxy")
	inner := make(chan http.Header, 4)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner <- r.Header.Clone()
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "data: two\n\n")
	}))
	backend.TLS = &tls.Config{Certificates: []tls.Certificate{proxyCA.serverCert(t, "example.com")}, MinVersion: tls.VersionTLS12}
	backend.StartTLS()
	t.Cleanup(backend.Close)
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", backend.Listener.Addr().String())
	proxyJSON, _ := json.Marshal(UpstreamProxy{Workspace: "test", URL: p.url, CA: proxyCA.certPath, Auth: &UpstreamAuth{Username: "user", FromEnv: "PASS"}})
	policy, err := ParsePolicy([]byte(`{"allow":[`+ruleAPIRead+`],"inspect":`+inspectSection(t, inspection, nil)+`,"upstream_proxy":`+string(proxyJSON)+`}`), "TLS proxy")
	if err != nil {
		t.Fatal(err)
	}
	if policy.inspect.upstream.Equal(x509.NewCertPool()) {
		t.Fatal("empty base roots")
	}
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), ResolveUpstreamAuth: func(ctx context.Context, workspace string, auth UpstreamAuth) ([]byte, error) {
		if workspace != "test" || auth.FromEnv != "PASS" {
			return nil, errors.New("wrong source")
		}
		return []byte("password\n"), nil
	}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	b.names.record(netip.MustParseAddr(l7IP), "example.com", time.Hour)
	g := connectTestBroker(t, b)
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return upstreamGuestDial(t, g, l7IP, 443) }, TLSClientConfig: &tls.Config{RootCAs: inspection.pool, MinVersion: tls.VersionTLS12}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/ok/stream", nil)
	req.Header.Set("X-Jailoc-Workspace", "forged")
	req.Header.Set("Proxy-Authorization", "forged")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("TLS response %d: %s", res.StatusCode, readBody(t, res))
	}
	if body := readBody(t, res); !strings.Contains(body, "data: two") {
		t.Fatalf("SSE body %q", body)
	}
	h := <-inner
	if h.Get("X-Jailoc-Workspace") != "" || h.Get("Proxy-Authorization") != "" {
		t.Fatalf("inner header leak %v", h)
	}
	seen := p.seen()
	if len(seen) != 1 || seen[0].Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNzd29yZA==" {
		t.Fatalf("CONNECT auth %v", seen)
	}
	res, err = client.Get("https://example.com/no")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	_ = res.Body.Close()
	pool := x509.NewCertPool()
	route := b.upstream.Load()
	combined := route.roots(pool)
	if !pool.Equal(x509.NewCertPool()) || combined.Equal(pool) {
		t.Fatal("roots not cloned")
	}
}

func TestUpstreamInspectedNon2xx(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "inspect")
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 403 Policy Forbidden\r\n\r\n", "")
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", URL: p.url})
	_, g, _ := upstreamTestBroker(t, `{"allow":[`+ruleAPIRead+`],"inspect":`+inspectSection(t, ca, nil)+`,"upstream_proxy":`+string(u)+`}`)
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return upstreamGuestDial(t, g, l7IP, 80) }}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	res, err := client.Get("http://example.com/ok/test")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 502 || !strings.Contains(readBody(t, res), "HTTP/1.1 403 Policy Forbidden") {
		t.Fatal("missing proxy refusal 502/status")
	}
}

func TestUpstreamUnixHelper(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "upstream")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := newTestConnectProxy(t, "unix", filepath.Join(dir, "proxy.sock"), "HTTP/1.1 200 OK\r\n\r\nhello", "")
	result, _ := json.Marshal(map[string]any{"version": 1, "url": p.url, "headers": map[string]string{"Proxy-Authorization": "Bearer helper-token"}})
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", Command: "printf '%s' '" + string(result) + "'"})
	_, guest, log := upstreamTestBroker(t, `{"upstream_proxy":`+string(u)+`}`)
	c, err := upstreamGuestDial(t, guest, l7IP, 443)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" || p.seen()[0].Header.Get("Proxy-Authorization") != "Bearer helper-token" {
		t.Fatal("bad unix CONNECT/helper")
	}
	if strings.Contains(log.String(), "helper-token") {
		t.Fatal("secret in log")
	}
}

func TestUpstreamUDPAndLocalServices(t *testing.T) {
	t.Parallel()
	dns, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dns.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := dns.ReadFrom(buf)
			if err != nil {
				return
			}
			buf[2] |= 0x80
			_, _ = dns.WriteTo(buf[:n], addr)
		}
	}()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", "")
	_, g, log := upstreamTestBroker(t, upstreamPolicyJSON(p.url, `,"dns_upstreams":["`+dns.LocalAddr().String()+`"]`))
	c, err := gonet.DialUDP(g, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(netip.MustParseAddr(l7IP).As4()), Port: 9999}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = c.Write([]byte("denied"))
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, err = c.Read(make([]byte, 16))
	if err == nil {
		t.Fatal("UDP not denied")
	}
	if !strings.Contains(log.String(), "upstream-proxy-udp") {
		t.Fatalf("missing UDP denial %s", log.String())
	}
	for _, port := range []uint16{53, sntp.Port} {
		c, err := gonet.DialUDP(g, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}), Port: port}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatal(err)
		}
		if port == sntp.Port {
			if _, err := sntp.Offset(c, time.Second); err != nil {
				t.Fatal(err)
			}
		} else {
			q := []byte{0xbe, 0xef, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
			_, _ = c.Write(q)
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 512)
			n, err := c.Read(buf)
			if err != nil || n != len(q) || buf[2]&0x80 == 0 {
				t.Fatalf("DNS %d %v", n, err)
			}
		}
		_ = c.Close()
	}
	if len(p.seen()) != 0 {
		t.Fatal("local services reached CONNECT proxy")
	}
}

func TestUpstreamDirectRealSocket(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, "direct")
	}()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", "")
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", URL: p.url, Direct: []string{"example.com:9999"}})
	b, g, _ := upstreamTestBroker(t, `{"upstream_proxy":`+string(u)+`}`)
	b.dialTCP = func(network, address string) (net.Conn, error) {
		if address != l7IP+":9999" {
			return nil, errors.New("wrong direct destination")
		}
		return net.DialTimeout(network, l.Addr().String(), time.Second)
	}
	c, err := upstreamGuestDial(t, g, l7IP, 9999)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 6)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "direct" || len(p.seen()) != 0 {
		t.Fatal("direct flow used proxy")
	}
}

func TestUpstreamNoDirectFallback(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	b, g, log := upstreamTestBroker(t, upstreamPolicyJSON("http://"+addr, ""))
	var dials atomic.Int64
	b.dialTCP = func(string, string) (net.Conn, error) { dials.Add(1); return nil, errors.New("unexpected direct") }
	c, err := upstreamGuestDial(t, g, l7IP, 443)
	if c != nil {
		_ = c.Close()
	}
	if err == nil || dials.Load() != 0 {
		t.Fatal("upstream failure fell back")
	}
	if !strings.Contains(log.String(), "no direct fallback") {
		t.Fatal("missing unreachable hint")
	}
}

func TestUpstreamConnectDeadlineCleared(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, err = http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\nfirst")
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(c, "second")
	}()
	ep, err := parseUpstreamEndpoint("http://" + l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b := &Broker{upstreamState: &upstreamManager{}, log: json.NewEncoder(io.Discard)}
	b.upstreamState.b = b
	r := &upstreamRoute{endpoint: ep, workspace: "test", headers: make(http.Header)}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	c, err := b.dialEgress(ctx, r, 42, netip.MustParseAddrPort(l7IP+":443"), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	data, err := io.ReadAll(c)
	if err != nil || string(data) != "firstsecond" {
		t.Fatalf("handshake deadline leaked into tunnel %q %v", data, err)
	}
}

// testConnectProxy speaks real CONNECT over TCP or a unix socket. Tunnel bytes
// are relayed to a real backend, not a substituted broker dial implementation.
type testConnectProxy struct {
	url      string
	mu       sync.Mutex
	requests []*http.Request
}

func newTestConnectProxy(t *testing.T, network, address, response, backend string) *testConnectProxy {
	t.Helper()
	l, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	p := &testConnectProxy{url: "http://" + l.Addr().String()}
	if network == "unix" {
		p.url = "unix://" + address
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				br := bufio.NewReader(c)
				r, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				p.mu.Lock()
				p.requests = append(p.requests, r)
				p.mu.Unlock()
				if _, err := io.WriteString(c, response); err != nil {
					return
				}
				if backend == "" {
					return
				}
				out, err := net.DialTimeout("tcp", backend, time.Second)
				if err != nil {
					return
				}
				_ = c.SetDeadline(time.Time{})
				splice(&bufConn{Conn: c, r: br}, out)
			}()
		}
	}()
	return p
}

func (p *testConnectProxy) seen() []*http.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*http.Request{}, p.requests...)
}

func upstreamTestBroker(t *testing.T, policy string) (*Broker, *stack.Stack, *syncBuf) {
	t.Helper()
	p, err := ParsePolicy([]byte(policy), "upstream-test")
	if err != nil {
		t.Fatal(err)
	}
	log := &syncBuf{}
	b, err := New(Config{
		Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
		Gateway: netip.MustParseAddr("192.168.127.1"),
		GuestIP: netip.MustParseAddr("192.168.127.2"), Log: log,
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	b.names.record(netip.MustParseAddr(l7IP), "example.com", time.Hour)
	return b, connectTestBroker(t, b), log
}

func upstreamGuestDial(t *testing.T, g *stack.Stack, ip string, port uint16) (net.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, err := gonet.DialContextTCP(ctx, g, tcpip.FullAddress{
		NIC: 1, Addr: tcpip.AddrFrom4(netip.MustParseAddr(ip).As4()), Port: port,
	}, ipv4.ProtocolNumber)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func upstreamPolicyJSON(url, extra string) string {
	u, _ := json.Marshal(url)
	return `{"upstream_proxy":{"workspace":"test","url":` + string(u) + `}` + extra + `}`
}

func TestUpstreamFakeGuestCONNECT(t *testing.T) {
	t.Parallel()
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	go func() {
		c, err := backend.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, "hello through tunnel")
	}()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 Connection established\r\n\r\n", backend.Addr().String())
	_, guest, log := upstreamTestBroker(t, upstreamPolicyJSON(p.url, ""))
	c, err := upstreamGuestDial(t, guest, l7IP, 443)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len("hello through tunnel"))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello through tunnel" {
		t.Fatalf("tunnel bytes %q", got)
	}
	seen := p.seen()
	if len(seen) != 1 {
		t.Fatalf("CONNECT count %d", len(seen))
	}
	r := seen[0]
	if r.Method != "CONNECT" || r.Host != "example.com:443" || r.RequestURI != "example.com:443" {
		t.Fatalf("CONNECT request %+v", r)
	}
	if r.Header.Get("X-Jailoc-Workspace") != "test" || r.Header.Get("X-Jailoc-Flow") == "" {
		t.Fatalf("metadata %v", r.Header)
	}
	if !strings.Contains(log.String(), fmt.Sprintf(`"conn":%s`, r.Header.Get("X-Jailoc-Flow"))) {
		t.Fatalf("flow ID not in log: %s", log.String())
	}
}

func TestUpstreamDenyBeforeProxy(t *testing.T) {
	t.Parallel()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", "")
	_, guest, _ := upstreamTestBroker(t, upstreamPolicyJSON(p.url, `,"allow":[{"cidrs":["*"]}],"deny":[{"cidrs":["93.184.216.34/32"]}]`))
	for _, ip := range []string{l7IP, "10.1.2.3", "127.0.0.1"} {
		c, err := upstreamGuestDial(t, guest, ip, 443)
		if c != nil {
			_ = c.Close()
		}
		if err == nil {
			t.Fatalf("guest reached denied %s", ip)
		}
	}
	if len(p.seen()) != 0 {
		t.Fatal("denied guest flow reached proxy")
	}
}

func TestUpstreamNon2xxResetsSplice(t *testing.T) {
	t.Parallel()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: secret-response\r\n\r\n", "")
	_, guest, log := upstreamTestBroker(t, upstreamPolicyJSON(p.url, ""))
	c, err := upstreamGuestDial(t, guest, l7IP, 443)
	if c != nil {
		_ = c.Close()
	}
	if err == nil {
		t.Fatal("non-2xx did not reset")
	}
	if !strings.Contains(log.String(), "407 Proxy Authentication Required") || strings.Contains(log.String(), "secret-response") {
		t.Fatalf("unsafe/missing failure log: %s", log.String())
	}
}
func TestUpstreamPipelinedHTTPCannotSwitchTunnelHost(t *testing.T) {
	t.Parallel()
	secondSeen := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" {
			select {
			case secondSeen <- r.Host:
			default:
			}
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", backend.Listener.Addr().String())
	b, g, _ := upstreamTestBroker(t, upstreamPolicyJSON(p.url, `,"allow":[{"names":["example.com","other.example.com"]}]`))
	b.names.record(netip.MustParseAddr(l7IP), "other.example.com", time.Hour)
	c, err := upstreamGuestDial(t, g, l7IP, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(c)
	if _, err := io.WriteString(c, "GET /one HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	res1, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = res1.Body.Close()
	_, _ = io.WriteString(c, "GET /two HTTP/1.1\r\nHost: other.example.com\r\n\r\n")
	if res2, err := http.ReadResponse(br, nil); err == nil {
		_ = res2.Body.Close()
		t.Fatalf("second request with switched Host succeeded: %d", res2.StatusCode)
	}
	select {
	case host := <-secondSeen:
		t.Fatalf("switched Host %q reached the first host's tunnel", host)
	default:
	}
}
func TestUpstreamExternalDNSRouting(t *testing.T) {
	t.Parallel()
	resolverTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolverTCP.Close() })
	go func() {
		for {
			c, err := resolverTCP.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, int(l[0])<<8|int(l[1]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				q[2] |= 0x80
				_, _ = c.Write(append(l[:], q...))
			}()
		}
	}()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", resolverTCP.Addr().String())
	_, g, log := upstreamTestBroker(t, upstreamPolicyJSON(p.url, `,"allow":[{"cidrs":["93.184.216.34/32"],"proto":"any","ports":["53"]},{"names":["example.com"]}]`))

	// Non-direct UDP to an external resolver on port 53 must be denied.
	uc, err := gonet.DialUDP(g, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(netip.MustParseAddr(l7IP).As4()), Port: 53}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	_ = uc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = uc.Write([]byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1})
	_, err = uc.Read(make([]byte, 64))
	_ = uc.Close()
	if err == nil || !strings.Contains(log.String(), "upstream-proxy-udp") {
		t.Fatalf("external UDP DNS was not denied: err=%v log=%s", err, log.String())
	}

	// Non-direct TCP to an external resolver on port 53 must traverse CONNECT and still be filtered by the broker.
	tc, err := upstreamGuestDial(t, g, l7IP, 53)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tc.Close() }()
	_ = tc.SetDeadline(time.Now().Add(2 * time.Second))
	q := []byte{0xca, 0xfe, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	frame := append([]byte{0, byte(len(q))}, q...) //nolint:gosec // G115: fixed 29-byte DNS query
	if _, err := tc.Write(frame); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, len(frame))
	if _, err := io.ReadFull(tc, resp); err != nil {
		t.Fatal(err)
	}
	if resp[2] != 0xca || resp[4]&0x80 == 0 || resp[5]&0x0f != 0 {
		t.Fatalf("unexpected TCP DNS reply: % x", resp)
	}
	seen := p.seen()
	if len(seen) != 1 || seen[0].Host != "example.com:53" {
		t.Fatalf("external TCP DNS did not use CONNECT: %+v", seen)
	}
}

func TestUpstreamInspectedDirectStripsReservedHeaders(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "inspect")
	inner := make(chan http.Header, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner <- r.Header.Clone()
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", "")
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", URL: p.url, Direct: []string{"example.com:80"}})
	b, g, _ := upstreamTestBroker(t, `{"allow":[`+ruleAPIRead+`],"inspect":`+inspectSection(t, ca, nil)+`,"upstream_proxy":`+string(u)+`}`)
	b.dialTCP = func(network, _ string) (net.Conn, error) {
		return net.DialTimeout(network, backend.Listener.Addr().String(), time.Second)
	}
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return upstreamGuestDial(t, g, l7IP, 80) }}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://example.com/ok/test", nil)
	req.Header.Set("X-Jailoc-Workspace", "forged")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if h := <-inner; h.Get("X-Jailoc-Workspace") != "" {
		t.Fatalf("direct inspected flow leaked X-Jailoc-* header: %v", h)
	}
}

func TestUpstreamDirectRequiresVerifiedDNSAssociation(t *testing.T) {
	t.Parallel()
	directBackend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directBackend.Close() })
	var directHits atomic.Int64
	go func() {
		for {
			c, err := directBackend.Accept()
			if err != nil {
				return
			}
			directHits.Add(1)
			_ = c.Close()
		}
	}()
	proxyBackend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxyBackend.Close() })
	go func() {
		for {
			c, err := proxyBackend.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 7\r\n\r\nproxied")
			_ = c.Close()
		}
	}()
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", proxyBackend.Addr().String())
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", URL: p.url, Direct: []string{"direct.example.com:80"}})
	b, g, _ := upstreamTestBroker(t, `{"allow":[{"cidrs":["93.184.216.34/32"]}],"upstream_proxy":`+string(u)+`}`)
	b.dialTCP = func(network, _ string) (net.Conn, error) {
		return net.DialTimeout(network, directBackend.Addr().String(), time.Second)
	}

	// Hostname was NOT resolved to 93.184.216.34 in the broker DNS cache:
	// an address rule allows the flow, so it must traverse the upstream proxy
	// instead of taking the hostname-based direct exception.
	c, err := upstreamGuestDial(t, g, l7IP, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: direct.example.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, res)
	if body != "proxied" || directHits.Load() != 0 || len(p.seen()) != 1 {
		t.Fatalf("unverified Host bypassed proxy via direct: body=%q directHits=%d proxySeen=%d", body, directHits.Load(), len(p.seen()))
	}

	// Untrusted DNS associations (e.g. after an internal_zones removal) must
	// also be excluded in both branches of upstreamNames.
	env := Env{
		Names:     []string{"direct.example.com"},
		Untrusted: map[string]bool{"direct.example.com": true},
	}
	if got := upstreamNames("direct.example.com", env); got != nil {
		t.Fatalf("upstreamNames(explicit untrusted) = %v, want nil", got)
	}
	if got := upstreamNames("", env); got != nil {
		t.Fatalf("upstreamNames(single untrusted) = %v, want nil", got)
	}
	envMixed := Env{
		Names:     []string{"untrusted.example.com", "trusted.example.com"},
		Untrusted: map[string]bool{"untrusted.example.com": true},
	}
	if got := upstreamNames("", envMixed); !slices.Equal(got, []string{"trusted.example.com"}) {
		t.Fatalf("upstreamNames(mixed trusted/untrusted) = %v, want [trusted.example.com]", got)
	}
}

func TestUpstreamSharedIPMultipleCachedNamesSniffsBeforeRoute(t *testing.T) {
	t.Parallel()
	directBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	t.Cleanup(directBackend.Close)
	proxyBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "proxy")
	}))
	t.Cleanup(proxyBackend.Close)
	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", proxyBackend.Listener.Addr().String())
	u, _ := json.Marshal(UpstreamProxy{Workspace: "test", URL: p.url, Direct: []string{"z-direct.example.com:80"}})
	b, g, _ := upstreamTestBroker(t, `{"upstream_proxy":`+string(u)+`}`)
	b.names.record(netip.MustParseAddr(l7IP), "a-proxied.example.com", time.Hour)
	b.names.record(netip.MustParseAddr(l7IP), "z-direct.example.com", time.Hour)
	b.dialTCP = func(network, _ string) (net.Conn, error) {
		return net.DialTimeout(network, directBackend.Listener.Addr().String(), time.Second)
	}

	doGet := func(host string) string {
		t.Helper()
		tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return upstreamGuestDial(t, g, l7IP, 80) }}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
		res, err := client.Get("http://" + host + "/")
		if err != nil {
			t.Fatalf("GET %s: %v", host, err)
		}
		return readBody(t, res)
	}

	if got := doGet("z-direct.example.com"); got != "direct" {
		t.Fatalf("direct host on shared IP got %q", got)
	}
	if len(p.seen()) != 0 {
		t.Fatalf("direct host on shared IP used proxy: %+v", p.seen())
	}
	if got := doGet("a-proxied.example.com"); got != "proxy" {
		t.Fatalf("proxied host on shared IP got %q", got)
	}
	seen := p.seen()
	if len(seen) != 1 || seen[0].Host != "a-proxied.example.com:80" {
		t.Fatalf("proxied host on shared IP sent wrong CONNECT: %+v", seen)
	}
}

func TestInspectH2CFlowConnectionLimitAcrossAuthorities(t *testing.T) {
	t.Parallel()
	const rules = ruleWebHost + `,{"name":"two","names":["two.example.com"],"http":{"methods":["GET"],"paths":["/ok/**"]}}`
	e := newL7Env(t)
	release := make(chan struct{})
	var active atomic.Int64
	var maxActive atomic.Int64
	e.h2cUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := active.Add(1)
		for {
			prev := maxActive.Load()
			if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer active.Add(-1)
		<-release
		_, _ = io.WriteString(w, "ok")
	}))
	e.start(rules, "", false)
	e.broker.names.record(netip.MustParseAddr(l7IP), "two.example.com", time.Hour)
	f := e.bareFlow("", true)
	t.Cleanup(f.close)
	// Force each request onto its own upstream connection by disabling idle reuse and limiting streams.
	f.h1 = f.newTransport([]string{"http/1.1"}, false)
	f.h2 = f.h1

	var wg sync.WaitGroup
	codes := make([]int, maxUpstreamConns+4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			host := "example.com"
			if i%2 == 1 {
				host = "two.example.com"
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "http://"+host+"/ok/stream", nil)
			req.Host = host
			f.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	deadline := time.Now().Add(2 * time.Second)
	for active.Load() < maxUpstreamConns && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()
	if maxActive.Load() != maxUpstreamConns {
		t.Fatalf("max concurrent upstream connections = %d, want %d", maxActive.Load(), maxUpstreamConns)
	}
	admitted, rejected := 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			admitted++
		case http.StatusBadGateway:
			rejected++
		default:
			t.Fatalf("unexpected response status %d in codes=%v", code, codes)
		}
	}
	if admitted != maxUpstreamConns || rejected != 4 {
		t.Fatalf("admitted=%d rejected=%d, want admitted=%d rejected=4 (codes=%v)", admitted, rejected, maxUpstreamConns, codes)
	}
}

func TestUpstreamAddressAllowedUncachedHostnameAndServerFirst(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t, "origin")
	httpsBackend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "uncached.example.com" {
			_, _ = io.WriteString(w, "https:uncached.example.com")
		}
	}))
	var handshakeMu sync.Mutex
	var handshakenSNIs []string
	httpsBackend.TLS = &tls.Config{
		Certificates: []tls.Certificate{ca.serverCert(t, "uncached.example.com", "denied.example.com", "inspected.example.com")},
		MinVersion:   tls.VersionTLS12,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			handshakeMu.Lock()
			handshakenSNIs = append(handshakenSNIs, hello.ServerName)
			handshakeMu.Unlock()
			return nil, nil
		},
	}
	httpsBackend.StartTLS()
	t.Cleanup(httpsBackend.Close)

	serverFirstBackend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverFirstBackend.Close() })
	go func() {
		for {
			c, err := serverFirstBackend.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "SSH-2.0-OpenSSH_test\r\n")
			_ = c.Close()
		}
	}()

	proxyL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxyL.Close() })
	var mu sync.Mutex
	var seenHosts []string
	go func() {
		for {
			c, err := proxyL.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				br := bufio.NewReader(c)
				r, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				mu.Lock()
				seenHosts = append(seenHosts, r.Host)
				mu.Unlock()
				target := httpsBackend.Listener.Addr().String()
				if strings.HasSuffix(r.Host, ":2222") {
					target = serverFirstBackend.Addr().String()
				}
				if _, err := io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n"); err != nil {
					return
				}
				out, err := net.DialTimeout("tcp", target, time.Second)
				if err != nil {
					return
				}
				_ = c.SetDeadline(time.Time{})
				splice(&bufConn{Conn: c, r: br}, out)
			}()
		}
	}()

	u, _ := json.Marshal(UpstreamProxy{
		Workspace: "test",
		URL:       "http://" + proxyL.Addr().String(),
	})
	caInsp := newTestCA(t, "inspect-ca")
	inspJSON, _ := json.Marshal(inspectFile{CACert: caInsp.certPath, CAKey: caInsp.keyPath})
	p, err := ParsePolicy([]byte(`{
		"allow":[
			{"cidrs":["93.184.216.34/32"]},
			{"names":["inspected.example.com"],"http":{"methods":["GET"],"paths":["/ok"]}}
		],
		"deny":[{"names":["denied.example.com"]}],
		"inspect":`+string(inspJSON)+`,
		"upstream_proxy":`+string(u)+`
	}`), "uncached-addr")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{
		Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
		Gateway: netip.MustParseAddr("192.168.127.1"),
		GuestIP: netip.MustParseAddr("192.168.127.2"),
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	// Do NOT populate b.names: guest connects using a pre-resolved IP with no DNS cache entry.
	g := connectTestBroker(t, b)

	// 1. Pre-resolved HTTPS request with SNI uncached.example.com succeeds and uses CONNECT uncached.example.com:443.
	tr := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return upstreamGuestDial(t, g, l7IP, 443)
		},
		TLSClientConfig: &tls.Config{
			RootCAs:    ca.pool,
			ServerName: "uncached.example.com",
			MinVersion: tls.VersionTLS12,
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	res, err := client.Get("https://uncached.example.com/")
	if err != nil {
		t.Fatalf("pre-resolved HTTPS with uncached SNI failed: %v", err)
	}
	if got := readBody(t, res); got != "https:uncached.example.com" {
		t.Fatalf("unexpected HTTPS body %q", got)
	}

	// 2. Server-first connection on port 2222 (no client bytes during 200ms sniff window) falls back to CONNECT 93.184.216.34:2222.
	sc, err := upstreamGuestDial(t, g, l7IP, 2222)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sc.Close() }()
	_ = sc.SetReadDeadline(time.Now().Add(2 * time.Second))
	banner, err := io.ReadAll(sc)
	if err != nil || string(banner) != "SSH-2.0-OpenSSH_test\r\n" {
		t.Fatalf("server-first banner = %q, err = %v", banner, err)
	}

	// 3. Delayed TLS handshake after the 200ms sniff window over the IP-literal tunnel:
	//    allowed SNI succeeds, while denied SNI and L7-inspected SNI are rejected.
	dialDelayedTLS := func(sni string) error {
		c, err := upstreamGuestDial(t, g, l7IP, 4443)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		time.Sleep(250 * time.Millisecond)
		tc := tls.Client(c, &tls.Config{
			RootCAs:    ca.pool,
			ServerName: sni,
			MinVersion: tls.VersionTLS12,
		})
		_ = tc.SetDeadline(time.Now().Add(2 * time.Second))
		return tc.Handshake()
	}
	if err := dialDelayedTLS("uncached.example.com"); err != nil {
		t.Fatalf("delayed TLS handshake for allowed host failed: %v", err)
	}
	if err := dialDelayedTLS("denied.example.com"); err == nil {
		t.Fatal("delayed TLS handshake for denied host succeeded on IP tunnel")
	}
	if err := dialDelayedTLS("inspected.example.com"); err == nil {
		t.Fatal("delayed TLS handshake for L7-inspected host succeeded on uninspected IP tunnel")
	}

	handshakeMu.Lock()
	gotSNIs := append([]string{}, handshakenSNIs...)
	handshakeMu.Unlock()
	wantSNIs := []string{"uncached.example.com", "uncached.example.com"}
	if !slices.Equal(gotSNIs, wantSNIs) {
		t.Fatalf("origin saw TLS ClientHellos for %v, want %v (denied and L7-inspected SNIs must never reach origin)", gotSNIs, wantSNIs)
	}

	mu.Lock()
	gotHosts := append([]string{}, seenHosts...)
	mu.Unlock()
	wantHosts := []string{"uncached.example.com:443", l7IP + ":2222", l7IP + ":4443", l7IP + ":4443", l7IP + ":4443"}
	if !slices.Equal(gotHosts, wantHosts) {
		t.Fatalf("seen CONNECT hosts = %v, want %v", gotHosts, wantHosts)
	}
}

func TestUpstreamWebSocketUpgradeDirectAndProxied(t *testing.T) {
	t.Parallel()
	holdParsed := make(chan struct{})
	holdResp := make(chan struct{})
	holdOriginBytes := make(chan int, 1)
	wsHandler := func(label string) net.Listener {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					defer func() { _ = c.Close() }()
					_ = c.SetDeadline(time.Now().Add(3 * time.Second))
					br := bufio.NewReader(c)
					req, err := http.ReadRequest(br)
					if err != nil || !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
						return
					}
					switch req.URL.Path {
					case "/early-hints":
						if _, err := io.WriteString(c, "HTTP/1.1 103 Early Hints\r\nLink: </style.css>; rel=preload\r\n\r\n"); err != nil {
							return
						}
					case "/oversize-resp":
						_, _ = io.WriteString(c, "HTTP/1.1 103 Early Hints\r\nX-Pad: "+strings.Repeat("a", sniffMax+1))
						<-holdResp
						return
					case "/hold":
						close(holdParsed)
						<-holdResp
						rest, _ := io.ReadAll(br)
						holdOriginBytes <- len(rest)
						return
					}
					if _, err := io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
						return
					}
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					_, _ = io.WriteString(c, label+":"+line)
				}()
			}
		}()
		return l
	}
	directWS := wsHandler("direct")
	proxyWS := wsHandler("proxy")

	p := newTestConnectProxy(t, "tcp", "127.0.0.1:0", "HTTP/1.1 200 OK\r\n\r\n", proxyWS.Addr().String())
	u, _ := json.Marshal(UpstreamProxy{
		Workspace: "test",
		URL:       p.url,
		Direct:    []string{"direct.example.com:80"},
	})
	b, g, _ := upstreamTestBroker(t, `{"allow":[{"cidrs":["93.184.216.34/32"]}],"upstream_proxy":`+string(u)+`}`)
	b.names.record(netip.MustParseAddr(l7IP), "direct.example.com", time.Hour)
	b.dialTCP = func(network, _ string) (net.Conn, error) {
		return net.DialTimeout(network, directWS.Addr().String(), time.Second)
	}

	checkWS := func(host, path, wantPrefix string) {
		t.Helper()
		c, err := upstreamGuestDial(t, g, l7IP, 80)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.WriteString(c, "GET "+path+" HTTP/1.1\r\nHost: "+host+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(c)
		for {
			res, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("upgrade response for %s %s: %v", host, path, err)
			}
			if res.StatusCode == http.StatusEarlyHints {
				continue
			}
			if res.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("upgrade response for %s %s = %+v", host, path, res)
			}
			break
		}
		// Send post-101 frames that look like a second HTTP request with another Host to verify raw frame streaming works.
		if _, err := io.WriteString(c, "GET / HTTP/1.1 Host: evil.example.com\n"); err != nil {
			t.Fatalf("write websocket frame for %s: %v", host, err)
		}
		got, err := br.ReadString('\n')
		if err != nil || got != wantPrefix+":GET / HTTP/1.1 Host: evil.example.com\n" {
			t.Fatalf("websocket echo for %s = %q, err = %v", host, got, err)
		}
	}

	checkWS("direct.example.com", "/ws", "direct")
	b.names.record(netip.MustParseAddr(l7IP), "proxied.example.com", time.Hour)
	checkWS("proxied.example.com", "/ws", "proxy")
	checkWS("proxied.example.com", "/early-hints", "proxy")

	// An oversized incomplete upgrade response header exceeding sniffMax must terminate the flow instead of clearing the upgrade gate.
	oversize, err := upstreamGuestDial(t, g, l7IP, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oversize.Close() }()
	_ = oversize.SetDeadline(time.Now().Add(5 * time.Second))
	oversizeStart := time.Now()
	if _, err := io.WriteString(oversize, "GET /oversize-resp HTTP/1.1\r\nHost: proxied.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_, oversizeErr := io.ReadAll(oversize)
	if oversizeErr == nil {
		t.Fatal("oversize incomplete upgrade response did not reset the flow")
	}
	var netErr net.Error
	if errors.As(oversizeErr, &netErr) && netErr.Timeout() {
		t.Fatalf("oversize incomplete upgrade response timed out instead of resetting: %v", oversizeErr)
	}
	if elapsed := time.Since(oversizeStart); elapsed > 2*time.Second {
		t.Fatalf("oversize incomplete upgrade rejection took %v, expected immediate reset", elapsed)
	}

	// A separate client write sent while the final response is still withheld — even with the same allowed Host — must be rejected immediately by the pending-upgrade guard and deliver zero bytes to the origin.
	smuggled, err := upstreamGuestDial(t, g, l7IP, 80)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = smuggled.Close() }()
	_ = smuggled.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(smuggled, "GET /hold HTTP/1.1\r\nHost: proxied.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-holdParsed:
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not receive /hold upgrade request")
	}
	start := time.Now()
	if _, err := io.WriteString(smuggled, "GET /allowed HTTP/1.1\r\nHost: proxied.example.com\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(smuggled)
	close(holdResp)
	if readErr == nil {
		t.Fatal("separate client write before 101 was not rejected")
	}
	if errors.As(readErr, &netErr) && netErr.Timeout() {
		t.Fatalf("pre-101 client write timed out instead of being rejected: %v", readErr)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("pre-101 rejection took %v, expected immediate reset", elapsed)
	}
	select {
	case gotBytes := <-holdOriginBytes:
		if gotBytes != 0 {
			t.Fatalf("origin received %d pre-101 bytes after /hold, want 0", gotBytes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for origin /hold read result")
	}
	// Deterministic unit check: while abort() is paused on an oversized or malformed upgrade response, c.upgrading stays true so concurrent Read calls still reject data.
	for _, respHead := range []string{
		"HTTP/1.1 103 Early Hints\r\nX-Pad: " + strings.Repeat("a", sniffMax+1),
		"INVALID\r\n\r\n",
	} {
		clientSide, enforceSide := net.Pipe()
		abortEntered := make(chan struct{})
		secondAbortEntered := make(chan struct{})
		releaseAbort := make(chan struct{})
		var abortCount atomic.Int32
		ec := &enforceConn{
			Conn:         enforceSide,
			allowUpgrade: true,
			check: func(string) Decision {
				return Decision{Allow: true}
			},
			abort: func() {
				switch abortCount.Add(1) {
				case 1:
					close(abortEntered)
				case 2:
					close(secondAbortEntered)
				}
				<-releaseAbort
				_ = enforceSide.Close()
			},
			closeUpstream: func() {},
		}
		reqBytes := []byte("GET /ws HTTP/1.1\r\nHost: proxied.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		go func() {
			_, _ = clientSide.Write(reqBytes)
		}()
		buf := make([]byte, len(reqBytes))
		if _, err := io.ReadFull(ec, buf); err != nil {
			t.Fatalf("initial upgrade ReadFull(%q): %v", respHead[:min(len(respHead), 12)], err)
		}
		writeDone := make(chan error, 1)
		go func() {
			_, err := ec.Write([]byte(respHead))
			writeDone <- err
		}()
		select {
		case <-abortEntered:
		case <-time.After(2 * time.Second):
			t.Fatalf("Write(%q) did not enter abort", respHead[:min(len(respHead), 12)])
		}
		// Concurrent client read while Write's abort is paused must still hit deny() rather than passing inspect().
		secondReq := []byte("GET /allowed HTTP/1.1\r\nHost: proxied.example.com\r\n\r\n")
		go func() {
			_, _ = clientSide.Write(secondReq)
		}()
		readDone := make(chan error, 1)
		go func() {
			_, err := ec.Read(make([]byte, len(secondReq)))
			readDone <- err
		}()
		select {
		case <-secondAbortEntered:
		case <-time.After(2 * time.Second):
			t.Fatalf("Read(%q) did not enter abort while Write abort was paused", respHead[:min(len(respHead), 12)])
		}
		close(releaseAbort)
		if err := <-readDone; !errors.Is(err, errPolicyDenied) {
			t.Fatalf("Read during upgrade abort(%q) = %v, want errPolicyDenied", respHead[:min(len(respHead), 12)], err)
		}
		if err := <-writeDone; !errors.Is(err, errPolicyDenied) {
			t.Fatalf("Write(%q) = %v, want errPolicyDenied", respHead[:min(len(respHead), 12)], err)
		}
		_ = clientSide.Close()
	}
}
