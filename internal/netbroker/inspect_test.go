package netbroker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// testCA is a CA like the one jailoc generates, with its files on disk.
type testCA struct {
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	pool     *x509.CertPool
	certPath string
	keyPath  string
}

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"jailoc"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ca := &testCA{cert: cert, key: key, pool: x509.NewCertPool(), certPath: filepath.Join(dir, "ca.pem"), keyPath: filepath.Join(dir, "ca.key")}
	ca.pool.AddCert(cert)
	if err := os.WriteFile(ca.certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca.keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return ca
}

// serverCert issues a TLS server certificate for names.
func (ca *testCA) serverCert(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func inspectSection(t *testing.T, ca *testCA, upstream *testCA) string {
	t.Helper()
	m := map[string]string{"ca_cert": ca.certPath, "ca_key": ca.keyPath}
	if upstream != nil {
		m["upstream_ca"] = upstream.certPath
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// l7IP is the public address the guest connects to; the broker's DNS cache
// maps it to example.com.
const l7IP = "93.184.216.34"

const (
	ruleWeb      = `{"name":"web","names":["*"]}`
	ruleWebHost  = `{"name":"web","names":["example.com"]}`
	ruleAPIRead  = `{"name":"api-read","names":["example.com"],"http":{"methods":["GET","HEAD"],"paths":["/ok/**"]}}`
	ruleNoDelete = `{"name":"no-delete","names":["example.com"],"http":{"methods":["DELETE"]}}`
)

// l7Env is a broker with an inspection CA in front of one upstream.
type l7Env struct {
	t        *testing.T
	ca       *testCA // the workspace inspection CA the guest trusts
	upCA     *testCA // signs the upstream's certificate
	upstream string
	log      *syncBuf
	broker   *Broker
	guest    *stack.Stack

	mu     sync.Mutex
	dialed []string
}

func newL7Env(t *testing.T) *l7Env {
	t.Helper()
	return &l7Env{t: t, ca: newTestCA(t, "jailoc inspection CA test"), upCA: newTestCA(t, "upstream CA"), log: &syncBuf{}}
}

func quietServer(ts *httptest.Server) {
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
}

// httpsUpstream starts an HTTPS server (HTTP/2 enabled) for example.com.
func (e *l7Env) httpsUpstream(h http.Handler) {
	e.t.Helper()
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{e.upCA.serverCert(e.t, "example.com")}, MinVersion: tls.VersionTLS12}
	ts.EnableHTTP2 = true
	quietServer(ts)
	ts.StartTLS()
	e.t.Cleanup(ts.Close)
	e.upstream = ts.Listener.Addr().String()
}

func (e *l7Env) httpUpstream(h http.Handler) {
	e.t.Helper()
	ts := httptest.NewServer(h)
	quietServer(ts)
	e.t.Cleanup(ts.Close)
	e.upstream = ts.Listener.Addr().String()
}

// rawUpstream serves handle on a plain TCP listener, or on TLS with the
// upstream certificate when useTLS is set.
func (e *l7Env) rawUpstream(useTLS bool, handle func(net.Conn)) {
	e.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	if useTLS {
		l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{e.upCA.serverCert(e.t, "example.com")}, MinVersion: tls.VersionTLS12})
	}
	e.t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				handle(c)
			}()
		}
	}()
	e.upstream = l.Addr().String()
}

// start builds the broker with the given allow and deny rules (JSON array
// contents). The upstream CA is trusted only when trustUpstream is set.
func (e *l7Env) start(allow, deny string, trustUpstream bool) {
	e.t.Helper()
	var up *testCA
	if trustUpstream {
		up = e.upCA
	}
	pol, err := ParsePolicy([]byte(`{"allow":[`+allow+`],"deny":[`+deny+`],"inspect":`+inspectSection(e.t, e.ca, up)+`}`), "l7-test")
	if err != nil {
		e.t.Fatalf("ParsePolicy: %v", err)
	}
	b, err := New(Config{
		Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
		Gateway: netip.MustParseAddr("192.168.127.1"),
		GuestIP: netip.MustParseAddr("192.168.127.2"),
		Log:     e.log,
	}, pol)
	if err != nil {
		e.t.Fatal(err)
	}
	b.names.record(netip.MustParseAddr(l7IP), "example.com", time.Hour)
	b.dialTCP = func(network, addr string) (net.Conn, error) {
		e.mu.Lock()
		e.dialed = append(e.dialed, addr)
		e.mu.Unlock()
		return net.DialTimeout(network, e.upstream, 2*time.Second)
	}
	e.broker = b
	e.guest = connectTestBroker(e.t, b)
}

func (e *l7Env) dialGuest(port uint16) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := gonet.DialContextTCP(ctx, e.guest, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(netip.MustParseAddr(l7IP).As4()), Port: port}, ipv4.ProtocolNumber)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// client is an HTTP client in the guest. It trusts only the inspection CA.
func (e *l7Env) client(h2 bool, port uint16) *http.Client {
	tr := &http.Transport{
		DialContext:       func(context.Context, string, string) (net.Conn, error) { return e.dialGuest(port) },
		TLSClientConfig:   &tls.Config{RootCAs: e.ca.pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: h2,
	}
	e.t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func (e *l7Env) events() []map[string]any {
	var out []map[string]any
	dec := json.NewDecoder(strings.NewReader(e.log.String()))
	for dec.More() {
		var ev map[string]any
		if err := dec.Decode(&ev); err != nil {
			e.t.Fatalf("decode log: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

// find returns the events of the given kind whose fields all match.
func (e *l7Env) find(event string, match map[string]any) []map[string]any {
	var out []map[string]any
	for _, ev := range e.events() {
		if ev["event"] != event {
			continue
		}
		ok := true
		for k, v := range match {
			if ev[k] != v {
				ok = false
			}
		}
		if ok {
			out = append(out, ev)
		}
	}
	return out
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestInspectNarrowsHost(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		name := "http/1.1"
		if h2 {
			name = "h2"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			var seen sync.Map
			e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen.Store(r.Method+" "+r.URL.Path, true)
				if len(r.Header.Values("X-Forwarded-For")) != 0 {
					t.Errorf("upstream got X-Forwarded-For %v", r.Header.Values("X-Forwarded-For"))
				}
				_, _ = io.WriteString(w, "upstream "+r.Proto) //nolint:gosec // G705: test upstream echoes the protocol version
			}))
			// A plain wildcard allow rule opens every host; the http rule
			// narrows example.com to read-only requests under /ok.
			e.start(ruleWeb+","+ruleAPIRead, "", true)
			client := e.client(h2, 443)

			get := func(method, path string) (*http.Response, bool) {
				var reused bool
				ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }})
				req, err := http.NewRequestWithContext(ctx, method, "https://example.com"+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				return resp, reused
			}

			resp, _ := get("GET", "/ok/a?token=secret")
			if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
				t.Fatal("no TLS state on response")
			}
			leaf := resp.TLS.PeerCertificates[0]
			if leaf.Issuer.CommonName != e.ca.cert.Subject.CommonName || !strings.Contains(strings.Join(leaf.DNSNames, ","), "example.com") {
				t.Fatalf("guest saw certificate issuer=%q dns=%v, want leaf for example.com from the inspection CA", leaf.Issuer.CommonName, leaf.DNSNames)
			}
			wantProto := 1
			if h2 {
				wantProto = 2
			}
			if resp.StatusCode != 200 || resp.ProtoMajor != wantProto {
				t.Fatalf("GET /ok/a = %d proto %s, want 200 on HTTP/%d", resp.StatusCode, resp.Proto, wantProto)
			}
			if body := readBody(t, resp); !strings.HasPrefix(body, "upstream ") {
				t.Fatalf("body = %q", body)
			}

			denied := []struct{ method, path string }{{"POST", "/ok/a"}, {"GET", "/other"}, {"GET", "/ok2"}}
			for _, d := range denied {
				resp, _ := get(d.method, d.path)
				body := readBody(t, resp)
				if resp.StatusCode != 403 || body != "blocked by network policy (reason=http-no-match)\n" {
					t.Fatalf("%s %s = %d %q, want 403 http-no-match", d.method, d.path, resp.StatusCode, body)
				}
			}
			// The denied requests did not close the connection.
			resp, reused := get("HEAD", "/ok/b")
			_ = readBody(t, resp)
			if resp.StatusCode != 200 || !reused {
				t.Fatalf("HEAD after denials = %d reused=%v, want 200 on the same connection", resp.StatusCode, reused)
			}

			for _, k := range []string{"POST /ok/a", "GET /other", "GET /ok2"} {
				if _, ok := seen.Load(k); ok {
					t.Fatalf("denied request %q reached upstream", k)
				}
			}
			e.mu.Lock()
			for _, a := range e.dialed {
				if a != l7IP+":443" {
					t.Fatalf("broker dialed %q, want only %s:443", a, l7IP)
				}
			}
			e.mu.Unlock()

			if evs := e.find("l7", map[string]any{"method": "GET", "path": "/ok/a", "status": float64(200), "rule": "api-read", "proto": "https", "host": "example.com"}); len(evs) != 1 {
				t.Fatalf("allowed l7 events = %v\n%s", evs, e.log.String())
			}
			if evs := e.find("l7", map[string]any{"method": "POST", "path": "/ok/a", "status": float64(403), "denied": true, "reason": "http-no-match"}); len(evs) != 1 {
				t.Fatalf("denied l7 events = %v\n%s", evs, e.log.String())
			}
			if strings.Contains(e.log.String(), "secret") {
				t.Fatal("query string leaked into events")
			}
		})
	}
}

