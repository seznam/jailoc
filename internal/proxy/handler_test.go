package proxy

import (
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginIsRouteBoundAndSingleUse(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), nil)
	if x.codes[hostA] == x.codes[hostB] {
		t.Fatal("routes share a code")
	}
	login(t, x, hostB, x.codes[hostA], 401)
	login(t, x, hostA, "wrong", 401)
	page := request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/.jailoc/login"), 200)
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `name="code"`) || strings.Contains(string(body), x.codes[hostA]) || page.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("login form missing or leaks code")
	}
	cookie := loginCookie(t, x, hostA)
	login(t, x, hostA, x.codes[hostA], 401)
	r := newRequest(t, "GET", hostB, "/asset.js")
	r.AddCookie(cookie)
	request(t, x.client(t, hostB), r, 303)
	r = newRequest(t, "GET", hostA, "/asset.js")
	request(t, x.client(t, hostA), r, 303)
	r.AddCookie(cookie)
	request(t, x.client(t, hostA), r, 200)
	r = newRequest(t, "POST", hostA, "/.jailoc/logout")
	r.AddCookie(cookie)
	request(t, x.client(t, hostA), r, 403)
	r.Header.Set("Origin", "https://"+hostA)
	res := request(t, x.client(t, hostA), r, 303)
	if len(res.Cookies()) != 1 || res.Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not expire cookie")
	}
	r = newRequest(t, "GET", hostA, "/asset.js")
	r.AddCookie(cookie)
	request(t, x.client(t, hostA), r, 303)
}

func TestStartPathLanding(t *testing.T) {
	t.Parallel()
	const start = "/vnc.html?autoconnect=1"
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream "+html.EscapeString(r.URL.RequestURI())) // the test paths contain no escapable characters
	}), func(g *Server) {
		route := g.routes[hostA]
		route.StartPath = start
		g.routes[hostA] = route
	})
	// Unauthenticated root still goes to login, never to the start path.
	res := request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/"), 303)
	if got := res.Header.Get("Location"); got != "/.jailoc/login" {
		t.Fatalf("unauthenticated root: Location %q", got)
	}
	cookie := loginCookieAt(t, x, hostA, start)
	for _, method := range []string{"GET", "HEAD"} {
		r := newRequest(t, method, hostA, "/")
		r.AddCookie(cookie)
		res = request(t, x.client(t, hostA), r, 303)
		if got := res.Header.Get("Location"); got != start {
			t.Fatalf("%s root: Location %q, want %q", method, got, start)
		}
	}
	// The landing page and other paths reach the upstream unchanged.
	for _, path := range []string{"/vnc.html?autoconnect=1", "/app/ui.js", "/websockify"} {
		r := newRequest(t, "GET", hostA, path)
		r.AddCookie(cookie)
		res = request(t, x.client(t, hostA), r, 200)
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "upstream "+path {
			t.Fatalf("%s: body %q", path, body)
		}
	}
	// Root with a query is not rewritten; it stays an upstream request.
	r := newRequest(t, "GET", hostA, "/?x=1")
	r.AddCookie(cookie)
	request(t, x.client(t, hostA), r, 200)
	// A POST to the root is never redirected.
	r = newRequest(t, "POST", hostA, "/")
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://"+hostA)
	request(t, x.client(t, hostA), r, 200)
	// Another route keeps the generic root.
	cookieB := loginCookie(t, x, hostB)
	r = newRequest(t, "GET", hostB, "/")
	r.AddCookie(cookieB)
	res = request(t, x.client(t, hostB), r, 200)
	if body, _ := io.ReadAll(res.Body); string(body) != "upstream /" {
		t.Fatalf("generic root body %q", body)
	}
}

func TestLoginCodeExpiry(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.NotFoundHandler(), func(g *Server) {
		future := time.Now().Add(11 * time.Minute)
		g.now = func() time.Time { return future }
	})
	login(t, x, hostA, x.codes[hostA], 401)
}

