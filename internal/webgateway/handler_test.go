package webgateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLoginIsRouteBoundAndSingleUse(t *testing.T) {
	t.Parallel()
	x := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), nil)
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
	x := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream "+r.URL.RequestURI())
	}), func(g *Gateway) {
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
	x := startGateway(t, http.NotFoundHandler(), func(g *Gateway) {
		future := time.Now().Add(11 * time.Minute)
		g.now = func() time.Time { return future }
	})
	login(t, x, hostA, x.codes[hostA], 401)
}

func TestOriginBoundary(t *testing.T) {
	t.Parallel()
	x := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), nil)
	cookie := loginCookie(t, x, hostA)
	for _, tt := range []struct {
		name, method, origin, site, upgrade string
		status                              int
	}{
		{name: "safe no origin", method: "GET", status: 200},
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
			if tt.site != "" {
				r.Header.Set("Sec-Fetch-Site", tt.site)
			}
			if tt.upgrade != "" {
				r.Header.Set("Upgrade", tt.upgrade)
				r.Header.Set("Connection", "Upgrade")
			}
			request(t, x.client(t, hostA), r, tt.status)
		})
	}
}

func TestLoginRequiresExactOriginAndAuthCoversUpgrade(t *testing.T) {
	t.Parallel()
	x := startGateway(t, http.NotFoundHandler(), nil)
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
	x := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		w.Header().Add("Set-Cookie", "__Host-jailoc=forged; Path=/; Secure")
		w.Header().Add("Set-Cookie", "__HOST-JAILOC=forged; Path=/; Secure")
		w.Header().Add("Set-Cookie", "app=ok; Path=/")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Alt-Svc", `h2=":443"`)
		w.WriteHeader(200)
	}), nil)
	cookie := loginCookie(t, x, hostA)
	r := newRequest(t, "GET", hostA, "/asset.js?x=1")
	r.Host = hostA + ":443"
	r.AddCookie(cookie)
	r.AddCookie(&http.Cookie{Name: "app", Value: "ok"})
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Evil"} {
		r.Header.Set(h, "attacker")
	}
	res := request(t, x.client(t, hostA), r, 200)
	u := <-seen
	if strings.Contains(u.Header.Get("Cookie"), "jailoc") || u.Header.Get("Cookie") != "app=ok" {
		t.Fatalf("upstream cookie: %q", u.Header.Get("Cookie"))
	}
	if u.Host != hostA+":443" || u.Header.Get("X-Forwarded-Host") != hostA+":443" || u.Header.Get("X-Forwarded-Proto") != "https" || u.Header.Get("X-Forwarded-For") != "127.0.0.1" || !strings.Contains(u.Header.Get("Forwarded"), `host="`+hostA+`:443"`) || u.Header.Get("X-Forwarded-Evil") != "" {
		t.Fatalf("upstream host/forwarding: %s %+v", u.Host, u.Header)
	}
	if len(res.Cookies()) != 1 || res.Cookies()[0].Name != "app" || res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Alt-Svc") != "" {
		t.Fatalf("upstream response filtering: %+v", res.Header)
	}
}