func TestInspectHTTPDenyRule(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	e.start(ruleWeb, ruleNoDelete, true)
	client := e.client(false, 443)

	do := func(method, path string) (int, string) {
		req, err := http.NewRequest(method, "https://example.com"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp.StatusCode, readBody(t, resp)
	}
	if code, body := do("DELETE", "/x"); code != 403 || body != "blocked by network policy (rule=no-delete)\n" {
		t.Fatalf("DELETE = %d %q, want 403 naming no-delete", code, body)
	}
	if code, body := do("delete", "/x"); code != 403 || !strings.Contains(body, "rule=no-delete") {
		t.Fatalf("lowercase delete = %d %q, want 403 naming no-delete", code, body)
	}
	if code, _ := do("GET", "/x"); code != 200 {
		t.Fatalf("GET = %d, want 200", code)
	}
	if evs := e.find("l7", map[string]any{"method": "DELETE", "denied": true, "rule": "no-delete", "reason": "rule no-delete", "status": float64(403)}); len(evs) != 1 {
		t.Fatalf("deny events = %v\n%s", evs, e.log.String())
	}
}

func TestInspectRejectsRequests(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	var hits sync.Map
	e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Store(r.RequestURI, true)
		_, _ = io.WriteString(w, "ok")
	}))
	// The rule lists a path pattern, so every request path is canonicalized.
	e.start(ruleWeb+`,{"name":"all","names":["example.com"],"http":{"paths":["/**"]}}`, "", true)

	// Each request gets its own connection: net/http closes the connection
	// after denying a CONNECT, so later requests cannot share it.
	roundTrip := func(raw string) (int, string) {
		t.Helper()
		conn, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		tc := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: e.ca.pool, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
		if err := tc.Handshake(); err != nil {
			t.Fatalf("handshake: %v", err)
		}
		br := bufio.NewReader(tc)
		if _, err := io.WriteString(tc, raw); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("read response to %q: %v", raw, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	tests := []struct {
		name   string
		raw    string
		status int
		reason string
	}{
		{"host mismatch", "GET / HTTP/1.1\r\nHost: other.example.net\r\n\r\n", 421, "reason=host-mismatch"},
		{"host with port matches", "GET /fine HTTP/1.1\r\nHost: example.com:443\r\n\r\n", 200, ""},
		{"connect", "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n", 403, "reason=connect-denied"},
		{"double slash", "GET //etc HTTP/1.1\r\nHost: example.com\r\n\r\n", 403, "reason=bad-path"},
		{"encoded slash is one segment", "GET /a%2Fb HTTP/1.1\r\nHost: example.com\r\n\r\n", 200, ""},
		{"encoded slash traversal", "GET /x%2F..%2Fadmin HTTP/1.1\r\nHost: example.com\r\n\r\n", 403, "reason=bad-path"},
		{"leading encoded slash", "GET /%2Fadmin HTTP/1.1\r\nHost: example.com\r\n\r\n", 403, "reason=bad-path"},
		{"encoded dot dot", "GET /%2e%2e/etc HTTP/1.1\r\nHost: example.com\r\n\r\n", 403, "reason=bad-path"},
		{"encoded nul", "GET /a%00 HTTP/1.1\r\nHost: example.com\r\n\r\n", 403, "reason=bad-path"},
		{"backslash", "GET /a\\b HTTP/1.1\r\nHost: example.com\r\n\r\n", 403, "reason=bad-path"},
		{"absolute form other host", "GET http://other.example.net/ HTTP/1.1\r\nHost: example.com\r\n\r\n", 421, "reason=host-mismatch"},
	}
	for _, tt := range tests {
		status, body := roundTrip(tt.raw)
		if status != tt.status || (tt.reason != "" && !strings.Contains(body, tt.reason)) {
			t.Fatalf("%s: got %d %q, want %d containing %q", tt.name, status, body, tt.status, tt.reason)
		}
	}
	for _, uri := range []string{"//etc", "/x%2F..%2Fadmin", "/%2Fadmin", "/%2e%2e/etc", "/a%00", "/a\\b"} {
		if _, ok := hits.Load(uri); ok {
			t.Fatalf("rejected path %q reached upstream", uri)
		}
	}
	if evs := e.find("l7", map[string]any{"status": float64(421), "denied": true, "reason": "host-mismatch", "host": "other.example.net"}); len(evs) == 0 {
		t.Fatalf("no host-mismatch event:\n%s", e.log.String())
	}
	if evs := e.find("l7", map[string]any{"status": float64(403), "denied": true, "reason": "connect-denied"}); len(evs) != 1 {
		t.Fatalf("connect events = %v", evs)
	}
}

// TestInspectPathNotEvaluated covers an http rule without path patterns: the
// request path is not canonicalized, and reaches the upstream and the l7 event
// as the guest sent it, escaped.
func TestInspectPathNotEvaluated(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		name := "http/1.1"
		if h2 {
			name = "h2"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			var mu sync.Mutex
			var got []string
			e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				got = append(got, r.RequestURI)
				mu.Unlock()
				_, _ = io.WriteString(w, "ok")
			}))
			e.start(ruleWeb+`,{"name":"any","names":["example.com"],"http":{"methods":["*"]}}`, "", true)
			client := e.client(h2, 443)
			paths := []string{
				"/%40tarquinen%2Fopencode-dcp/latest",
				"/a%2Fb/%2e%2e/c",
				"//x",
				"/a%5Cb",
			}
			for _, path := range paths {
				req, err := http.NewRequest(http.MethodGet, "https://example.com"+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				if body := readBody(t, resp); resp.StatusCode != 200 || body != "ok" {
					t.Fatalf("GET %s = %d %q, want 200", path, resp.StatusCode, body)
				}
				if evs := e.find("l7", map[string]any{"method": "GET", "path": path, "status": float64(200), "rule": "any"}); len(evs) != 1 {
					t.Fatalf("l7 events for %s = %v\n%s", path, evs, e.log.String())
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(got, paths) {
				t.Fatalf("upstream saw %q, want %q", got, paths)
			}
			if evs := e.find("l7", map[string]any{"reason": "bad-path"}); len(evs) != 0 {
				t.Fatalf("bad-path events: %v", evs)
			}
		})
	}
}

func TestInspectStreamsResponses(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		name := "http/1.1"
		if h2 {
			name = "h2"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			release := make(chan struct{})
			e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: one\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
					_, _ = io.WriteString(w, "data: two\n\n")
				case <-r.Context().Done():
				}
			}))
			e.start(ruleWeb+","+ruleAPIRead, "", true)
			resp, err := e.client(h2, 443).Get("https://example.com/ok/events")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			br := bufio.NewReader(resp.Body)
			lines := make(chan string, 4)
			go func() {
				for {
					l, err := br.ReadString('\n')
					if err != nil {
						close(lines)
						return
					}
					lines <- l
				}
			}()
			select {
			case l := <-lines:
				if l != "data: one\n" {
					t.Fatalf("first line = %q", l)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("first event not flushed while the response is still open")
			}
			close(release)
			select {
			case l := <-lines:
				if l != "\n" {
					t.Fatalf("second line = %q", l)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("rest of the stream not delivered")
			}
		})
	}
}

func TestInspectUntrustedUpstream(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	// The upstream CA is not given: the upstream certificate cannot verify.
	e.start(ruleWeb+","+ruleAPIRead, "", false)
	resp, err := e.client(false, 443).Get("https://example.com/ok/a")
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != 502 || !strings.Contains(body, "certificate") {
		t.Fatalf("GET = %d %q, want 502 about the certificate", resp.StatusCode, body)
	}
	evs := e.find("l7", map[string]any{"status": float64(502)})
	if len(evs) != 1 || !strings.Contains(fmt.Sprint(evs[0]["hint"]), "upstream certificate failed verification") || evs[0]["error"] == nil {
		t.Fatalf("502 events = %v\n%s", evs, e.log.String())
	}
}

func TestInspectGuestRejectsCA(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	e.start(ruleWeb+","+ruleAPIRead, "", true)
	conn, err := e.dialGuest(443)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	// The guest trusts a different CA.
	other := newTestCA(t, "someone else")
	tc := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: other.pool, MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err == nil {
		t.Fatal("handshake with an untrusted CA succeeded")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.find("inspect-error", map[string]any{"name": "example.com"})) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no inspect-error event:\n%s", e.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	ev := e.find("inspect-error", nil)[0]
	if !strings.Contains(fmt.Sprint(ev["hint"]), "does not trust the workspace inspection CA") || ev["dst"] != l7IP+":443" {
		t.Fatalf("inspect-error = %v", ev)
	}
}