func TestOriginBoundary(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), nil)
	cookie := loginCookie(t, x, hostA)
	for _, tt := range []struct {
		name, method, origin, referer, site, mode, upgrade string
		status                                             int
	}{
		{name: "safe no origin", method: "GET", status: 200},
		{name: "same-origin referer", method: "GET", referer: "https://" + hostA + "/page", status: 200},
		{name: "same-origin referer port 443", method: "GET", referer: "https://" + hostA + ":443/page", status: 200},
		{name: "foreign referer", method: "GET", referer: "https://example.com/", status: 403},
		{name: "sibling referer", method: "GET", referer: "https://" + hostB + "/", status: 403},
		{name: "http referer", method: "GET", referer: "http://" + hostA + "/", status: 403},
		{name: "nested-navigate without same-origin site", method: "GET", mode: "nested-navigate", status: 403},
		{name: "nested-navigate with same-origin site", method: "GET", site: "same-origin", mode: "nested-navigate", status: 200},
		{name: "foreign", method: "GET", origin: "https://example.com", status: 403},
		{name: "null", method: "GET", origin: "null", status: 403},
		{name: "sibling", method: "GET", origin: "https://" + hostB, status: 403},
		{name: "port origin", method: "GET", origin: "https://" + hostA + ":443", status: 403},
		{name: "cross site", method: "GET", site: "cross-site", status: 403},
		{name: "same site", method: "GET", site: "same-site", status: 403},
		{name: "verified same site", method: "GET", origin: "https://" + hostA, site: "same-site", status: 200},
		{name: "unsafe absent", method: "POST", status: 403},
		{name: "unsafe correct", method: "POST", origin: "https://" + hostA, status: 200},
		{name: "preflight", method: "OPTIONS", origin: "https://example.com", status: 403},
		{name: "CONNECT", method: "CONNECT", status: 405},
		{name: "WS absent", method: "GET", upgrade: "websocket", status: 403},
		{name: "WS sibling", method: "GET", origin: "https://" + hostB, upgrade: "websocket", status: 403},
		{name: "TRACE", method: "TRACE", status: 405},
		{name: "unsupported upgrade", method: "GET", origin: "https://" + hostA, upgrade: "h2c", status: 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRequest(t, tt.method, hostA, "/")
			r.AddCookie(cookie)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				r.Header.Set("Referer", tt.referer)
			}
			if tt.site != "" {
				r.Header.Set("Sec-Fetch-Site", tt.site)
			}
			if tt.mode != "" {
				r.Header.Set("Sec-Fetch-Mode", tt.mode)
			}
			if tt.upgrade != "" {
				r.Header.Set("Upgrade", tt.upgrade)
				r.Header.Set("Connection", "Upgrade")
			}
			request(t, x.client(t, hostA), r, tt.status)
		})
	}
}

func TestUnauthenticatedRouteServesDirectlyAndEnforcesOrigin(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "open-ok")
	}), func(g *Server) {
		route := g.routes[hostA]
		route.Auth = false
		g.routes[hostA] = route
		delete(g.codes, hostA)
	})
	res := request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/"), 200)
	if res.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" || res.Header.Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Fatalf("missing CORP/COOP headers: %+v", res.Header)
	}
	// Login path redirects to landing when Auth is false.
	res = request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/.jailoc/login"), 303)
	if got := res.Header.Get("Location"); got != "/" {
		t.Fatalf("unauthenticated /.jailoc/login Location = %q, want /", got)
	}
	// Cross-site and foreign-Referer GETs are still refused on unauthenticated routes.
	r := newRequest(t, "GET", hostA, "/")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	request(t, x.client(t, hostA), r, 403)
	r = newRequest(t, "GET", hostA, "/")
	r.Header.Set("Referer", "https://evil.example/attack")
	request(t, x.client(t, hostA), r, 403)
	// Sibling route with Auth = true still requires login.
	request(t, x.client(t, hostB), newRequest(t, "GET", hostB, "/"), 303)
}

func TestLoginRequiresExactOriginAndAuthCoversUpgrade(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.NotFoundHandler(), nil)
	for _, origin := range []string{"", "null", "https://" + hostB} {
		r := newRequest(t, "POST", hostA, "/.jailoc/login")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		request(t, x.client(t, hostA), r, 403)
	}
	r := newRequest(t, "GET", hostA, "/ws")
	r.Header.Set("Origin", "https://"+hostA)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	request(t, x.client(t, hostA), r, 401)
	cookie := loginCookie(t, x, hostA)
	r = newRequest(t, "GET", hostA, "/.jailoc/unknown")
	r.AddCookie(cookie)
	request(t, x.client(t, hostA), r, 404)
	r = newRequest(t, "GET", hostA, "/")
	r.AddCookie(cookie)
	r.Header.Add("Origin", "https://"+hostA)
	r.Header.Add("Origin", "https://"+hostA)
	request(t, x.client(t, hostA), r, 403)
}

