package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	hostA = "foo.ws-one.jailoc.local"
	hostB = "bar.ws-two.jailoc.local"
)

func testCertificate(t *testing.T, hosts ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: hosts,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

type testServer struct {
	g      *Server
	codes  map[string]string
	addr   string
	pool   *x509.CertPool
	cancel context.CancelFunc
	done   chan error
}

func startServer(t *testing.T, handler http.Handler, setup func(*Server)) *testServer {
	t.Helper()
	up := httptest.NewServer(handler)
	t.Cleanup(up.Close)
	cert, pool := testCertificate(t, hostA, hostB)
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", up.Listener.Addr().String())
	}
	g, codes, err := New(Config{Cert: cert, Routes: []Route{{Host: hostA, Dial: dial, Auth: true}, {Host: hostB, Dial: dial, Auth: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(g)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	x := &testServer{g: g, codes: codes, addr: ln.Addr().String(), pool: pool, cancel: cancel, done: make(chan error, 1)}
	go func() { x.done <- g.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-x.done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	return x
}

func (x *testServer) client(t *testing.T, sni string) *http.Client {
	t.Helper()
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: x.pool, ServerName: sni, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", x.addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func request(t *testing.T, client *http.Client, req *http.Request, status int) *http.Response {
	t.Helper()
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() }) // the body is already drained or abandoned by the test
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d", req.Method, req.URL.Path, res.StatusCode, status)
	}
	return res
}

func newRequest(t *testing.T, method, host, path string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, "https://"+host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func login(t *testing.T, x *testServer, host, code string, status int) *http.Response {
	t.Helper()
	r := newRequest(t, "POST", host, "/.jailoc/login")
	r.Body = http.NoBody
	form := url.Values{"code": []string{code}}.Encode()
	r, err := http.NewRequest("POST", r.URL.String(), strings.NewReader(form))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://"+host)
	return request(t, x.client(t, host), r, status)
}

func loginCookie(t *testing.T, x *testServer, host string) *http.Cookie {
	t.Helper()
	return loginCookieAt(t, x, host, "/")
}

func loginCookieAt(t *testing.T, x *testServer, host, location string) *http.Cookie {
	t.Helper()
	r := login(t, x, host, x.codes[host], 303)
	if r.Header.Get("Location") != location {
		t.Fatalf("login location: %q", r.Header.Get("Location"))
	}
	cs := r.Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies: %v", cs)
	}
	c := cs[0]
	if c.Name != "__Host-jailoc" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 8*60*60 {
		t.Fatalf("cookie attributes: %+v", c)
	}
	return c
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	cert, _ := testCertificate(t, hostA, hostB)
	dial := func(context.Context) (net.Conn, error) { return nil, net.ErrClosed }
	tests := []struct {
		name  string
		cfg   Config
		valid bool
	}{
		{name: "valid auth", cfg: Config{Cert: cert, Routes: []Route{{Host: hostA, Dial: dial, Auth: true}}}, valid: true},
		{name: "valid unauth", cfg: Config{Cert: cert, Routes: []Route{{Host: hostA, Dial: dial, Auth: false}}}, valid: true},
		{name: "no routes", cfg: Config{Cert: cert}},
		{name: "no cert", cfg: Config{Routes: []Route{{Host: hostA, Dial: dial}}}},
		{name: "nil dial", cfg: Config{Cert: cert, Routes: []Route{{Host: hostA}}}},
		{name: "duplicate", cfg: Config{Cert: cert, Routes: []Route{{Host: hostA, Dial: dial}, {Host: hostA, Dial: dial}}}},
		{name: "start path", cfg: Config{Cert: cert, Routes: []Route{{Host: hostA, Dial: dial, StartPath: "/vnc.html?autoconnect=1", Auth: true}}}, valid: true},
		{name: "cert mismatch", cfg: Config{Cert: cert, Routes: []Route{{Host: "other.ws.jailoc.local", Dial: dial}}}},
	}
	for _, host := range []string{"FOO.ws.jailoc.local", "127.0.0.1", "[::1]", "foo:443", "foo.", "*.ws.jailoc.local", "-bad.ws.jailoc.local", "bad..ws.jailoc.local", "bad_name.ws.jailoc.local", ""} {
		tests = append(tests, struct {
			name  string
			cfg   Config
			valid bool
		}{name: "host " + host, cfg: Config{Cert: cert, Routes: []Route{{Host: host, Dial: dial}}}})
	}
	for _, path := range []string{
		"vnc.html", "//evil.example/", "/\\evil.example", "https://evil.example/", "http:/evil", "/", "/?a=1", "/#x",
		"/vnc.html#frag", "/.jailoc/login", "/.jailoc", "/a\nb", "/a b", "/a\\b", "/%2f%2fevil", "/../x", "/a/./b", "/a/../b",
		"/vnc.html?a=\r", "/vnc.html?a=%zz", "/%zz", "/a%00b", "/%5cevil", "/%2e%2e/x", "/" + strings.Repeat("a", 2048),
	} {
		tests = append(tests, struct {
			name  string
			cfg   Config
			valid bool
		}{name: "start path " + path, cfg: Config{Cert: cert, Routes: []Route{{Host: hostA, Dial: dial, StartPath: path}}}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g, codes, err := New(tt.cfg)
			if (err == nil) != tt.valid {
				t.Fatalf("New: %v, valid %v", err, tt.valid)
			}
			if tt.valid {
				if g == nil {
					t.Fatal("missing gateway")
				}
				if tt.cfg.Routes[0].Auth && len(codes[hostA]) < 32 {
					t.Fatal("missing random code for authenticated route")
				}
				if !tt.cfg.Routes[0].Auth && codes[hostA] != "" {
					t.Fatalf("unexpected code %q for unauthenticated route", codes[hostA])
				}
			}
		})
	}
}

func TestTLSAndHostBoundary(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.NotFoundHandler(), nil)
	t.Run("TLS 1.1 rejected", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", x.addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }() // the rejected handshake may already have closed the connection
		if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		c := tls.Client(conn, &tls.Config{RootCAs: x.pool, ServerName: hostA, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}) //nolint:gosec // Deliberately obsolete client tests the server's TLS floor.
		if err := c.Handshake(); err == nil {
			t.Fatal("obsolete TLS accepted")
		}
	})
	for _, sni := range []string{"unknown.ws.jailoc.local", ""} {
		t.Run("SNI "+sni, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", x.addr, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }() // the rejected handshake may already have closed the connection
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			// Server rejection is under test, not client hostname verification.
			c := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: sni, MinVersion: tls.VersionTLS12}) //nolint:gosec
			if err := c.Handshake(); err == nil {
				t.Fatal("unknown SNI accepted")
			}
		})
	}
	for _, tt := range []struct {
		name, host string
		status     int
	}{
		{name: "valid", host: hostA, status: 200},
		{name: "443", host: hostA + ":443", status: 200},
		{name: "8443", host: hostA + ":8443", status: 400},
		{name: "IP", host: "127.0.0.1", status: 400},
		{name: "malformed", host: hostA + ":", status: 400},
		{name: "sibling", host: hostB, status: 421},
		{name: "unknown", host: "unknown.ws.jailoc.local", status: 421},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRequest(t, "GET", hostA, "/.jailoc/login")
			r.Host = tt.host
			res := request(t, x.client(t, hostA), r, tt.status)
			if res.TLS.Version < tls.VersionTLS12 || res.ProtoMajor != 1 {
				t.Fatal("TLS floor or HTTP/1.1 violated")
			}
		})
	}
}