func TestInspectNonHTTPALPN(t *testing.T) {
	t.Parallel()
	hello := clientHelloBytes(t, "example.com", []string{"foo"})
	t.Run("allowed by a plain rule and covered by no http rule is passed through undecrypted", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		got := make(chan []byte, 1)
		e.rawUpstream(false, func(c net.Conn) {
			b := make([]byte, len(hello))
			_, _ = io.ReadFull(c, b)
			got <- b
			_, _ = c.Write([]byte("upstream-ok"))
		})
		e.start(ruleWebHost+`,{"name":"gh","names":["api.github.com"],"http":{"methods":["GET"]}}`, "", true)
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(hello); err != nil {
			t.Fatal(err)
		}
		reply, err := io.ReadAll(c)
		if err != nil || string(reply) != "upstream-ok" {
			t.Fatalf("reply = %q, %v; want the raw upstream's bytes", reply, err)
		}
		if !bytes.Equal(<-got, hello) {
			t.Fatal("upstream did not receive the ClientHello unchanged")
		}
		if evs := e.find("l7", map[string]any{"proto": "https"}); len(evs) != 0 {
			t.Fatalf("passed-through flow produced http events: %v", evs)
		}
	})
	t.Run("http rule alone does not open it", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(false, func(c net.Conn) { t.Error("upstream dialed for a denied flow") })
		e.start(ruleAPIRead, "", true)
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Write(hello)
		reply, _ := io.ReadAll(c)
		if want := []byte{0x15, hello[1], hello[2], 0, 2, 2, 49}; !bytes.Equal(reply, want) {
			t.Fatalf("reply = %x, want access_denied alert %x", reply, want)
		}
	})
}

// dialALPN completes a TLS handshake with the inspection CA, offering alpn,
// and returns the connection with the protocol the broker negotiated.
func dialALPN(t *testing.T, e *l7Env, alpn []string) *tls.Conn {
	t.Helper()
	c, err := e.dialGuest(443)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	tc := tls.Client(c, &tls.Config{ServerName: "example.com", RootCAs: e.ca.pool, NextProtos: alpn, MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	return tc
}

// alpnBlindUpstream starts an HTTPS upstream for example.com that offers no
// ALPN protocol, so it serves whatever arrives, and counts requests by
// "METHOD path".
func (e *l7Env) alpnBlindUpstream(seen *sync.Map) {
	e.t.Helper()
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := seen.LoadOrStore(r.Method+" "+r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		_, _ = io.WriteString(w, "upstream ok")
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{e.upCA.serverCert(e.t, "example.com")}, MinVersion: tls.VersionTLS12, NextProtos: []string{}}
	quietServer(ts)
	ts.StartTLS()
	e.t.Cleanup(ts.Close)
	e.upstream = ts.Listener.Addr().String()
}

// seenCount is how many requests alpnBlindUpstream got for key.
func seenCount(seen *sync.Map, key string) int32 {
	if n, ok := seen.Load(key); ok {
		return n.(*atomic.Int32).Load()
	}
	return 0
}

// guestRequest writes one HTTP/1.1 request with an empty body and reads the
// response.
func guestRequest(t *testing.T, tc net.Conn, br *bufio.Reader, method, path string) (int, string) {
	t.Helper()
	if _, err := io.WriteString(tc, method+" "+path+" HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp.StatusCode, readBody(t, resp)
}

func TestInspectNonHTTPALPNInspected(t *testing.T) {
	t.Parallel()
	t.Run("http inside is checked by the http allow rules", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		var seen sync.Map
		e.alpnBlindUpstream(&seen)
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		tc := dialALPN(t, e, []string{"foo"})
		if p := tc.ConnectionState().NegotiatedProtocol; p != "foo" {
			t.Fatalf("negotiated ALPN %q, want the guest's foo", p)
		}
		br := bufio.NewReader(tc)
		if code, body := guestRequest(t, tc, br, "POST", "/forbidden"); code != 403 || !strings.Contains(body, "http-no-match") {
			t.Fatalf("POST = %d %q, want 403 http-no-match", code, body)
		}
		if code, body := guestRequest(t, tc, br, "GET", "/ok/x"); code != 200 || body != "upstream ok" {
			t.Fatalf("GET = %d %q, want 200", code, body)
		}
		if n := seenCount(&seen, "POST /forbidden"); n != 0 {
			t.Fatalf("denied POST reached upstream %d times", n)
		}
		if n := seenCount(&seen, "GET /ok/x"); n != 1 {
			t.Fatalf("allowed GET reached upstream %d times, want 1", n)
		}
		if evs := e.find("l7", map[string]any{"proto": "https", "method": "POST", "denied": true, "reason": "http-no-match", "status": float64(403)}); len(evs) != 1 {
			t.Fatalf("deny events = %v\n%s", evs, e.log.String())
		}
		if evs := e.find("l7", map[string]any{"proto": "https", "method": "GET", "rule": "api-read", "status": float64(200)}); len(evs) != 1 {
			t.Fatalf("allow events = %v\n%s", evs, e.log.String())
		}
	})
	t.Run("http inside is checked by an http deny rule", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		var seen sync.Map
		e.alpnBlindUpstream(&seen)
		e.start(ruleWebHost, ruleNoDelete, true)
		tc := dialALPN(t, e, []string{"foo"})
		br := bufio.NewReader(tc)
		if code, body := guestRequest(t, tc, br, "DELETE", "/x"); code != 403 || !strings.Contains(body, "rule=no-delete") {
			t.Fatalf("DELETE = %d %q, want 403 naming no-delete", code, body)
		}
		if code, _ := guestRequest(t, tc, br, "GET", "/x"); code != 200 {
			t.Fatalf("GET = %d, want 200", code)
		}
		if n := seenCount(&seen, "DELETE /x"); n != 0 {
			t.Fatalf("denied DELETE reached upstream %d times", n)
		}
		if evs := e.find("l7", map[string]any{"method": "DELETE", "denied": true, "rule": "no-delete", "reason": "rule no-delete", "status": float64(403)}); len(evs) != 1 {
			t.Fatalf("deny events = %v\n%s", evs, e.log.String())
		}
	})
	t.Run("the first protocol the guest offers is negotiated", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		var seen sync.Map
		e.alpnBlindUpstream(&seen)
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		tc := dialALPN(t, e, []string{"bar", "foo"})
		if p := tc.ConnectionState().NegotiatedProtocol; p != "bar" {
			t.Fatalf("negotiated ALPN %q, want bar", p)
		}
	})
	t.Run("h2 inside is checked by the http rules", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		var seen sync.Map
		e.alpnBlindUpstream(&seen)
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		tc := dialALPN(t, e, []string{"foo"})
		tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return tc, nil }}
		tr.Protocols = new(http.Protocols)
		tr.Protocols.SetUnencryptedHTTP2(true)
		t.Cleanup(tr.CloseIdleConnections)
		client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
		resp, err := client.Post("http://example.com/forbidden", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		if body := readBody(t, resp); resp.ProtoMajor != 2 || resp.StatusCode != 403 || !strings.Contains(body, "http-no-match") {
			t.Fatalf("POST = HTTP/%d %d %q, want an HTTP/2 403 http-no-match", resp.ProtoMajor, resp.StatusCode, body)
		}
		if n := seenCount(&seen, "POST /forbidden"); n != 0 {
			t.Fatalf("denied POST reached upstream %d times", n)
		}
	})
	t.Run("a malformed request line inside is closed unforwarded", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, func(c net.Conn) { t.Error("upstream reached by a malformed request line") })
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		tc := dialALPN(t, e, []string{"foo"})
		if _, err := io.WriteString(tc, malformedLines[0].raw); err != nil {
			t.Fatal(err)
		}
		if n, err := tc.Read(make([]byte, 16)); err == nil {
			t.Fatalf("read %d bytes, want the flow closed", n)
		}
		e.waitEvent("inspect-error", map[string]any{"reason": "malformed-request-line"})
	})
	t.Run("another protocol inside is relayed with the guest's ALPN offered upstream", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		offered := make(chan []string, 1)
		negotiated := make(chan string, 1)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		tl := tls.NewListener(l, &tls.Config{
			Certificates: []tls.Certificate{e.upCA.serverCert(t, "example.com")},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"foo"},
			GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				offered <- append([]string(nil), h.SupportedProtos...)
				return nil, nil
			},
		})
		go func() {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
			b := make([]byte, 64)
			n, _ := c.Read(b)
			negotiated <- c.(*tls.Conn).ConnectionState().NegotiatedProtocol
			_, _ = c.Write(append([]byte("echo:"), b[:n]...))
		}()
		e.upstream = l.Addr().String()
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		tc := dialALPN(t, e, []string{"foo"})
		if _, err := io.WriteString(tc, "hello\n"); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len("echo:hello\n"))
		if _, err := io.ReadFull(tc, got); err != nil || string(got) != "echo:hello\n" {
			t.Fatalf("relayed reply = %q, %v", got, err)
		}
		if p := <-offered; len(p) != 1 || p[0] != "foo" {
			t.Fatalf("upstream was offered ALPN %v, want [foo]", p)
		}
		if p := <-negotiated; p != "foo" {
			t.Fatalf("upstream negotiated ALPN %q, want foo", p)
		}
	})
	t.Run("another protocol inside is not relayed to an untrusted upstream", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, func(c net.Conn) {
			b := make([]byte, 64)
			if n, _ := c.Read(b); n > 0 {
				t.Errorf("untrusted upstream received %q", b[:n])
			}
		})
		e.start(ruleWebHost+","+ruleAPIRead, "", false)
		tc := dialALPN(t, e, []string{"foo"})
		_, _ = io.WriteString(tc, "hello\n")
		if n, err := tc.Read(make([]byte, 16)); err == nil {
			t.Fatalf("read %d bytes, want the flow closed", n)
		}
		e.waitEvent("inspect-error", nil)
	})
	t.Run("a server-first protocol inside is relayed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, func(c net.Conn) {
			_, _ = c.Write([]byte("* OK ready\r\n"))
			_, _ = io.Copy(io.Discard, c)
		})
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		e.broker.policy.Load().inspect.peekTimeout = 100 * time.Millisecond
		tc := dialALPN(t, e, []string{"foo"})
		got := make([]byte, len("* OK ready\r\n"))
		if _, err := io.ReadFull(tc, got); err != nil || string(got) != "* OK ready\r\n" {
			t.Fatalf("greeting = %q, %v", got, err)
		}
	})
	t.Run("http after the peek timed out is closed unforwarded", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		got := make(chan []byte, 1)
		e.rawUpstream(true, func(c net.Conn) {
			b, _ := io.ReadAll(c)
			got <- b
		})
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		e.broker.policy.Load().inspect.peekTimeout = 100 * time.Millisecond
		tc := dialALPN(t, e, []string{"foo"})
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(tc, "POST /ok/a HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n")
		if n, err := tc.Read(make([]byte, 16)); err == nil {
			t.Fatalf("read %d bytes, want the flow closed", n)
		}
		select {
		case b := <-got:
			if len(b) != 0 {
				t.Fatalf("upstream received %q, want nothing", b)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("upstream connection never ended")
		}
		e.waitEvent("inspect-error", map[string]any{"reason": "http-after-silence"})
	})
}