func TestProxyHeadersAndCookies(t *testing.T) {
	t.Parallel()
	seen := make(chan *http.Request, 1)
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		w.Header().Add("Set-Cookie", "__Host-jailoc=forged; Path=/; Secure")
		w.Header().Add("Set-Cookie", "__HOST-JAILOC=forged; Path=/; Secure")
		w.Header().Add("Set-Cookie", "app=ok; Domain=jailoc.local; Path=/sub")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Alt-Svc", `h2=":443"`)
		w.Header().Set("Clear-Site-Data", `"cookies"`)
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		w.Header().Set("Cross-Origin-Opener-Policy", "unsafe-none")
		w.WriteHeader(200)
	}), nil)
	cookie := loginCookie(t, x, hostA)
	r := newRequest(t, "GET", hostA, "/asset.js?x=1")
	r.Host = hostA + ":443"
	r.AddCookie(cookie)
	r.AddCookie(&http.Cookie{Name: appCookiePrefix(hostA) + "app", Value: "ok", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	r.AddCookie(&http.Cookie{Name: appCookiePrefix(hostB) + "app", Value: "other-route", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	r.AddCookie(&http.Cookie{Name: "parent_domain_js", Value: "attacker", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Evil"} {
		r.Header.Set(h, "attacker")
	}
	res := request(t, x.client(t, hostA), r, 200)
	u := <-seen
	if strings.Contains(u.Header.Get("Cookie"), "jailoc") || strings.Contains(u.Header.Get("Cookie"), "parent_domain_js") || strings.Contains(u.Header.Get("Cookie"), "other-route") || u.Header.Get("Cookie") != "app=ok" {
		t.Fatalf("upstream cookie: %q", u.Header.Get("Cookie"))
	}
	if u.Host != hostA+":443" || u.Header.Get("X-Forwarded-Host") != hostA+":443" || u.Header.Get("X-Forwarded-Proto") != "https" || u.Header.Get("X-Forwarded-For") != "127.0.0.1" || !strings.Contains(u.Header.Get("Forwarded"), `host="`+hostA+`:443"`) || u.Header.Get("X-Forwarded-Evil") != "" {
		t.Fatalf("upstream host/forwarding: %s %+v", u.Host, u.Header)
	}
	if len(res.Cookies()) != 1 || res.Cookies()[0].Name != appCookiePrefix(hostA)+"app" || res.Cookies()[0].Value != "ok" || res.Cookies()[0].Domain != "" || res.Cookies()[0].Path != "/" || !res.Cookies()[0].Secure || res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Alt-Svc") != "" || res.Header.Get("Clear-Site-Data") != "" || res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "same-origin" || !slices.Equal(res.Header.Values("Cross-Origin-Resource-Policy"), []string{"same-origin"}) || !slices.Equal(res.Header.Values("Cross-Origin-Opener-Policy"), []string{"same-origin"}) {
		t.Fatalf("upstream response filtering: %+v", res.Header)
	}
}

const gatewayWorkerPolicy = "worker-src 'none'"

func countPolicy(h http.Header) int {
	n := 0
	for _, v := range h.Values("Content-Security-Policy") {
		if v == gatewayWorkerPolicy {
			n++
		}
	}
	return n
}

func TestServiceWorkerScriptFetchRejected(t *testing.T) {
	t.Parallel()
	var upstream atomic.Int32
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstream.Add(1)
		w.WriteHeader(200)
	}), nil)
	cookie := loginCookie(t, x, hostA)
	for _, tt := range []struct {
		name, path string
		headers    [][2]string
		auth       bool
	}{
		{name: "authenticated Service-Worker script", path: "/sw.js", headers: [][2]string{{"Service-Worker", "script"}}, auth: true},
		{name: "authenticated Sec-Fetch-Dest", path: "/sw.js", headers: [][2]string{{"Sec-Fetch-Dest", "serviceworker"}}, auth: true},
		{name: "mixed case values", path: "/sw.js", headers: [][2]string{{"service-worker", " Script "}, {"sec-fetch-dest", "ServiceWorker"}}, auth: true},
		{name: "second header value", path: "/sw.js", headers: [][2]string{{"Sec-Fetch-Dest", "empty"}, {"Sec-Fetch-Dest", "serviceworker"}}, auth: true},
		{name: "unauthenticated", path: "/sw.js", headers: [][2]string{{"Service-Worker", "script"}}},
		{name: "login path", path: "/.jailoc/login", headers: [][2]string{{"Service-Worker", "script"}}},
		{name: "login page", path: "/.jailoc/login", headers: [][2]string{{"Sec-Fetch-Dest", "serviceworker"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRequest(t, "GET", hostA, tt.path)
			if tt.auth {
				r.AddCookie(cookie)
			}
			for _, h := range tt.headers {
				r.Header.Add(h[0], h[1])
			}
			res := request(t, x.client(t, hostA), r, 403)
			if countPolicy(res.Header) != 1 {
				t.Fatalf("worker policy: %v", res.Header.Values("Content-Security-Policy"))
			}
		})
	}
	if n := upstream.Load(); n != 0 {
		t.Fatalf("upstream dialed %d times", n)
	}
	// Ordinary destinations are unaffected.
	r := newRequest(t, "GET", hostA, "/asset.js")
	r.AddCookie(cookie)
	r.Header.Set("Sec-Fetch-Dest", "script")
	request(t, x.client(t, hostA), r, 200)
}

func TestProxyWorkerPolicyOnEveryResponse(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A hostile upstream tries to loosen or replace the policy.
		w.Header().Add("Content-Security-Policy", "worker-src *; default-src *")
		w.Header().Add("Content-Security-Policy", gatewayWorkerPolicy)
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(302)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html></html>")
		}
	}), nil)
	cookie := loginCookie(t, x, hostA)
	check := func(t *testing.T, name string, res *http.Response, upstreamPolicy bool) {
		t.Helper()
		vals := res.Header.Values("Content-Security-Policy")
		ours := 0
		for _, v := range vals {
			if v == gatewayWorkerPolicy {
				ours++
			}
		}
		if ours == 0 {
			t.Fatalf("%s: gateway policy missing: %v", name, vals)
		}
		if upstreamPolicy && !slices.Contains(vals, "worker-src *; default-src *") {
			t.Fatalf("%s: upstream policy dropped: %v", name, vals)
		}
	}
	for _, tt := range []struct {
		name, method, path string
		auth               bool
		status             int
		upstream           bool
	}{
		{"proxied html", "GET", "/page.html", true, 200, true},
		{"proxied redirect", "GET", "/redirect", true, 302, true},
		{"proxied 404 path", "GET", "/missing", true, 200, true},
		{"login redirect", "GET", "/page.html", false, 303, false},
		{"login page", "GET", "/.jailoc/login", false, 200, false},
		{"login failure", "POST", "/.jailoc/login", false, 403, false},
		{"unknown gateway path", "GET", "/.jailoc/unknown", true, 404, false},
		{"origin forbidden", "GET", "/page.html", true, 403, false},
		{"unauthenticated POST", "POST", "/page.html", false, 401, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRequest(t, tt.method, hostA, tt.path)
			if tt.auth {
				r.AddCookie(cookie)
			}
			if tt.name == "origin forbidden" {
				r.Header.Set("Origin", "https://example.com")
			}
			if tt.name == "unauthenticated POST" {
				r.Header.Set("Origin", "https://"+hostA)
			}
			res := request(t, x.client(t, hostA), r, tt.status)
			check(t, tt.name, res, tt.upstream)
			if n := countPolicy(res.Header); n != 1 && !tt.upstream {
				t.Fatalf("duplicate gateway policy: %v", res.Header.Values("Content-Security-Policy"))
			}
		})
	}
	// Misdirected and invalid-authority responses carry the policy too.
	r := newRequest(t, "GET", hostA, "/")
	r.Host = "unknown.example"
	res := request(t, x.client(t, hostA), r, 421)
	check(t, "misdirected", res, false)
}

