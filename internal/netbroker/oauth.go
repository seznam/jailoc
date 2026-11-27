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
	defaultOAuthCallbackTTL = 3 * time.Minute
	maxOAuthCallbacks       = 8
	minOAuthCallbackPort    = 1024
	maxOAuthCallbackPath    = 2048
	// maxOAuthURLBytes caps the optional authorization URL of a registration.
	maxOAuthURLBytes = 8192
	// maxOAuthRegisterBodyBytes leaves room for a maximal URL whose every byte
	// JSON-escapes to six ('&' becomes \u0026) plus the other members.
	maxOAuthRegisterBodyBytes = 64 << 10
	oauthProxyTimeout         = 30 * time.Second

	defaultOAuthRegisterTimeout = 10 * time.Second
)

type oauthState struct {
	mu     sync.Mutex
	byPort map[uint16]*oauthCallback
	// registerTimeout overrides defaultOAuthRegisterTimeout (nanoseconds) for
	// the registration request body; zero keeps the default. Tests lower it.
	registerTimeout atomic.Int64
	// pendingTimeout overrides defaultOAuthPendingTimeout the same way for the
	// host pending endpoint.
	pendingTimeout atomic.Int64
}

type oauthCallback struct {
	port      uint16
	path      string
	served    atomic.Bool
	listeners []net.Listener
	server    *http.Server
	timer     *time.Timer
	closeOnce sync.Once

	// authURL and expires are the authorization link handed to the host CLI.
	// Both are guarded by oauthState.mu. authURL is only ever held while the
	// callback is registered, unserved and live, and is cleared the moment the
	// callback is claimed, expires, is replaced, cancelled or shut down.
	authURL string
	expires time.Time
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
			// Shutdown waits for the active handler to flush its response;
			// Close then force-drops anything still open after the bound.
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = c.server.Shutdown(sctx)
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

func (b *Broker) oauthRegisterTimeout() time.Duration {
	if d := time.Duration(b.oauth.registerTimeout.Load()); d > 0 {
		return d
	}
	return defaultOAuthRegisterTimeout
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
		ReadTimeout:       defaultOAuthRegisterTimeout,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       10 * time.Second,
	}
	srv.SetKeepAlivesEnabled(false)
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		b.stopAllOAuthCallbacks()
	}()
	go func() {
		_ = srv.Serve(&flowLimitListener{Listener: ln, b: b})
	}()
	return nil
}

// flowLimitListener counts every accepted registration connection against the
// broker's TCP flow cap and drops connections beyond it.
type flowLimitListener struct {
	net.Listener
	b *Broker
}

func (l *flowLimitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err //nolint:wrapcheck // net.Listener contract: callers match net.ErrClosed
		}
		if !l.b.tcpFlows.acquire() {
			l.b.record(map[string]any{"event": "flow-limit", "proto": ProtoTCP, "guest_port": l.b.cfg.OAuthCallbackPort})
			_ = c.Close()
			continue
		}
		return &flowLimitConn{Conn: c, release: l.b.tcpFlows.release}, nil
	}
}

// flowLimitConn releases its flow slot once, on Close.
type flowLimitConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *flowLimitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err //nolint:wrapcheck // thin net.Conn wrapper
}

