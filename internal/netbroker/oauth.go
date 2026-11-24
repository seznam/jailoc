package netbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

const (
	defaultOAuthCallbackTTL   = 3 * time.Minute
	maxOAuthCallbacks         = 8
	minOAuthCallbackPort      = 1024
	maxOAuthCallbackPath      = 2048
	maxOAuthRegisterBodyBytes = 4096
	oauthProxyTimeout         = 30 * time.Second
)

type oauthState struct {
	mu     sync.Mutex
	byPort map[uint16]*oauthCallback
}

type oauthCallback struct {
	port      uint16
	path      string
	served    atomic.Bool
	listeners []net.Listener
	server    *http.Server
	timer     *time.Timer
	closeOnce sync.Once
}

func (c *oauthCallback) closeListeners() {
	for _, l := range c.listeners {
		_ = l.Close()
	}
}

func (c *oauthCallback) stop() {
	c.closeOnce.Do(func() {
		if c.timer != nil {
			c.timer.Stop()
		}
		c.closeListeners()
		if c.server != nil {
			_ = c.server.Close()
		}
	})
}

func (b *Broker) oauthTTL() time.Duration {
	if b.cfg.OAuthCallbackTTL > 0 {
		return b.cfg.OAuthCallbackTTL
	}
	return defaultOAuthCallbackTTL
}

// startOAuthCallbackServer serves the gateway-side OAuth loopback callback
// registration endpoint on b.cfg.Gateway:b.cfg.OAuthCallbackPort.
func (b *Broker) startOAuthCallbackServer(ctx context.Context) error {
	ln, err := gonet.ListenTCP(b.stack, *guestAddr(b.cfg.Gateway, b.cfg.OAuthCallbackPort), ipv4.ProtocolNumber)
	if err != nil {
		return fmt.Errorf("oauth callback listener on port %d: %w", b.cfg.OAuthCallbackPort, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		b.handleOAuthRegister(ctx, w, r)
	})
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		b.stopAllOAuthCallbacks()
	}()
	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

func (b *Broker) stopAllOAuthCallbacks() {
	b.oauth.mu.Lock()
	cbs := make([]*oauthCallback, 0, len(b.oauth.byPort))
	for p, cb := range b.oauth.byPort {
		cbs = append(cbs, cb)
		delete(b.oauth.byPort, p)
	}
	b.oauth.mu.Unlock()
	for _, cb := range cbs {
		cb.stop()
	}
}

// cancelOAuthPort stops any active ephemeral OAuth callback listener on port
// so an explicitly configured Forward can bind it during reload.
func (b *Broker) cancelOAuthPort(port uint16) {
	b.oauth.mu.Lock()
	cb := b.oauth.byPort[port]
	delete(b.oauth.byPort, port)
	b.oauth.mu.Unlock()
	if cb != nil {
		cb.stop()
	}
}

func (b *Broker) isConfiguredForwardPort(port uint16) bool {
	for _, f := range b.cfg.Forwards {
		if f.UnixPath == "" && f.Host.IsValid() && f.Host.Port() == port {
			return true
		}
	}
	b.runtime.mu.Lock()
	defer b.runtime.mu.Unlock()
	for f := range b.runtime.forwards {
		if hp, err := netip.ParseAddrPort(f.Host); err == nil && hp.Port() == port {
			return true
		}
	}
	return false
}

func validOAuthPath(path string) bool {
	if len(path) == 0 || len(path) > maxOAuthCallbackPath || path[0] != '/' {
		return false
	}
	for i := range len(path) {
		c := path[i]
		if c < 0x20 || c == 0x7f || c == '?' || c == '#' {
			return false
		}
	}
	return true
}

