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
	"strings"
	"sync"
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
	e.start(ruleWeb+`,{"name":"all","names":["example.com"],"http":{}}`, "", true)

	conn, err := e.dialGuest(443)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	tc := tls.Client(conn, &tls.Config{ServerName: "example.com", RootCAs: e.ca.pool, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	br := bufio.NewReader(tc)
	roundTrip := func(raw string) (int, string) {
		t.Helper()
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
		{"encoded slash", "GET /a%2Fb HTTP/1.1\r\nHost: example.com\r\n\r\n", 403, "reason=bad-path"},
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
	for _, uri := range []string{"//etc", "/a%2Fb", "/%2e%2e/etc", "/a%00", "/a\\b"} {
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
	t.Run("allowed by a plain rule is passed through undecrypted", func(t *testing.T) {
		t.Parallel()
		e := newL7Env(t)
		got := make(chan []byte, 1)
		e.rawUpstream(false, func(c net.Conn) {
			b := make([]byte, len(hello))
			_, _ = io.ReadFull(c, b)
			got <- b
			_, _ = c.Write([]byte("upstream-ok"))
		})
		e.start(ruleWebHost+","+ruleAPIRead, "", true)
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

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