func TestLoginPageKeepsProxyWorkerPolicy(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.NotFoundHandler(), nil)
	res := request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/.jailoc/login"), 200)
	vals := res.Header.Values("Content-Security-Policy")
	if !slices.Contains(vals, gatewayWorkerPolicy) || !slices.ContainsFunc(vals, func(v string) bool { return strings.HasPrefix(v, "default-src 'none'") }) {
		t.Fatalf("login policies: %v", vals)
	}
	if countPolicy(res.Header) != 1 {
		t.Fatalf("duplicate proxy policy: %v", vals)
	}
}

// Browsers send "Origin: null" on a form POST from a page with
// Referrer-Policy no-referrer (Fetch, "serializing a request origin"), which
// the exact-Origin check refuses. The login page must keep its own origin.
func TestLoginPageReferrerPolicyKeepsFormOrigin(t *testing.T) {
	t.Parallel()
	x := startServer(t, http.NotFoundHandler(), nil)
	res := request(t, x.client(t, hostA), newRequest(t, "GET", hostA, "/.jailoc/login"), 200)
	if got := res.Header.Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("login Referrer-Policy = %q, want same-origin", got)
	}
}

func TestStaticBodyRoute(t *testing.T) {
	t.Parallel()
	const host = "ws-one.jailoc.local"
	cert, pool := testCertificate(t, host)
	g, _, err := New(Config{Cert: cert, Routes: []Route{{Host: host, StaticBody: "OK"}}})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- g.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	x := &testServer{g: g, addr: ln.Addr().String(), pool: pool}
	client := x.client(t, host)

	res := request(t, client, newRequest(t, "GET", host, "/"), 200)
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "OK" {
		t.Fatalf("GET / body %q", body)
	}
	if got := res.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type %q", got)
	}

	res = request(t, client, newRequest(t, "HEAD", host, "/"), 200)
	if body, err = io.ReadAll(res.Body); err != nil || len(body) != 0 {
		t.Fatalf("HEAD / body %q, err %v", body, err)
	}

	request(t, client, newRequest(t, "GET", host, "/other"), 404)

	r := newRequest(t, "POST", host, "/")
	r.Header.Set("Origin", "https://"+host)
	request(t, client, r, 405)

	r = newRequest(t, "GET", host, "/")
	r.Header.Set("Origin", "https://"+host)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	request(t, client, r, 400)
}
