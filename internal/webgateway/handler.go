package webgateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
)

// workerPolicy is added to every gateway response. Browsers enforce every
// Content-Security-Policy header, so an upstream policy cannot weaken it.
const workerPolicy = "worker-src 'none'"

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	host := externalHostname(r.Host)
	if host == "" {
		http.Error(w, "invalid authority", http.StatusBadRequest)
		return
	}
	route, ok := g.route(host)
	if !ok || r.TLS == nil || r.TLS.ServerName != host {
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	if !sameOriginRequest(r, host) {
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
		g.loginHandler(w, r, host)
		return
	}
	s := g.authenticated(r, host)
	if s == nil {
		w.Header().Set("Cache-Control", "no-store")
		if upgrade == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			http.Redirect(w, r, "/.jailoc/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if start := route.StartPath; start != "" && r.URL.Path == "/" && r.URL.RawQuery == "" && upgrade == "" &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, start, http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	g.proxy(route, s).ServeHTTP(&sessionWriter{ResponseWriter: w, owner: s}, r.WithContext(ctx))
}

func externalHostname(authority string) string {
	host := authority
	if strings.ContainsRune(authority, ':') {
		var port string
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil || port != "443" {
			return ""
		}
	}
	if !validHostname(host) {
		return ""
	}
	return host
}

func sameOriginRequest(r *http.Request, host string) bool {
	origins, present := r.Header["Origin"]
	exact := len(origins) == 1 && origins[0] == "https://"+host
	if present && !exact {
		return false
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
		return n, fmt.Errorf("write gateway response: %w", err)
	}
	return n, nil
}
