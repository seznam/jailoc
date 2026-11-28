package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// workerPolicy is added to every gateway response. Browsers enforce every
// Content-Security-Policy header, so an upstream policy cannot weaken it.
const workerPolicy = "worker-src 'none'"

func (g *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = &policyWriter{ResponseWriter: w}
	addWorkerPolicy(w.Header())
	// A guest-installed root-scoped worker can outlive its VM. Refuse to serve
	// any worker script so no new one is installed or updated.
	if isServiceWorkerFetch(r.Header) {
		http.Error(w, "service workers are not supported", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodConnect || r.Method == http.MethodTrace {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host := g.externalHostname(r.Host)
	if host == "" {
		http.Error(w, "invalid authority", http.StatusBadRequest)
		return
	}
	route, ok := g.route(host)
	if !ok || r.TLS == nil || r.TLS.ServerName != host {
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	if !g.sameOriginRequest(r, host) {
		http.Error(w, "origin forbidden", http.StatusForbidden)
		return
	}
	upgrade := r.Header.Get("Upgrade")
	if upgrade != "" && !strings.EqualFold(upgrade, "websocket") {
		http.Error(w, "upgrade not supported", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/.jailoc/") || r.URL.Path == "/.jailoc" {
		if upgrade != "" {
			http.Error(w, "upgrade not supported", http.StatusBadRequest)
			return
		}
		if !route.Auth {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, g.landing(host), http.StatusSeeOther) //nolint:gosec // G710: landing returns "/" or the validated same-origin Route.StartPath
				return
			}
			http.NotFound(w, r)
			return
		}
		g.loginHandler(w, r, host)
		return
	}
	var s *session
	if route.Auth {
		s = g.authenticated(r, host)
		if s == nil {
			w.Header().Set("Cache-Control", "no-store")
			if upgrade == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				http.Redirect(w, r, "/.jailoc/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
	} else {
		var current bool
		s, current = g.unauthenticatedSession(host, route)
		if !current {
			http.Error(w, "proxy route changed, retry the request", http.StatusServiceUnavailable)
			return
		}
		if s == nil {
			http.Error(w, "proxy stopped", http.StatusServiceUnavailable)
			return
		}
	}
	if start := route.StartPath; start != "" && r.URL.Path == "/" && r.URL.RawQuery == "" && upgrade == "" &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, start, http.StatusSeeOther)
		return
	}
	if route.StaticBody != "" {
		serveStatic(w, r, route, upgrade)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	g.reverseProxy(route, s).ServeHTTP(&sessionWriter{ResponseWriter: w, owner: s}, r.WithContext(ctx))
}

// serveStatic answers a route that has no upstream. Only GET and HEAD of the
// bare root are served; everything else is refused.
func serveStatic(w http.ResponseWriter, r *http.Request, route Route, upgrade string) {
	if upgrade != "" {
		http.Error(w, "upgrade not supported", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(route.StaticBody)) // the client may have gone away; there is nothing to recover
	}
}

// externalHostname returns the hostname of an HTTPS authority, or "" when the
// authority is malformed or names a port other than the external HTTPS port.
// The port may be omitted only when it is 443.
func (g *Server) externalHostname(authority string) string {
	return authorityHostname(authority, g.httpsPort, defaultHTTPSPort)
}

// authorityHostname returns the hostname of authority if it is a valid route
// hostname with exactly the port, or with no port when port is the implicit
// one of the scheme. It returns "" otherwise.
func authorityHostname(authority string, port, implicit int) string {
	host := authority
	if strings.ContainsRune(authority, ':') {
		var got string
		var err error
		host, got, err = net.SplitHostPort(authority)
		if err != nil || got != strconv.Itoa(port) {
			return ""
		}
	} else if port != implicit {
		return ""
	}
	if !validHostname(host) {
		return ""
	}
	return host
}

// origin is the exact Origin header value browsers send for host.
func (g *Server) origin(host string) string {
	if g.httpsPort == defaultHTTPSPort {
		return "https://" + host
	}
	return "https://" + host + ":" + strconv.Itoa(g.httpsPort)
}

// unauthenticatedSession returns the shared session of an unauthenticated
// route. It reports false when Update changed or removed the route after the
// caller looked it up, so no session outlives the revocation done by Update.
func (g *Server) unauthenticatedSession(host string, route Route) (*session, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cur, ok := g.routes[host]
	if !ok || cur.Auth || cur.Generation != route.Generation {
		return nil, false
	}
	if g.base == nil || g.base.Err() != nil {
		return nil, true
	}
	if s := g.unauth[host]; s != nil && s.ctx.Err() == nil {
		return s, true
	}
	ctx, cancel := context.WithCancel(g.base)
	s := &session{host: host, ctx: ctx, cancel: cancel, conns: make(map[*sessionConn]struct{})}
	g.unauth[host] = s
	return s, true
}

func (g *Server) sameOriginRequest(r *http.Request, host string) bool {
	origins, present := r.Header["Origin"]
	exact := len(origins) == 1 && origins[0] == g.origin(host)
	if present && !exact {
		return false
	}
	if referers := r.Header.Values("Referer"); len(referers) > 0 {
		if len(referers) > 1 {
			return false
		}
		u, err := url.Parse(referers[0])
		if err != nil || u.Scheme != "https" || u.User != nil || g.externalHostname(u.Host) != host {
			return false
		}
	}
	sites := r.Header.Values("Sec-Fetch-Site")
	if len(sites) > 1 {
		return false
	}
	if len(sites) == 1 {
		switch sites[0] {
		case "cross-site":
			return false
		case "same-site":
			if !exact {
				return false
			}
		case "same-origin", "none":
		default:
			return false
		}
	}
	if modes := r.Header.Values("Sec-Fetch-Mode"); len(modes) > 0 {
		if len(modes) > 1 {
			return false
		}
		if modes[0] == "nested-navigate" && (len(sites) != 1 || sites[0] != "same-origin") {
			return false
		}
	}
	if r.Header.Get("Upgrade") != "" {
		return exact
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return true
	default:
		return exact
	}
}

// isServiceWorkerFetch matches the headers browsers set on service worker
// script fetches. Clients that omit them are not browsers' service workers.
func isServiceWorkerFetch(h http.Header) bool {
	return headerHasToken(h, "Service-Worker", "script") || headerHasToken(h, "Sec-Fetch-Dest", "serviceworker")
}

func headerHasToken(h http.Header, name, want string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}

func addWorkerPolicy(h http.Header) {
	if !slices.Contains(h.Values("Content-Security-Policy"), workerPolicy) {
		h.Add("Content-Security-Policy", workerPolicy)
	}
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

// policyWriter re-adds the gateway policy at every WriteHeader, including
// informational responses, if a handler replaced the header.
type policyWriter struct{ http.ResponseWriter }

func (w *policyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *policyWriter) WriteHeader(status int) {
	addWorkerPolicy(w.Header())
	w.ResponseWriter.WriteHeader(status)
}

func (w *policyWriter) Write(p []byte) (int, error) {
	addWorkerPolicy(w.Header())
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		return n, fmt.Errorf("write proxy response: %w", err)
	}
	return n, nil
}