func (b *Broker) handleOAuthRegister(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxOAuthRegisterBodyBytes+1))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	if len(body) > maxOAuthRegisterBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req struct {
		Port uint16 `json:"port"`
		Path string `json:"path"`
	}
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Port < minOAuthCallbackPort || !validOAuthPath(req.Path) {
		http.Error(w, "invalid port or path", http.StatusBadRequest)
		return
	}
	if b.isConfiguredForwardPort(req.Port) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := b.openOAuthCallback(ctx, req.Port, req.Path); err != nil {
		switch {
		case errors.Is(err, errOAuthLimit):
			http.Error(w, err.Error(), http.StatusTooManyRequests)
		case errors.Is(err, errOAuthBind):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
}

var (
	errOAuthLimit = errors.New("too many active OAuth callbacks")
	errOAuthBind  = errors.New("host loopback port already in use")
)

func (b *Broker) openOAuthCallback(ctx context.Context, port uint16, path string) error {
	b.oauth.mu.Lock()
	if b.oauth.byPort == nil {
		b.oauth.byPort = map[uint16]*oauthCallback{}
	}
	prev := b.oauth.byPort[port]
	if prev == nil && len(b.oauth.byPort) >= maxOAuthCallbacks {
		b.oauth.mu.Unlock()
		return errOAuthLimit
	}
	if prev != nil {
		delete(b.oauth.byPort, port)
	}
	b.oauth.mu.Unlock()
	if prev != nil {
		prev.stop()
	}

	p := strconv.Itoa(int(port))
	var lc net.ListenConfig
	l4, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", p))
	if err != nil {
		return fmt.Errorf("%w: %w", errOAuthBind, err)
	}
	listeners := []net.Listener{l4}
	if l6, err := lc.Listen(ctx, "tcp", net.JoinHostPort("::1", p)); err == nil {
		listeners = append(listeners, l6)
	}

	targetURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", p)}
	cb := &oauthCallback{
		port:      port,
		path:      path,
		listeners: listeners,
	}
	transport := &http.Transport{
		DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			return b.dialGuestPort(dctx, port)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			if isLoopbackHostHeader(pr.In.Host) {
				pr.Out.Host = pr.In.Host
			}
		},
	}

	cb.server = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Upgrade") != "" {
				http.Error(w, "upgrade not allowed", http.StatusBadRequest)
				return
			}
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			if !q.Has("code") && !q.Has("error") {
				http.NotFound(w, r)
				return
			}
			if !cb.served.CompareAndSwap(false, true) {
				http.Error(w, "callback already served", http.StatusGone)
				return
			}
			// Stop accepting any further connections immediately while the single
			// in-flight callback request completes.
			cb.closeListeners()

			pctx, cancel := context.WithTimeout(r.Context(), oauthProxyTimeout)
			defer cancel()
			proxy.ServeHTTP(w, r.WithContext(pctx))

			b.removeOAuthCallback(port, cb)
			b.record(map[string]any{
				"event":      "oauth-callback",
				"action":     "served",
				"host":       l4.Addr().String(),
				"guest_port": port,
				"path":       path,
			})
			go cb.stop()
		}),
	}

	cb.timer = time.AfterFunc(b.oauthTTL(), func() {
		if cb.served.CompareAndSwap(false, true) {
			b.removeOAuthCallback(port, cb)
			cb.stop()
			b.record(map[string]any{
				"event":      "oauth-callback",
				"action":     "expired",
				"host":       l4.Addr().String(),
				"guest_port": port,
				"path":       path,
			})
		}
	})

	b.oauth.mu.Lock()
	if ctx.Err() != nil {
		b.oauth.mu.Unlock()
		cb.stop()
		return ctx.Err()
	}
	if existing := b.oauth.byPort[port]; existing != nil {
		defer existing.stop()
	}
	b.oauth.byPort[port] = cb
	b.oauth.mu.Unlock()

	b.record(map[string]any{
		"event":      "oauth-callback",
		"action":     "open",
		"host":       l4.Addr().String(),
		"guest_port": port,
		"path":       path,
	})

	for _, l := range listeners {
		go func(ln net.Listener) {
			_ = cb.server.Serve(ln)
		}(l)
	}
	return nil
}

func (b *Broker) removeOAuthCallback(port uint16, cb *oauthCallback) {
	b.oauth.mu.Lock()
	if b.oauth.byPort[port] == cb {
		delete(b.oauth.byPort, port)
	}
	b.oauth.mu.Unlock()
}

func isLoopbackHostHeader(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}
