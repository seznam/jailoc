package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startRedirect serves g's HTTP redirect on a loopback listener and returns its address.
func startRedirect(t *testing.T, g *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- g.ServeHTTPRedirect(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ServeHTTPRedirect: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("ServeHTTPRedirect did not stop")
		}
	})
	return ln.Addr().String()
}

func redirectGet(t *testing.T, addr, method, host, target string, header http.Header) *http.Response {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() }) // the test is over with the connection
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(method + " " + target + " HTTP/1.1\r\nHost: " + host + "\r\nConnection: close\r\n")
	for k, v := range header {
		b.WriteString(k + ": " + v[0] + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() }) // the body is abandoned
	return res
}

func TestHTTPRedirect(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name              string
		httpPort, tlsPort int
		method, host      string
		target            string
		header            http.Header
		status            int
		location          string
	}{
		{name: "default root", method: "GET", host: hostA, target: "/", status: 307, location: "https://" + hostA + "/"},
		{name: "default explicit port", method: "GET", host: hostA + ":80", target: "/", status: 307, location: "https://" + hostA + "/"},
		{name: "path and query stay encoded", method: "GET", host: hostA, target: "/a%2Fb/%7e?x=%2F&y=1&y=2", status: 307, location: "https://" + hostA + "/a%2Fb/%7e?x=%2F&y=1&y=2"},
		{name: "empty query kept", method: "GET", host: hostA, target: "/p?", status: 307, location: "https://" + hostA + "/p?"},
		{name: "HEAD", method: "HEAD", host: hostA, target: "/x", status: 307, location: "https://" + hostA + "/x"},
		{name: "double slash path is not an authority", method: "GET", host: hostA, target: "//example.com/x", status: 307, location: "https://" + hostA + "//example.com/x"},
		{name: "absolute form uses allowlisted host", method: "GET", host: hostA, target: "http://" + hostA + "/x", status: 307, location: "https://" + hostA + "/x"},
		{name: "absolute form foreign host", method: "GET", host: hostA, target: "http://example.com/x", status: 421},
		{name: "custom ports", httpPort: 8080, tlsPort: 8443, method: "GET", host: hostA + ":8080", target: "/x?y=1", status: 307, location: "https://" + hostA + ":8443/x?y=1"},
		{name: "custom HTTP port requires it", httpPort: 8080, tlsPort: 8443, method: "GET", host: hostA, target: "/", status: 400},
		{name: "custom HTTP port wrong port", httpPort: 8080, tlsPort: 8443, method: "GET", host: hostA + ":80", target: "/", status: 400},
		{name: "HTTPS port as HTTP authority", httpPort: 8080, tlsPort: 8443, method: "GET", host: hostA + ":8443", target: "/", status: 400},
		{name: "default HTTP port required", httpPort: 0, tlsPort: 8443, method: "GET", host: hostA + ":8080", target: "/", status: 400},
		{name: "default HTTPS port is portless", httpPort: 8080, method: "GET", host: hostA + ":8080", target: "/", status: 307, location: "https://" + hostA + "/"},
		{name: "unknown host", method: "GET", host: "unknown.ws.jailoc.local", target: "/", status: 421},
		{name: "disabled route", method: "GET", host: hostB, target: "/", status: 421},
		{name: "IP host", method: "GET", host: "127.0.0.1", target: "/", status: 400},
		{name: "malformed port", method: "GET", host: hostA + ":", target: "/", status: 400},
		{name: "uppercase host", method: "GET", host: strings.ToUpper(hostA), target: "/", status: 400},
		{name: "POST", method: "POST", host: hostA, target: "/.jailoc/login", status: 405},
		{name: "OPTIONS", method: "OPTIONS", host: hostA, target: "/", status: 405},
		{name: "asterisk target", method: "GET", host: hostA, target: "*", status: 400},
		{name: "upgrade", method: "GET", host: hostA, target: "/", header: http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}}, status: 400},
		{name: "backslash query", method: "GET", host: hostA, target: "/?a=\\b", status: 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := startServerWithPorts(t, http.NotFoundHandler(), tt.httpPort, tt.tlsPort, nil)
			routes := x.g.routes
			disabled := routes[hostB]
			disabled.DisableRedirect = true
			cfg := Config{HTTPPort: tt.httpPort, HTTPSPort: tt.tlsPort, Routes: []Route{routes[hostA], disabled}, Cert: x.g.cert}
			if _, err := x.g.Update(cfg); err != nil {
				t.Fatal(err)
			}
			res := redirectGet(t, startRedirect(t, x.g), tt.method, tt.host, tt.target, tt.header)
			if res.StatusCode != tt.status {
				t.Fatalf("status %d, want %d", res.StatusCode, tt.status)
			}
			if got := res.Header.Get("Location"); got != tt.location {
				t.Fatalf("Location %q, want %q", got, tt.location)
			}
			if res.Header.Get("Set-Cookie") != "" {
				t.Fatal("redirect set a cookie")
			}
			if tt.status == 307 && res.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("redirect is cacheable")
			}
		})
	}
}