func TestInspectNoALPNNotHTTP(t *testing.T) {
	t.Parallel()
	handshake := func(t *testing.T, e *l7Env) *tls.Conn {
		t.Helper()
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		tc := tls.Client(c, &tls.Config{ServerName: "example.com", RootCAs: e.ca.pool, MinVersion: tls.VersionTLS12})
		if err := tc.Handshake(); err != nil {
			t.Fatalf("handshake: %v", err)
		}
		if p := tc.ConnectionState().NegotiatedProtocol; p != "" {
			t.Fatalf("negotiated ALPN %q, want none", p)
		}
		return tc
	}
	t.Run("allowed by a plain rule is relayed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, func(c net.Conn) {
			b := make([]byte, 64)
			n, _ := c.Read(b)
			_, _ = c.Write(append([]byte("echo:"), b[:n]...))
		})
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		tc := handshake(t, e)
		if _, err := io.WriteString(tc, "HELLO\r\n"); err != nil {
			t.Fatal(err)
		}
		_ = tc.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len("echo:HELLO\r\n"))
		if _, err := io.ReadFull(tc, got); err != nil || string(got) != "echo:HELLO\r\n" {
			t.Fatalf("relayed reply = %q, %v", got, err)
		}
	})
	t.Run("denied by the plain rules is closed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, func(c net.Conn) { t.Error("upstream dialed for a denied flow") })
		e.start(ruleAPIRead, "", true)
		tc := handshake(t, e)
		_, _ = io.WriteString(tc, "HELLO\r\n")
		_ = tc.SetReadDeadline(time.Now().Add(5 * time.Second))
		if n, err := tc.Read(make([]byte, 16)); err == nil {
			t.Fatalf("read %d bytes from a denied flow", n)
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(e.find("inspect-error", map[string]any{"error": "not HTTP"})) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("no not-HTTP event:\n%s", e.log.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
		if ev := e.find("inspect-error", map[string]any{"error": "not HTTP"})[0]; ev["reason"] != "default-deny" {
			t.Fatalf("event = %v, want the plain decision reason", ev)
		}
	})
	t.Run("http after decryption without ALPN is served", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
		e.start(ruleWeb+","+ruleAPIRead, "", true)
		tc := handshake(t, e)
		_, _ = io.WriteString(tc, "POST /ok/a HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
		if err != nil {
			t.Fatal(err)
		}
		if body := readBody(t, resp); resp.StatusCode != 403 || !strings.Contains(body, "http-no-match") {
			t.Fatalf("POST = %d %q, want 403", resp.StatusCode, body)
		}
	})
}

func TestInspectPlainHTTP(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	var seen sync.Map
	e.httpUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Method+" "+r.URL.Path, true)
		_, _ = io.WriteString(w, "plain ok")
	}))
	e.start(ruleWeb+","+ruleAPIRead, "", false)
	client := e.client(false, 80)

	resp, err := client.Get("http://example.com/ok/a")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); resp.StatusCode != 200 || body != "plain ok" {
		t.Fatalf("GET = %d %q", resp.StatusCode, body)
	}
	resp, err = client.Post("http://example.com/ok/a", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); resp.StatusCode != 403 || body != "blocked by network policy (reason=http-no-match)\n" {
		t.Fatalf("POST = %d %q, want 403", resp.StatusCode, body)
	}
	if _, ok := seen.Load("POST /ok/a"); ok {
		t.Fatal("denied POST reached upstream")
	}
	if evs := e.find("l7", map[string]any{"proto": "http", "method": "GET", "status": float64(200), "rule": "api-read"}); len(evs) != 1 {
		t.Fatalf("l7 events = %v\n%s", evs, e.log.String())
	}
	if evs := e.find("l7", map[string]any{"proto": "http", "method": "POST", "status": float64(403), "denied": true}); len(evs) != 1 {
		t.Fatalf("denied l7 events = %v\n%s", evs, e.log.String())
	}
}

func TestInspectUpgrade(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			_, _ = io.WriteString(w, "plain")
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = brw.Flush()
		b := make([]byte, 64)
		n, _ := brw.Read(b)
		_, _ = conn.Write(append([]byte("echo:"), b[:n]...))
	}))
	e.start(ruleWeb+","+ruleAPIRead, "", true)

	open := func(path string) (*bufio.Reader, *tls.Conn) {
		t.Helper()
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		tc := tls.Client(c, &tls.Config{ServerName: "example.com", RootCAs: e.ca.pool, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
		if _, err := io.WriteString(tc, "GET "+path+" HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		return bufio.NewReader(tc), tc
	}

	br, tc := open("/ok/ws")
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade = %v, %v; want 101", resp, err)
	}
	if _, err := io.WriteString(tc, "ping"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("echo:ping"))
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "echo:ping" {
		t.Fatalf("after upgrade got %q, %v; want the tunnelled echo", got, err)
	}
	if evs := e.find("l7", map[string]any{"path": "/ok/ws", "status": float64(101)}); len(evs) != 1 {
		t.Fatalf("101 events = %v\n%s", evs, e.log.String())
	}

	br, _ = open("/other/ws")
	resp, err = http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("denied upgrade = %v, %v; want 403", resp, err)
	}
}

func TestInspectH2CUpgradeStripped(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	var mu sync.Mutex
	var seen http.Header
	e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		if r.Header.Get("Upgrade") == "h2c" {
			// A server that took the upgrade would tunnel the connection.
			conn, brw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n")
			_ = brw.Flush()
			return
		}
		_, _ = io.WriteString(w, "plain")
	}))
	e.start(ruleWeb+","+ruleAPIRead, "", true)

	c, err := e.dialGuest(443)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	tc := tls.Client(c, &tls.Config{ServerName: "example.com", RootCAs: e.ca.pool, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	req := "GET /ok/a HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABkAAQCAAAAAAIAAAAA\r\n\r\n"
	if _, err := io.WriteString(tc, req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); resp.StatusCode != 200 || body != "plain" {
		t.Fatalf("h2c upgrade request = %d %q, want a plain 200", resp.StatusCode, body)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, k := range []string{"Upgrade", "Http2-Settings"} {
		if v := seen.Values(k); len(v) != 0 {
			t.Errorf("upstream saw %s: %v", k, v)
		}
	}
	if v := seen.Get("Connection"); strings.Contains(strings.ToLower(v), "upgrade") {
		t.Errorf("upstream saw Connection: %q", v)
	}
}

func TestStripH2CUpgrade(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   http.Header
		want http.Header
	}{
		{"h2c only", http.Header{"Connection": {"Upgrade, HTTP2-Settings"}, "Upgrade": {"h2c"}, "Http2-Settings": {"x"}}, http.Header{}},
		{"mixed case", http.Header{"Connection": {"upgrade"}, "Upgrade": {"H2C"}}, http.Header{}},
		{"keeps other connection tokens", http.Header{"Connection": {"keep-alive, Upgrade, HTTP2-Settings"}, "Upgrade": {"h2c"}, "Http2-Settings": {"x"}}, http.Header{"Connection": {"keep-alive"}}},
		{"comma list keeps websocket", http.Header{"Connection": {"Upgrade, HTTP2-Settings"}, "Upgrade": {"websocket, h2c"}, "Http2-Settings": {"x"}}, http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}},
		{"repeated headers", http.Header{"Connection": {"Upgrade", "HTTP2-Settings"}, "Upgrade": {"h2c", "websocket"}, "Http2-Settings": {"x"}}, http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}},
		{"websocket untouched", http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}, http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}},
		{"no upgrade", http.Header{"Accept": {"*/*"}}, http.Header{"Accept": {"*/*"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stripH2CUpgrade(tt.in)
			if fmt.Sprint(tt.in) != fmt.Sprint(tt.want) {
				t.Fatalf("headers = %v, want %v", tt.in, tt.want)
			}
		})
	}
}