func TestServeRequiresLoopback(t *testing.T) {
	t.Parallel()
	cert, _ := testCertificate(t, hostA)
	g, _, err := New(Config{Cert: cert, Routes: []Route{{Host: hostA, Dial: func(context.Context) (net.Conn, error) { return nil, net.ErrClosed }}}})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() }) // Serve rejects the listener without closing it
	// Serve only inspects the listener address, so a loopback socket that
	// reports the wildcard address stands in for a real all-interfaces bind.
	ln := wildcardListener{Listener: inner}
	if err := g.Serve(t.Context(), ln); err == nil {
		t.Fatal("wildcard listener accepted")
	}
}

// wildcardListener reports the IPv4 wildcard address for a loopback socket.
type wildcardListener struct{ net.Listener }

func (wildcardListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }

func TestServerUpdatePreservesSessionsAndRevokesRemovedRoutes(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), nil)
	cookieA := loginCookie(t, x, hostA)
	cookieB := loginCookie(t, x, hostB)

	dial := x.g.routes[hostA].Dial
	codes, err := x.g.Update(Config{
		Cert:   x.g.cert,
		Routes: []Route{{Host: hostA, Dial: dial, Auth: true}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, ok := codes[hostA]; ok {
		t.Fatalf("existing route %s received a new login code", hostA)
	}

	// Existing session on hostA stays valid without re-login.
	r := newRequest(t, "GET", hostA, "/")
	r.AddCookie(cookieA)
	request(t, x.client(t, hostA), r, 200)

	// Removed route hostB is rejected at TLS SNI / routing and its session is gone.
	if _, err := x.client(t, hostB).Do(newRequest(t, "GET", hostB, "/")); err == nil {
		t.Fatal("removed route still served")
	}

	// Re-adding hostB mints a new code and requires a fresh login; old cookieB is no longer valid.
	codes, err = x.g.Update(Config{
		Cert:   x.g.cert,
		Routes: []Route{{Host: hostA, Dial: dial, Auth: true}, {Host: hostB, Dial: dial, Auth: true}},
	})
	if err != nil {
		t.Fatalf("Update re-add: %v", err)
	}
	if codes[hostB] == "" {
		t.Fatalf("re-added route %s missing login code", hostB)
	}
	r = newRequest(t, "GET", hostB, "/")
	r.AddCookie(cookieB)
	request(t, x.client(t, hostB), r, 303)
}

func TestUnauthenticatedSessionRejectsRouteChangedByUpdate(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), nil)
	dial := x.g.routes[hostA].Dial
	if _, err := x.g.Update(Config{Cert: x.g.cert, Routes: []Route{{Host: hostA, Dial: dial}}}); err != nil {
		t.Fatalf("Update to unauthenticated: %v", err)
	}
	request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/"), 200)

	// A request that looked up the unauthenticated route before Update
	// enabled authentication must not get a session that escaped revocation.
	stale, _ := x.g.route(hostA)
	if _, err := x.g.Update(Config{Cert: x.g.cert, Routes: []Route{{Host: hostA, Dial: dial, Auth: true}}}); err != nil {
		t.Fatalf("Update to authenticated: %v", err)
	}
	if s, current := x.g.unauthenticatedSession(hostA, stale); current || s != nil {
		t.Fatalf("unauthenticatedSession for a stale route = (%v, %v), want (nil, false)", s, current)
	}
	x.g.mu.Lock()
	leaked := x.g.unauth[hostA]
	x.g.mu.Unlock()
	if leaked != nil {
		t.Fatal("stale lookup registered an unauthenticated session")
	}
	request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/"), 303)
}