func TestServeHTTPRedirectRequiresLoopbackAndServesOnce(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.NotFoundHandler(), nil)
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() }) // rejected without closing
	if err := x.g.ServeHTTPRedirect(t.Context(), wildcardListener{Listener: inner}); err == nil {
		t.Fatal("wildcard listener accepted")
	}
	// A served request proves the first call owns the redirect before the second starts.
	redirectGet(t, startRedirect(t, x.g), "GET", hostA, "/", nil)
	second, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() }) // rejected without closing
	if err := x.g.ServeHTTPRedirect(t.Context(), second); err == nil {
		t.Fatal("second redirect serve accepted")
	}
}

func TestRedirectDoesNotBypassTLSChecks(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.NotFoundHandler(), nil)
	// The HTTP listener answers redirects only: a login path is redirected, never served.
	res := redirectGet(t, startRedirect(t, x.g), "GET", hostA, "/.jailoc/login", nil)
	if res.StatusCode != 307 || res.Header.Get("Location") != "https://"+hostA+"/.jailoc/login" {
		t.Fatalf("got %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	// The HTTPS listener still refuses the sibling SNI after the redirect.
	r := newRequest(t, "GET", hostA, "/.jailoc/login")
	r.Host = hostB
	request(t, x.client(t, hostA), r, 421)
}

func TestConfiguredHTTPSPort(t *testing.T) {
	t.Parallel()
	x := startServerWithPorts(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), 8080, 8443, nil)
	cookie := loginCookie(t, x, hostA)
	for _, tt := range []struct {
		name, method, host, origin, referer string
		status                              int
	}{
		{name: "port authority", method: "GET", host: hostA + ":8443", status: 200},
		{name: "portless authority", method: "GET", host: hostA, status: 400},
		{name: "default port authority", method: "GET", host: hostA + ":443", status: 400},
		{name: "HTTP port authority", method: "GET", host: hostA + ":8080", status: 400},
		{name: "referer with port", method: "GET", host: hostA + ":8443", referer: "https://" + hostA + ":8443/p", status: 200},
		{name: "portless referer", method: "GET", host: hostA + ":8443", referer: "https://" + hostA + "/p", status: 403},
		{name: "http referer", method: "GET", host: hostA + ":8443", referer: "http://" + hostA + ":8080/p", status: 403},
		{name: "origin with port", method: "POST", host: hostA + ":8443", origin: "https://" + hostA + ":8443", status: 200},
		{name: "portless origin", method: "POST", host: hostA + ":8443", origin: "https://" + hostA, status: 403},
		{name: "default port origin", method: "POST", host: hostA + ":8443", origin: "https://" + hostA + ":443", status: 403},
		{name: "sibling origin", method: "POST", host: hostA + ":8443", origin: "https://" + hostB + ":8443", status: 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRequest(t, tt.method, hostA, "/")
			r.Host = tt.host
			r.AddCookie(cookie)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				r.Header.Set("Referer", tt.referer)
			}
			request(t, x.client(t, hostA), r, tt.status)
		})
	}
	t.Run("websocket needs exact origin", func(t *testing.T) {
		for origin, status := range map[string]int{"": 403, "https://" + hostA: 403, "https://" + hostA + ":8443": 200} {
			r := newRequest(t, "GET", hostA, "/")
			r.Host = hostA + ":8443"
			r.AddCookie(cookie)
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Connection", "Upgrade")
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
			res, err := x.client(t, hostA).Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close() // only the status is under test
			if (status == 403) != (res.StatusCode == 403) {
				t.Fatalf("origin %q: status %d, want 403=%v", origin, res.StatusCode, status == 403)
			}
		}
	})
}

func TestUpdateKeepsEffectivePorts(t *testing.T) {
	t.Parallel()
	cert, _ := testCertificate(t, hostA)
	dial := func(context.Context) (net.Conn, error) { return nil, net.ErrClosed }
	routes := []Route{{Host: hostA, Dial: dial}}
	for _, tt := range []struct {
		name         string
		first, next  [2]int
		wantNewError bool
		wantUpdate   bool
	}{
		{name: "defaults repeated", first: [2]int{0, 0}, next: [2]int{80, 443}, wantUpdate: true},
		{name: "custom repeated", first: [2]int{8080, 8443}, next: [2]int{8080, 8443}, wantUpdate: true},
		{name: "custom to default", first: [2]int{8080, 8443}, next: [2]int{0, 0}},
		{name: "HTTPS changed", first: [2]int{0, 8443}, next: [2]int{0, 9443}},
		{name: "HTTP changed", first: [2]int{8080, 0}, next: [2]int{8081, 0}},
		{name: "out of range", first: [2]int{0, 70000}, wantNewError: true},
		{name: "negative", first: [2]int{-1, 0}, wantNewError: true},
		{name: "equal", first: [2]int{8443, 8443}, wantNewError: true},
		{name: "equal through defaults", first: [2]int{443, 0}, wantNewError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g, _, err := New(Config{Cert: cert, Routes: routes, HTTPPort: tt.first[0], HTTPSPort: tt.first[1]})
			if (err != nil) != tt.wantNewError {
				t.Fatalf("New error %v, want error %v", err, tt.wantNewError)
			}
			if tt.wantNewError {
				return
			}
			_, err = g.Update(Config{Cert: cert, Routes: routes, HTTPPort: tt.next[0], HTTPSPort: tt.next[1]})
			if (err == nil) != tt.wantUpdate {
				t.Fatalf("Update error %v, want success %v", err, tt.wantUpdate)
			}
		})
	}
}
