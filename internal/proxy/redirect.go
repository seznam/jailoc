package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ServeHTTPRedirect takes ownership of a TCP listener bound to a loopback IP
// and answers plain HTTP with redirects to the HTTPS origin. It never reaches
// a login or an upstream: a request is redirected only when its Host names,
// with Config.HTTPPort (80 by default), a route that has not set
// DisableRedirect. Only GET and HEAD are redirected, to
// "https://<route host>[:HTTPSPort]" plus the request's escaped path and
// query, with 307 and no-store. The target host comes from the route table,
// never from the request, so the redirect cannot leave the allowlist. Every
// other request is refused. Cancellation closes the listener; it may be called
// once per Server, independently of Serve.
func (g *Server) ServeHTTPRedirect(ctx context.Context, ln net.Listener) error {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return fmt.Errorf("proxy redirect listener must be loopback TCP")
	}
	g.mu.Lock()
	if g.redirected {
		g.mu.Unlock()
		return fmt.Errorf("proxy redirect already served")
	}
	g.redirected = true
	g.mu.Unlock()
	srv := &http.Server{
		Handler: http.HandlerFunc(g.redirectHTTP), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute,
		MaxHeaderBytes: 32 << 10, ErrorLog: log.New(io.Discard, "", 0),
	}
	done := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { done <- srv.Close() })
	err := srv.Serve(ln)
	if stop() {
		// ctx was not canceled: the listener failed on its own.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve proxy redirect: %w", err)
		}
		return nil
	}
	if closeErr := <-done; closeErr != nil {
		return fmt.Errorf("close proxy redirect: %w", closeErr)
	}
	return nil
}

func (g *Server) redirectHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "upgrade not supported", http.StatusBadRequest)
		return
	}
	host := authorityHostname(r.Host, g.httpPort, defaultHTTPPort)
	if host == "" {
		http.Error(w, "invalid authority", http.StatusBadRequest)
		return
	}
	route, ok := g.route(host)
	if !ok || route.DisableRedirect {
		// One answer for unknown and opted-out hosts, so the route table is not probed.
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	target, ok := g.redirectTarget(host, r.URL)
	if !ok {
		http.Error(w, "invalid request target", http.StatusBadRequest)
		return
	}
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusTemporaryRedirect)
}

// redirectTarget is the HTTPS URL of u on host. The path and query stay as
// the client encoded them. It reports false for a target that is not an
// absolute path or whose query holds bytes browsers never send raw.
func (g *Server) redirectTarget(host string, u *url.URL) (string, bool) {
	path := u.EscapedPath()
	if path == "" || path[0] != '/' {
		return "", false
	}
	target := g.origin(host) + path
	if u.RawQuery != "" || u.ForceQuery {
		for _, c := range []byte(u.RawQuery) {
			if c <= ' ' || c >= 0x7f || c == '\\' {
				return "", false
			}
		}
		target += "?" + u.RawQuery
	}
	return target, true
}