func (b *Broker) stopAllOAuthCallbacks() {
	b.oauth.mu.Lock()
	cbs := make([]*oauthCallback, 0, len(b.oauth.byPort))
	for p, cb := range b.oauth.byPort {
		cb.authURL = ""
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
	if cb != nil {
		cb.authURL = ""
	}
	b.oauth.mu.Unlock()
	if cb != nil {
		cb.stop()
	}
}

// isConfiguredForwardPortLocked reports whether a configured or runtime
// forward already maps host port to the same guest port. The caller must hold
// b.runtime.mu.
func (b *Broker) isConfiguredForwardPortLocked(port uint16) bool {
	for _, f := range b.cfg.Forwards {
		if f.UnixPath == "" && f.Host.IsValid() && f.Host.Port() == port && f.GuestPort == port {
			return true
		}
	}
	for f := range b.runtime.forwards {
		if hp, err := netip.ParseAddrPort(f.Host); err == nil && hp.Port() == port && f.GuestPort == port {
			return true
		}
	}
	return false
}

// dialGuestOAuthPort dials the guest's loopback listener for an OAuth callback.
// With a loopback relay configured it is the only path, so a process bound to
// the guest NIC address can neither shadow nor substitute for the 127.0.0.1
// callback listener.
func (b *Broker) dialGuestOAuthPort(ctx context.Context, port uint16) (net.Conn, error) {
	if b.cfg.LoopbackRelayPort != 0 {
		return b.dialGuestLoopback(ctx, port)
	}
	conn, err := gonet.DialContextTCP(ctx, b.stack, *guestAddr(b.cfg.GuestIP, port), ipv4.ProtocolNumber)
	if err != nil {
		return nil, fmt.Errorf("dial guest oauth port %d: %w", port, err)
	}
	return conn, nil
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
	// Bound the whole request, body included, so a stalled client cannot pin
	// the connection (and its flow slot).
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(b.oauthRegisterTimeout())); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		// URL is the authorization URL the guest asked the host to open. Older
		// guests omit it; they still get a callback, but no link is listed and
		// nothing is opened (200). With a URL the answer is 202 once the host
		// opener accepted it, else 502 and the link stays pending.
		URL string `json:"url"`
	}
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Port < minOAuthCallbackPort || !validOAuthPath(req.Path) {
		http.Error(w, "invalid port or path", http.StatusBadRequest)
		return
	}
	// Validate before anything binds or replaces a registration. The reply is
	// fixed text: the URL carries OAuth state and is never echoed.
	if req.URL != "" {
		if err := validateOAuthAuthURL(req.URL, req.Port, req.Path); err != nil {
			http.Error(w, "invalid authorization url", http.StatusBadRequest)
			return
		}
	}
	if err := b.registerOAuthCallback(ctx, req.Port, req.Path, req.URL); err != nil {
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
	// The callback is bound (or already forwarded) and runtime.mu is released:
	// the host browser is opened last, so a sign-in that finishes at once
	// finds its callback ready and a slow opener blocks no reload.
	if req.URL == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !b.openOAuthURL(ctx, r.Context(), req.URL) {
		http.Error(w, "could not open the host browser", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// registerOAuthCallback binds the callback unless a configured or runtime
// forward already maps the same host and guest port. It holds the runtime lock
// across that check and the host bind so a concurrent ApplyRuntime cannot
// cancel this callback and bind its forward in between. Lock order is
// runtime.mu, then oauth.mu.
func (b *Broker) registerOAuthCallback(ctx context.Context, port uint16, path, authURL string) error {
	b.runtime.mu.Lock()
	defer b.runtime.mu.Unlock()
	if b.isConfiguredForwardPortLocked(port) {
		return nil
	}
	return b.openOAuthCallback(ctx, port, path, authURL)
}

// openOAuthURL hands rawURL to the configured host opener and reports whether
// it accepted it. No lock is held: the opener may wait on the browser. An
// absent opener, a stopped broker or an opener error all answer false; the
// opener's error is discarded because it may carry the URL, which holds OAuth
// state. The opener's context ends with the broker or with the request.
func (b *Broker) openOAuthURL(ctx, reqCtx context.Context, rawURL string) bool {
	open := b.cfg.OpenURL
	if open == nil || ctx.Err() != nil {
		return false
	}
	octx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(reqCtx, cancel)
	defer stop()
	return open(octx, rawURL) == nil
}

var (
	errOAuthLimit = errors.New("too many active OAuth callbacks")
	errOAuthBind  = errors.New("host loopback port already in use")
)

func (b *Broker) openOAuthCallback(ctx context.Context, port uint16, path, authURL string) error {
	// Reserve the slot under the lock before binding so concurrent
	// registrations cannot overshoot maxOAuthCallbacks.
	cb := &oauthCallback{port: port, path: path}
	b.oauth.mu.Lock()
	if ctx.Err() != nil {
		b.oauth.mu.Unlock()
		return ctx.Err()
	}
	if b.oauth.byPort == nil {
		b.oauth.byPort = map[uint16]*oauthCallback{}
	}
	prev := b.oauth.byPort[port]
	if prev == nil && len(b.oauth.byPort) >= maxOAuthCallbacks {
		b.oauth.mu.Unlock()
		return errOAuthLimit
	}
	b.oauth.byPort[port] = cb
	if prev != nil {
		prev.authURL = ""
	}
	b.oauth.mu.Unlock()
	if prev != nil {
		prev.stop()
	}

	p := strconv.Itoa(int(port))
	var lc net.ListenConfig
	l4, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", p))
	if err != nil {
		b.removeOAuthCallback(port, cb)
		return fmt.Errorf("%w: %w", errOAuthBind, err)
	}
	listeners := []net.Listener{l4}
	if l6, err := lc.Listen(ctx, "tcp", net.JoinHostPort("::1", p)); err == nil {
		listeners = append(listeners, l6)
	}

	targetHost := net.JoinHostPort("127.0.0.1", p)
	transport := &http.Transport{
		DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			dctx, cancel := context.WithTimeout(dctx, dialTimeout)
			defer cancel()
			return b.dialGuestOAuthPort(dctx, port)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 10 * time.Second,
	}

	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       10 * time.Second,
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
			// Claimed: the link is spent from this instant, well before the
			// guest's possibly slow answer lets the entry be removed.
			b.clearOAuthURL(cb)
			defer func() {
				b.removeOAuthCallback(port, cb)
				b.record(map[string]any{
					"event":      "oauth-callback",
					"action":     "served",
					"host":       l4.Addr().String(),
					"guest_port": port,
					"path":       path,
				})
				go cb.stop()
			}()
			// Stop accepting any further connections immediately while the single
			// in-flight callback request completes.
			cb.closeListeners()

			pctx, cancel := context.WithTimeout(r.Context(), oauthProxyTimeout)
			defer cancel()

			// Forward only the callback path and query. Host browser headers
			// (cookies, credentials, origin) never reach the guest, and the
			// guest's response never reaches the host browser.
			out := &url.URL{Scheme: "http", Host: targetHost, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
			req, err := http.NewRequestWithContext(pctx, http.MethodGet, out.String(), nil)
			if err != nil {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			if isLoopbackHostHeader(r.Host) {
				req.Host = r.Host
			}
			req.Header.Set("User-Agent", "")
			resp, err := transport.RoundTrip(req)
			if err != nil {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()

			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "OAuth callback received. You may close this tab.\n")
		}),
	}
	srv.SetKeepAlivesEnabled(false)

	// Publish the listeners, server and timer under the lock, and only if cb
	// is still the registered callback: a cancel, supersede or shutdown that
	// ran while binding removed it, and nothing may then be left running.
	b.oauth.mu.Lock()
	if ctx.Err() != nil || b.oauth.byPort[port] != cb {
		if b.oauth.byPort[port] == cb {
			delete(b.oauth.byPort, port)
		}
		b.oauth.mu.Unlock()
		for _, l := range listeners {
			_ = l.Close()
		}
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // caller cancellation
		}
		return errOAuthBind
	}
	cb.listeners = listeners
	cb.server = srv
	ttl := b.oauthTTL()
	if authURL != "" {
		cb.authURL = authURL
		cb.expires = time.Now().Add(ttl)
	}
	cb.timer = time.AfterFunc(ttl, func() {
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
			_ = srv.Serve(ln)
		}(l)
	}
	return nil
}

func (b *Broker) removeOAuthCallback(port uint16, cb *oauthCallback) {
	b.oauth.mu.Lock()
	if b.oauth.byPort[port] == cb {
		delete(b.oauth.byPort, port)
	}
	cb.authURL = ""
	b.oauth.mu.Unlock()
}

// clearOAuthURL drops the authorization link of cb while leaving the callback
// itself registered.
func (b *Broker) clearOAuthURL(cb *oauthCallback) {
	b.oauth.mu.Lock()
	cb.authURL = ""
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