func TestAuthorityHosts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"name", "Example.COM", "example.com"},
		{"name and port", "example.com:8443", "example.com"},
		{"trailing dot", "example.com.", "example.com"},
		{"ipv4", "93.184.216.34", "93.184.216.34"},
		{"ipv4 and port", "93.184.216.34:80", "93.184.216.34"},
		{"bracketed ipv6", "[2606:4700:4700::1111]", "2606:4700:4700::1111"},
		{"bracketed ipv6 and port", "[2606:4700:4700::1111]:443", "2606:4700:4700::1111"},
		{"expanded ipv6", "[2606:4700:4700:0:0:0:0:1111]", "2606:4700:4700::1111"},
		{"expanded ipv6 and port", "[2606:4700:4700:0000:0000:0000:0000:1111]:443", "2606:4700:4700::1111"},
		{"uppercase ipv6", "[2606:4700:4700::ABCD]", "2606:4700:4700::abcd"},
		{"v4-mapped ipv6", "[::ffff:93.184.216.34]:80", "93.184.216.34"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := requestHost(tt.in); got != tt.want {
				t.Errorf("requestHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if got := normalizeAuthority(tt.in); got != tt.want {
				t.Errorf("normalizeAuthority(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInspectIPv6Authority(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	var hits atomic.Int32
	e.httpUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	e.start(ruleWeb+","+ruleAPIRead, "", false)
	dst := netip.MustParseAddrPort("[2606:4700:4700::1111]:80")
	p := e.broker.policy.Load()
	f := e.broker.newInspectFlow(p, e.broker.env(p, dst.Addr()), Decision{Allow: true}, httpScope{}, 1, dst, dst.Addr().String(), false, false)
	f.byIP = true
	t.Cleanup(f.close)

	tests := []struct {
		name, host string
		want       int
	}{
		{"compressed literal", "[2606:4700:4700::1111]", 200},
		{"compressed literal and port", "[2606:4700:4700::1111]:80", 200},
		{"expanded literal", "[2606:4700:4700:0:0:0:0:1111]", 200},
		{"expanded literal and port", "[2606:4700:4700:0000:0000:0000:0000:1111]:80", 200},
		{"uppercase literal", "[2606:4700:4700::1111]", 200},
		{"another address", "[2606:4700:4700::1112]", 421},
		{"another expanded address", "[2606:4700:4700:0:0:0:0:1112]:80", 421},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := hits.Load()
			req := httptest.NewRequest(http.MethodGet, "/ok/a", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			f.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("Host %q = %d %q, want %d", tt.host, rec.Code, rec.Body.String(), tt.want)
			}
			if reached := hits.Load() - before; (tt.want == 200) != (reached == 1) {
				t.Fatalf("Host %q: upstream reached %d times for a %d answer", tt.host, reached, rec.Code)
			}
		})
	}
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// serverCertIPs issues a TLS server certificate for IP addresses.
func (ca *testCA) serverCertIPs(t *testing.T, ips ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, s := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(s))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// h2cUpstream starts a cleartext server that speaks HTTP/1.1 and HTTP/2
// with prior knowledge.
func (e *l7Env) h2cUpstream(h http.Handler) {
	e.t.Helper()
	ts := httptest.NewUnstartedServer(h)
	ts.Config.Protocols = new(http.Protocols)
	ts.Config.Protocols.SetHTTP1(true)
	ts.Config.Protocols.SetUnencryptedHTTP2(true)
	quietServer(ts)
	ts.Start()
	e.t.Cleanup(ts.Close)
	e.upstream = ts.Listener.Addr().String()
}

// h2cClient is an HTTP/2 prior-knowledge client in the guest.
func (e *l7Env) h2cClient(port uint16) *http.Client {
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return e.dialGuest(port) }}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetUnencryptedHTTP2(true)
	e.t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// bareFlow is an inspected flow without a guest, served through its own
// HTTP server, for tests of the per-request handling.
func (e *l7Env) bareFlow(peerName string, h2c bool) *inspectFlow {
	e.t.Helper()
	dst := netip.MustParseAddrPort(l7IP + ":443")
	p := e.broker.policy.Load()
	return e.broker.newInspectFlow(p, e.broker.env(p, dst.Addr()), Decision{Allow: true}, httpScope{}, 1, dst, peerName, false, h2c)
}

// dialNoALPN completes a TLS handshake with the inspection CA that offers no
// ALPN protocol.
func dialNoALPN(t *testing.T, e *l7Env) *tls.Conn {
	t.Helper()
	c, err := e.dialGuest(443)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	tc := tls.Client(c, &tls.Config{ServerName: "example.com", RootCAs: e.ca.pool, MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	return tc
}

// waitEvent waits for an event to be logged.
func (e *l7Env) waitEvent(event string, match map[string]any) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if evs := e.find(event, match); len(evs) > 0 {
			return evs[0]
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("no %s event matching %v:\n%s", event, match, e.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestInspectKeepAliveHostSwitch(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	var seen sync.Map
	e.httpUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Method+" "+r.URL.Path, true)
		_, _ = io.WriteString(w, "plain ok")
	}))
	// A plain wildcard allow rule opens every host; example.com is narrowed by
	// an http rule, other.test is not.
	e.start(ruleWeb+","+ruleAPIRead, "", false)
	c, err := e.dialGuest(80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)

	if _, err := io.WriteString(c, "GET /plain HTTP/1.1\r\nHost: other.test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if body := readBody(t, resp); resp.StatusCode != 200 || body != "plain ok" {
		t.Fatalf("first request = %d %q, want 200", resp.StatusCode, body)
	}
	// The same connection now asks for the host the http rule narrows.
	if _, err := io.WriteString(c, "POST /ok/a HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.ReadResponse(br, nil); err == nil {
		t.Fatalf("second request answered %d, want the connection reset", resp.StatusCode)
	}
	if _, ok := seen.Load("POST /ok/a"); ok {
		t.Fatal("POST to the http-restricted host reached upstream over the uninspected connection")
	}
}

func TestInspectBoundsUpstreamConns(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	var accepted atomic.Int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, "ok")
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{e.upCA.serverCert(t, "example.com")}, MinVersion: tls.VersionTLS12}
	ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			accepted.Add(1)
		}
	}
	quietServer(ts)
	ts.StartTLS() // HTTP/1.1 only
	t.Cleanup(ts.Close)
	e.upstream = ts.Listener.Addr().String()
	e.start(ruleWeb+","+ruleAPIRead, "", true)
	client := e.client(true, 443)

	// One guest HTTP/2 connection carries all the streams.
	const streams = 64
	var wg sync.WaitGroup
	for i := range streams {
		wg.Go(func() {
			resp, err := client.Get(fmt.Sprintf("https://example.com/ok/%d", i))
			if err != nil {
				t.Errorf("GET %d: %v", i, err)
				return
			}
			_ = readBody(t, resp)
		})
	}
	wg.Wait()
	if n := accepted.Load(); n > maxUpstreamConns {
		t.Fatalf("upstream accepted %d connections for one flow, want at most %d", n, maxUpstreamConns)
	}
}

func TestClassifyRequestLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want lineClass
	}{
		{"empty", "", lineMore},
		{"get", "GET / HTTP/1.1\r\n", lineHTTP},
		{"extension method", "PROPFIND /dav HTTP/1.1\r\nHost: x\r\n", lineHTTP},
		{"lowercase method", "delete /x HTTP/1.1\r\n", lineHTTP},
		{"bare LF", "GET / HTTP/1.0\n", lineHTTP},
		{"absolute form", "GET http://example.com/ HTTP/1.1\r\n", lineHTTP},
		{"h2 preface", h2Preface, lineHTTP},
		{"method so far", "PROPFI", lineMore},
		{"target so far", "GET /a/b", lineMore},
		{"version so far", "GET / HTTP/1", lineMore},
		{"line end so far", "GET / HTTP/1.1\r", lineMore},
		{"not a line end", "GET / HTTP/1.1 \r\n", lineMalformed},
		{"line without a version", "GET /\r\n", lineMalformed},
		{"wrong version", "GET / HTTP/x.1\r\n", lineMalformed},
		{"greeting", "HELLO\r\n", lineNotHTTP},
		{"two words", "get foo\r\n", lineMalformed},
		{"ssh banner", "SSH-2.0-OpenSSH_9.6\r\n", lineNotHTTP},
		{"binary", "\x00\x01\x02", lineNotHTTP},
		{"leading space", " GET / HTTP/1.1\r\n", lineMalformed},
		{"control in target", "GET /\x01 HTTP/1.1\r\n", lineMalformed},
		{"empty target", "GET  HTTP/1.1\r\n", lineMalformed},
		{"two spaces", "POST  /forbidden HTTP/1.1\r\n", lineMalformed},
		{"two spaces before version", "POST /forbidden  HTTP/1.1\r\n", lineMalformed},
		{"tab separator", "POST\t/forbidden HTTP/1.1\r\n", lineMalformed},
		{"tab before the version", "POST /forbidden\tHTTP/1.1\r\n", lineMalformed},
		{"lowercase method with two spaces", "post  /forbidden HTTP/1.1\r\n", lineMalformed},
		{"trailing whitespace after the version", "POST /forbidden HTTP/1.1  \r\n", lineMalformed},
		{"unknown method with an odd separator", "FOO\t/x HTTP/1.1\r\n", lineMalformed},
		{"unknown method, version at the end", "FOO bar baz HTTP/1.1\r\n", lineMalformed},
		{"lowercase version", "FOO /x\thttp/1.1\n", lineMalformed},
		{"leading space so far", " ", lineMore},
		{"leading space and method so far", "\t PO", lineMore},
		{"leading space and non-method", " HELO x\r\n", lineNotHTTP},
		{"smtp greeting", "HELO x\r\n", lineNotHTTP},
		{"unknown method with a version-less line", "FOO /x\r\n", lineNotHTTP},
		{"extension method with a tab so far", "PROPFIND\t/forbidden HTTP/1.1", lineMore},
		{"extension method and a tab", "PROPFIND\t", lineMore},
		{"extension method with a tab", "PROPFIND\t/forbidden HTTP/1.1\r\n", lineMalformed},
		{"extension method with a vertical tab", "PROPFIND\v/forbidden HTTP/1.1\r\n", lineMalformed},
		{"extension method with a form feed so far", "PROPFIND\f/forbidden", lineMore},
		{"extension method with a tab and no version", "FOO\t/x\r\n", lineNotHTTP},
		{"leading form feed", "\fPOST /forbidden HTTP/1.1\r\n", lineMalformed},
		{"leading vertical tab", "\vPOST /forbidden HTTP/1.1\r\n", lineMalformed},
		{"leading carriage return", "\rPOST /forbidden HTTP/1.1\r\n", lineMalformed},
		{"leading empty line", "\r\nPOST /forbidden HTTP/1.1\r\n", lineMalformed},
		{"leading bare LF", "\nPOST /forbidden HTTP/1.1\r\n", lineMalformed},
		{"leading empty line, extension method with a tab", "\r\nPROPFIND\t/forbidden HTTP/1.1\r\n", lineMalformed},
		{"leading empty lines so far", "\r\n\r\n", lineMore},
		{"leading empty line and a token so far", "\r\nPROPF", lineMore},
		{"leading empty line, ssh banner", "\r\nSSH-2.0-OpenSSH_9.6\r\n", lineNotHTTP},
		{"line-based protocol so far", "SET k v", lineMore},
		{"line-based protocol without its line end", "SET k v\r", lineMore},
		{"line-based protocol", "SET k v\r\n", lineNotHTTP},
		{"token and a carriage return so far", "HELLO\r", lineMore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyRequestLine([]byte(tt.in)); got != tt.want {
				t.Fatalf("classifyRequestLine(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestInspectNoALPNRequestLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
	}{
		{"extension method", "PROPFIND /ok/a HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
		{"lowercase method", "delete /ok/a HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			var hits atomic.Int32
			e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, "ok")
			}))
			e.start(ruleWeb+","+ruleAPIRead, "", true)
			tc := dialNoALPN(t, e)
			if _, err := io.WriteString(tc, tt.raw); err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			if body := readBody(t, resp); resp.StatusCode != 403 || !strings.Contains(body, "http-no-match") {
				t.Fatalf("response = %d %q, want 403 http-no-match from the http rules", resp.StatusCode, body)
			}
			if hits.Load() != 0 {
				t.Fatal("request reached upstream")
			}
			if evs := e.find("inspect-error", map[string]any{"error": "not HTTP"}); len(evs) != 0 {
				t.Fatalf("request was classified as not HTTP: %v", evs)
			}
		})
	}
	t.Run("a first line past the cap fails closed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("upstream reached") }))
		e.start(ruleWeb+","+ruleAPIRead, "", true)
		tc := dialNoALPN(t, e)
		_, _ = io.WriteString(tc, "GET /"+strings.Repeat("a", 2*maxRequestLine))
		if n, err := tc.Read(make([]byte, 16)); err == nil {
			t.Fatalf("read %d bytes, want the flow closed", n)
		}
		e.waitEvent("inspect-error", map[string]any{"reason": "inconclusive-request-line"})
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.dialed) != 0 {
			t.Fatalf("upstream dialed for a closed flow: %v", e.dialed)
		}
	})
}

// malformedLines are request lines a lenient server could still take for a
// request, but which are not valid ones.
var malformedLines = []struct{ name, raw string }{
	{"two spaces", "POST  /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"tab separator", "POST\t/forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"leading space", " POST /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"lowercase method with two spaces", "post  /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"trailing whitespace after the version", "POST /forbidden HTTP/1.1 \r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
}

func TestInspectNoALPNMalformedRequestLine(t *testing.T) {
	t.Parallel()
	for _, tt := range malformedLines {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			e.rawUpstream(true, func(c net.Conn) { t.Error("upstream reached by a malformed request line") })
			e.start(ruleWeb+","+ruleAPIRead, "", true)
			tc := dialNoALPN(t, e)
			if _, err := io.WriteString(tc, tt.raw); err != nil {
				t.Fatal(err)
			}
			if n, err := tc.Read(make([]byte, 16)); err == nil {
				t.Fatalf("read %d bytes, want the flow closed", n)
			}
			e.waitEvent("inspect-error", map[string]any{"reason": "malformed-request-line"})
			e.mu.Lock()
			defer e.mu.Unlock()
			if len(e.dialed) != 0 {
				t.Fatalf("upstream dialed for a closed flow: %v", e.dialed)
			}
		})
	}
	// Other protocols are still decided by the plain rules and relayed.
	for _, tt := range []struct{ name, raw string }{
		{"ssh banner", "SSH-2.0-OpenSSH_9.6\r\n"},
		{"smtp greeting", "HELO x\r\n"},
		{"binary", "\x00\x01\x02\x03\r\n"},
	} {
		t.Run("relays "+tt.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			e.rawUpstream(true, func(c net.Conn) {
				b := make([]byte, 64)
				n, _ := c.Read(b)
				_, _ = c.Write(append([]byte("echo:"), b[:n]...))
			})
			e.start(ruleWeb+","+ruleAPIRead, "", true)
			tc := dialNoALPN(t, e)
			if _, err := io.WriteString(tc, tt.raw); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len("echo:"+tt.raw))
			if _, err := io.ReadFull(tc, got); err != nil || string(got) != "echo:"+tt.raw {
				t.Fatalf("relayed reply = %q, %v", got, err)
			}
		})
	}
}

// writeFragments sends raw in short pieces, so that a classifier sees the
// first line grow. A write error is ignored: the broker may close the flow as
// soon as the line is decided.
func writeFragments(w io.Writer, raw string) {
	for len(raw) > 0 {
		n := min(3, len(raw))
		if _, err := io.WriteString(w, raw[:n]); err != nil {
			return
		}
		raw = raw[n:]
		time.Sleep(3 * time.Millisecond)
	}
}

// fragmentedMalformedLines are request lines that must be denied however the
// guest splits them.
var fragmentedMalformedLines = []struct{ name, raw string }{
	{"extension method with a tab", "PROPFIND\t/forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"extension method with a vertical tab", "PROPFIND\v/forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"leading form feed", "\fPOST /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"leading vertical tab", "\vPOST /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"leading carriage return", "\rPOST /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"leading empty line", "\r\nPOST /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"leading bare LF", "\nPOST /forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
	{"leading empty lines, extension method with a tab", "\r\n\r\nPROPFIND\t/forbidden HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"},
}

// echoFirstLine answers a relayed stream's first line with "echo:" and the line.
func echoFirstLine(c net.Conn) {
	line, _ := bufio.NewReader(c).ReadString('\n')
	_, _ = io.WriteString(c, "echo:"+line)
}

func TestInspectNoALPNFragmentedRequestLine(t *testing.T) {
	t.Parallel()
	for _, tt := range fragmentedMalformedLines {
		t.Run("denied: "+tt.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			e.rawUpstream(true, func(c net.Conn) { t.Error("upstream reached by a fragmented malformed request line") })
			e.start(ruleWeb+","+ruleAPIRead, "", true)
			tc := dialNoALPN(t, e)
			writeFragments(tc, tt.raw)
			if n, err := tc.Read(make([]byte, 16)); err == nil {
				t.Fatalf("read %d bytes, want the flow closed", n)
			}
			e.waitEvent("inspect-error", map[string]any{"reason": "malformed-request-line"})
			e.mu.Lock()
			defer e.mu.Unlock()
			if len(e.dialed) != 0 {
				t.Fatalf("upstream dialed for a closed flow: %v", e.dialed)
			}
		})
	}
	t.Run("a fragmented line-based protocol is relayed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, echoFirstLine)
		e.start(ruleWeb+","+ruleAPIRead, "", true)
		tc := dialNoALPN(t, e)
		writeFragments(tc, "SET k v\r\n")
		const want = "echo:SET k v\r\n"
		got := make([]byte, len(want))
		if _, err := io.ReadFull(tc, got); err != nil || string(got) != want {
			t.Fatalf("relayed reply = %q, %v", got, err)
		}
	})
}

// TestInspectNoALPNRequestInOneRecord checks that a request that arrived whole
// is classified at once instead of waiting out the peek timeout for one more
// byte.
func TestInspectNoALPNRequestInOneRecord(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("upstream reached") }))
	e.start(ruleWeb+","+ruleAPIRead, "", true)
	e.broker.policy.Load().inspect.peekTimeout = 5 * time.Second
	tc := dialNoALPN(t, e)
	begin := time.Now()
	if _, err := io.WriteString(tc, "PROPFIND /ok/a HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if body := readBody(t, resp); resp.StatusCode != 403 || !strings.Contains(body, "http-no-match") {
		t.Fatalf("response = %d %q, want 403 http-no-match from the http rules", resp.StatusCode, body)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("response took %v, want the request classified without waiting for the 5s peek timeout", took)
	}
}

func TestInspectSilentNoALPN(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T, e *l7Env) {
		t.Helper()
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
		e.broker.policy.Load().inspect.peekTimeout = 100 * time.Millisecond
	}
	t.Run("http after the peek timed out is closed unforwarded", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		got := make(chan []byte, 1)
		e.rawUpstream(true, func(c net.Conn) {
			b, _ := io.ReadAll(c)
			got <- b
		})
		start(t, e)
		tc := dialNoALPN(t, e)
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(tc, "POST /ok/a HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n")
		if n, err := tc.Read(make([]byte, 16)); err == nil {
			t.Fatalf("read %d bytes, want the flow closed", n)
		}
		select {
		case b := <-got:
			if len(b) != 0 {
				t.Fatalf("upstream received %q, want nothing", b)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("upstream connection never ended")
		}
		e.waitEvent("inspect-error", map[string]any{"reason": "http-after-silence"})
	})
	for _, tt := range malformedLines {
		t.Run("malformed line after the peek timed out is closed unforwarded: "+tt.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			got := make(chan []byte, 1)
			e.rawUpstream(true, func(c net.Conn) {
				b, _ := io.ReadAll(c)
				got <- b
			})
			start(t, e)
			tc := dialNoALPN(t, e)
			time.Sleep(400 * time.Millisecond)
			_, _ = io.WriteString(tc, tt.raw)
			if n, err := tc.Read(make([]byte, 16)); err == nil {
				t.Fatalf("read %d bytes, want the flow closed", n)
			}
			select {
			case b := <-got:
				if len(b) != 0 {
					t.Fatalf("upstream received %q, want nothing", b)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("upstream connection never ended")
			}
			e.waitEvent("inspect-error", map[string]any{"reason": "malformed-request-line"})
		})
	}
	for _, tt := range fragmentedMalformedLines {
		t.Run("fragmented malformed line after the peek timed out is closed unforwarded: "+tt.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			got := make(chan []byte, 1)
			e.rawUpstream(true, func(c net.Conn) {
				b, _ := io.ReadAll(c)
				got <- b
			})
			start(t, e)
			tc := dialNoALPN(t, e)
			time.Sleep(400 * time.Millisecond)
			writeFragments(tc, tt.raw)
			if n, err := tc.Read(make([]byte, 16)); err == nil {
				t.Fatalf("read %d bytes, want the flow closed", n)
			}
			select {
			case b := <-got:
				if len(b) != 0 {
					t.Fatalf("upstream received %q, want nothing", b)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("upstream connection never ended")
			}
			e.waitEvent("inspect-error", map[string]any{"reason": "malformed-request-line"})
		})
	}
	t.Run("a fragmented line-based protocol after the peek timed out is relayed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, echoFirstLine)
		start(t, e)
		tc := dialNoALPN(t, e)
		time.Sleep(400 * time.Millisecond)
		writeFragments(tc, "SET k v\r\n")
		const want = "echo:SET k v\r\n"
		got := make([]byte, len(want))
		if _, err := io.ReadFull(tc, got); err != nil || string(got) != want {
			t.Fatalf("relayed reply = %q, %v", got, err)
		}
	})
	t.Run("server-first protocol is relayed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, func(c net.Conn) {
			_, _ = io.WriteString(c, "220 ready\r\n")
			b := make([]byte, 64)
			n, _ := c.Read(b)
			_, _ = c.Write(append([]byte("221 "), b[:n]...))
		})
		start(t, e)
		tc := dialNoALPN(t, e)
		br := bufio.NewReader(tc)
		if line, err := br.ReadString('\n'); err != nil || line != "220 ready\r\n" {
			t.Fatalf("greeting = %q, %v", line, err)
		}
		if _, err := io.WriteString(tc, "QUIT\r\n"); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len("221 QUIT\r\n"))
		if _, err := io.ReadFull(br, got); err != nil || string(got) != "221 QUIT\r\n" {
			t.Fatalf("reply = %q, %v", got, err)
		}
	})
	t.Run("a partial first line at the timeout fails closed", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(true, func(c net.Conn) { t.Error("upstream dialed for a closed flow") })
		start(t, e)
		tc := dialNoALPN(t, e)
		_, _ = io.WriteString(tc, "GE")
		if n, err := tc.Read(make([]byte, 16)); err == nil {
			t.Fatalf("read %d bytes, want the flow closed", n)
		}
		e.waitEvent("inspect-error", map[string]any{"reason": "inconclusive-request-line"})
	})
}

func TestInspectH2C(t *testing.T) {
	t.Parallel()
	const rules = ruleWebHost + `,{"name":"other","names":["other.example.net"]},` + ruleAPIRead
	e := newL7Env(t)
	var seen sync.Map
	e.h2cUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Method+" "+r.Host+r.URL.Path, r.ProtoMajor)
		_, _ = io.WriteString(w, "h2c ok")
	}))
	e.start(rules, "", false)
	client := e.h2cClient(80)

	do := func(method, host, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, "http://example.com"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s%s: %v", method, host, path, err)
		}
		return resp.StatusCode, readBody(t, resp)
	}
	if code, body := do("GET", "example.com", "/ok/a"); code != 200 || body != "h2c ok" {
		t.Fatalf("GET = %d %q, want 200 through the h2c flow", code, body)
	}
	if v, ok := seen.Load("GET example.com/ok/a"); !ok || v != 2 {
		t.Fatalf("upstream saw %v (found %v), want HTTP/2 prior knowledge", v, ok)
	}
	denied := []struct {
		name, method, host, path, want string
		code                           int
	}{
		{"method not allowed", "POST", "example.com", "/ok/a", "reason=http-no-match", 403},
		{"authority is not allowed", "GET", "evil.example.org", "/ok/a", "reason=default-deny", 403},
		{"authority does not resolve to the destination", "GET", "other.example.net", "/ok/a", "reason=sni-mismatch", 403},
		{"authority is another address", "GET", "1.2.3.4", "/ok/a", "reason=host-mismatch", 421},
	}
	for _, tt := range denied {
		if code, body := do(tt.method, tt.host, tt.path); code != tt.code || !strings.Contains(body, tt.want) {
			t.Fatalf("%s: %s %s = %d %q, want %d %q", tt.name, tt.method, tt.host, code, body, tt.code, tt.want)
		}
	}
	for _, k := range []string{"POST example.com/ok/a", "GET evil.example.org/ok/a", "GET other.example.net/ok/a", "GET 1.2.3.4/ok/a"} {
		if _, ok := seen.Load(k); ok {
			t.Fatalf("denied request %q reached upstream", k)
		}
	}
	if evs := e.find("l7", map[string]any{"proto": "http", "method": "GET", "host": "example.com", "status": float64(200), "rule": "api-read"}); len(evs) != 1 {
		t.Fatalf("allowed l7 events = %v\n%s", evs, e.log.String())
	}
	if evs := e.find("l7", map[string]any{"method": "GET", "host": "other.example.net", "status": float64(403), "denied": true, "reason": "sni-mismatch"}); len(evs) != 1 {
		t.Fatalf("denied l7 events = %v\n%s", evs, e.log.String())
	}

	t.Run("a request without authority is denied", func(t *testing.T) {
		t.Parallel()
		f := e.bareFlow("", true)
		t.Cleanup(f.close)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/ok/a", nil)
		req.Host = ""
		f.ServeHTTP(rec, req)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "reason=sni-required") {
			t.Fatalf("response = %d %q, want 403 sni-required", rec.Code, rec.Body.String())
		}
	})
}

func TestInspectTracksLiveUpstreamOnly(t *testing.T) {
	t.Parallel()
	e := newL7Env(t)
	e.httpUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		_, _ = io.WriteString(w, "ok")
	}))
	e.start(ruleWeb, "", false)
	f := e.bareFlow("example.com", false)
	t.Cleanup(f.close)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	const requests = 40
	for range requests {
		req, err := http.NewRequest("GET", srv.URL+"/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = readBody(t, resp)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.trackedUpstream() > 2 {
		if time.Now().After(deadline) {
			t.Fatalf("flow tracks %d upstream connections after %d requests that each closed theirs", f.trackedUpstream(), requests)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestInspectWithoutName(t *testing.T) {
	t.Parallel()
	ipHello := func(t *testing.T) []byte { return clientHelloBytes(t, l7IP, nil) }
	t.Run("plain CIDR allow is not broken by an unrelated name http rule", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		hello := ipHello(t)
		got := make(chan []byte, 1)
		e.rawUpstream(false, func(c net.Conn) {
			b := make([]byte, len(hello))
			_, _ = io.ReadFull(c, b)
			got <- b
			_, _ = c.Write([]byte("upstream-ok"))
		})
		e.start(`{"name":"net","cidrs":["93.184.216.0/24"]},{"name":"gh","names":["api.github.com"],"http":{"methods":["GET"]}}`, "", true)
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(hello); err != nil {
			t.Fatal(err)
		}
		reply, err := io.ReadAll(c)
		if err != nil || string(reply) != "upstream-ok" {
			t.Fatalf("reply = %x, %v; want the flow spliced to upstream", reply, err)
		}
		if !bytes.Equal(<-got, hello) {
			t.Fatal("upstream did not receive the ClientHello unchanged")
		}
	})
	t.Run("non-HTTP ALPN without a name is inspected under a plain CIDR allow and an http rule", func(t *testing.T) {
		t.Parallel()
		for _, alpn := range []string{"imap", "xmpp-client"} {
			t.Run(alpn, func(t *testing.T) {
				t.Parallel()
				e := newL7Env(t)
				e.rawUpstream(false, func(c net.Conn) { t.Error("upstream dialed for an inspected flow without a request") })
				e.start(`{"name":"net","cidrs":["93.184.216.0/24"]},{"name":"ip-read","cidrs":["`+l7IP+`"],"http":{"methods":["GET"]}}`, "", true)
				c, err := e.dialGuest(443)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Write(clientHelloBytes(t, l7IP, []string{alpn})); err != nil {
					t.Fatal(err)
				}
				// The broker answers the ClientHello itself (a TLS handshake record).
				first := make([]byte, 1)
				if _, err := io.ReadFull(c, first); err != nil || first[0] != 0x16 {
					t.Fatalf("first byte = %x, %v; want the broker's ServerHello", first, err)
				}
				if evs := e.find("flow", map[string]any{"reason": "sni-required"}); len(evs) != 0 {
					t.Fatalf("flow denied: %v", evs)
				}
			})
		}
	})
	t.Run("HTTP inside a non-HTTP ALPN without a name is checked by the http rules", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		var posts, gets atomic.Int32
		ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts.Add(1)
			} else {
				gets.Add(1)
			}
			_, _ = io.WriteString(w, "ip ok")
		}))
		ts.TLS = &tls.Config{Certificates: []tls.Certificate{e.upCA.serverCertIPs(t, l7IP)}, MinVersion: tls.VersionTLS12, NextProtos: []string{}}
		quietServer(ts)
		ts.StartTLS()
		t.Cleanup(ts.Close)
		e.upstream = ts.Listener.Addr().String()
		e.start(`{"name":"net","cidrs":["93.184.216.0/24"]},{"name":"ip-read","cidrs":["`+l7IP+`"],"http":{"methods":["GET"]}}`, "", true)
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		tc := tls.Client(c, &tls.Config{ServerName: l7IP, RootCAs: e.ca.pool, NextProtos: []string{"imap"}, MinVersion: tls.VersionTLS12})
		if err := tc.Handshake(); err != nil {
			t.Fatalf("handshake: %v", err)
		}
		if p := tc.ConnectionState().NegotiatedProtocol; p != "imap" {
			t.Fatalf("negotiated ALPN %q, want imap", p)
		}
		br := bufio.NewReader(tc)
		do := func(method string) (int, string) {
			t.Helper()
			if _, err := io.WriteString(tc, method+" /a HTTP/1.1\r\nHost: "+l7IP+"\r\nContent-Length: 0\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			return resp.StatusCode, readBody(t, resp)
		}
		if code, body := do("POST"); code != 403 || !strings.Contains(body, "http-no-match") {
			t.Fatalf("POST = %d %q, want 403 http-no-match", code, body)
		}
		if code, body := do("GET"); code != 200 || body != "ip ok" {
			t.Fatalf("GET = %d %q, want 200", code, body)
		}
		if posts.Load() != 0 || gets.Load() != 1 {
			t.Fatalf("upstream saw %d POST and %d GET, want 0 and 1", posts.Load(), gets.Load())
		}
	})
	t.Run("non-HTTP ALPN without a name passes under a plain CIDR allow when no http rule covers the address", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		hello := clientHelloBytes(t, l7IP, []string{"imap"})
		got := make(chan []byte, 1)
		e.rawUpstream(false, func(c net.Conn) {
			b := make([]byte, len(hello))
			_, _ = io.ReadFull(c, b)
			got <- b
			_, _ = c.Write([]byte("upstream-ok"))
		})
		e.start(`{"name":"net","cidrs":["93.184.216.0/24"]},{"name":"gh","names":["api.github.com"],"http":{"methods":["GET"]}}`, "", true)
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(hello); err != nil {
			t.Fatal(err)
		}
		reply, err := io.ReadAll(c)
		if err != nil || string(reply) != "upstream-ok" {
			t.Fatalf("reply = %x, %v; want the flow spliced to upstream", reply, err)
		}
		if !bytes.Equal(<-got, hello) {
			t.Fatal("upstream did not receive the ClientHello unchanged")
		}
	})
	t.Run("HTTP-capable ALPN without a name is still inspected", func(t *testing.T) {
		t.Parallel()
		for name, alpn := range map[string][]string{"no ALPN": nil, "h2": {"h2"}, "http/1.1 and imap": {"imap", "http/1.1"}} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				e := newL7Env(t)
				e.rawUpstream(false, func(c net.Conn) { t.Error("upstream dialed for an inspected flow without a request") })
				e.start(`{"name":"net","cidrs":["93.184.216.0/24"]},{"name":"ip-read","cidrs":["`+l7IP+`"],"http":{"methods":["GET"]}}`, "", true)
				c, err := e.dialGuest(443)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Write(clientHelloBytes(t, l7IP, alpn)); err != nil {
					t.Fatal(err)
				}
				// The broker answers the ClientHello itself (a TLS handshake record).
				first := make([]byte, 1)
				if _, err := io.ReadFull(c, first); err != nil || first[0] != 0x16 {
					t.Fatalf("first byte = %x, %v; want the broker's ServerHello", first, err)
				}
			})
		}
	})
	t.Run("name rules still require a name", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		e.rawUpstream(false, func(c net.Conn) { t.Error("upstream dialed for a flow without a name") })
		e.start(ruleWebHost+","+`{"name":"gh","names":["api.github.com"],"http":{"methods":["GET"]}}`, "", true)
		c, err := e.dialGuest(443)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Write(ipHello(t))
		reply, _ := io.ReadAll(c)
		if len(reply) == 0 || reply[0] != 0x15 {
			t.Fatalf("reply = %x, want an access_denied alert", reply)
		}
		e.waitEvent("flow", map[string]any{"reason": "sni-required"})
	})
	t.Run("address-scoped http rule inspects TLS under the IP", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		var seen sync.Map
		ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.Store(r.Method+" "+r.URL.Path, true)
			_, _ = io.WriteString(w, "ip ok")
		}))
		ts.TLS = &tls.Config{Certificates: []tls.Certificate{e.upCA.serverCertIPs(t, l7IP)}, MinVersion: tls.VersionTLS12}
		quietServer(ts)
		ts.StartTLS()
		t.Cleanup(ts.Close)
		e.upstream = ts.Listener.Addr().String()
		e.start(`{"name":"ip-read","cidrs":["`+l7IP+`"],"http":{"methods":["GET"]}}`, "", true)
		client := e.client(false, 443)

		resp, err := client.Get("https://" + l7IP + "/a")
		if err != nil {
			t.Fatal(err)
		}
		leaf := resp.TLS.PeerCertificates[0]
		if leaf.Issuer.CommonName != e.ca.cert.Subject.CommonName || len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != l7IP {
			t.Fatalf("guest saw issuer=%q ips=%v, want an IP leaf from the inspection CA", leaf.Issuer.CommonName, leaf.IPAddresses)
		}
		if body := readBody(t, resp); resp.StatusCode != 200 || body != "ip ok" {
			t.Fatalf("GET = %d %q", resp.StatusCode, body)
		}
		resp, err = client.Post("https://"+l7IP+"/a", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		if body := readBody(t, resp); resp.StatusCode != 403 || !strings.Contains(body, "http-no-match") {
			t.Fatalf("POST = %d %q, want 403 http-no-match", resp.StatusCode, body)
		}
		if _, ok := seen.Load("POST /a"); ok {
			t.Fatal("denied POST reached upstream")
		}
		// The request host must be the IP literal.
		req, err := http.NewRequest("GET", "https://"+l7IP+"/a", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "example.com"
		resp, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if body := readBody(t, resp); resp.StatusCode != 421 || !strings.Contains(body, "host-mismatch") {
			t.Fatalf("GET with another Host = %d %q, want 421 host-mismatch", resp.StatusCode, body)
		}
	})
	t.Run("cleartext HTTP/1.0 without Host is evaluated under the IP", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		var seen sync.Map
		e.httpUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen.Store(r.Method, true)
			_, _ = io.WriteString(w, "ip ok")
		}))
		e.start(`{"name":"ip-read","cidrs":["`+l7IP+`"],"http":{"methods":["GET"]}}`, "", false)
		do := func(method string) (int, string) {
			t.Helper()
			c, err := e.dialGuest(80)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(c, method+" / HTTP/1.0\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			return resp.StatusCode, readBody(t, resp)
		}
		if code, body := do("GET"); code != 200 || body != "ip ok" {
			t.Fatalf("GET = %d %q, want 200", code, body)
		}
		if code, body := do("POST"); code != 403 || !strings.Contains(body, "http-no-match") {
			t.Fatalf("POST = %d %q, want 403 http-no-match", code, body)
		}
		if _, ok := seen.Load("POST"); ok {
			t.Fatal("denied POST reached upstream")
		}
	})
}
