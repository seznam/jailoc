package netbroker

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// maxOAuthPendingConns bounds the concurrent connections of the host
	// pending endpoint; connections beyond it are closed at accept.
	maxOAuthPendingConns       = 8
	defaultOAuthPendingTimeout = 5 * time.Second
)

// OAuthLink is one live authorization link the guest asked to open: the
// loopback callback the broker listens on for it (Port, Path), the URL byte
// for byte as the guest sent it, and the host-clock time the callback stops
// listening. It is the element type of the host-only GET /pending reply.
type OAuthLink struct {
	Port    uint16    `json:"port"`
	Path    string    `json:"path"`
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

// errOAuthURL is the only failure ValidateOAuthLink reports apart from a
// reason keyword: the URL carries OAuth state, so no error ever includes any
// of it, nor the text of the standard library's own (URL-echoing) errors.
var errOAuthURL = errors.New("invalid OAuth authorization link")

func badOAuthURL(reason string) error {
	return fmt.Errorf("%w: %s", errOAuthURL, reason)
}

// ValidateOAuthLink checks the URL and callback binding of l the way the
// broker did at registration, for consumers that must not trust the reply
// (the CLI validates before it prints). Expires is not checked: expiry is the
// caller's business against its own clock.
func ValidateOAuthLink(l OAuthLink) error {
	if l.URL == "" {
		return badOAuthURL("empty url")
	}
	return validateOAuthAuthURL(l.URL, l.Port, l.Path)
}

// validateOAuthAuthURL accepts raw only if it is an absolute http(s) URL with
// exactly one redirect_uri that is the plain loopback callback http://HOST:port
// + path, HOST being 127.0.0.1, localhost or [::1]. raw is never modified.
func validateOAuthAuthURL(raw string, port uint16, path string) error {
	if port < minOAuthCallbackPort || !validOAuthPath(path) || terminalUnsafe(path) {
		return badOAuthURL("callback binding")
	}
	if raw == "" || len(raw) > maxOAuthURLBytes {
		return badOAuthURL("length")
	}
	if !printableURLText(raw) || strings.Contains(raw, "#") {
		return badOAuthURL("characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return badOAuthURL("syntax")
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return badOAuthURL("scheme")
	}
	// u.Host is percent-decoded by url.Parse, so a raw-printable URL can still
	// carry a terminal-driving host the CLI would print as its origin.
	if u.Opaque != "" || u.User != nil || u.Hostname() == "" || !printableURLText(u.Host) {
		return badOAuthURL("authority")
	}
	if terminalUnsafe(u.Path) {
		return badOAuthURL("path")
	}
	vals, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return badOAuthURL("query")
	}
	for k, vs := range vals {
		if terminalUnsafe(k) {
			return badOAuthURL("query")
		}
		for _, v := range vs {
			if terminalUnsafe(v) {
				return badOAuthURL("query")
			}
		}
	}
	redirects := vals["redirect_uri"]
	if len(redirects) != 1 {
		return badOAuthURL("redirect_uri")
	}
	return validateOAuthRedirect(redirects[0], port, path)
}

func validateOAuthRedirect(redirect string, port uint16, path string) error {
	if redirect == "" || !printableURLText(redirect) || strings.ContainsAny(redirect, "#?") {
		return badOAuthURL("redirect_uri")
	}
	ru, err := url.Parse(redirect)
	if err != nil || strings.ToLower(ru.Scheme) != "http" || ru.Opaque != "" || ru.User != nil {
		return badOAuthURL("redirect_uri")
	}
	switch strings.ToLower(ru.Hostname()) {
	case "127.0.0.1", "localhost", "::1":
	default:
		return badOAuthURL("redirect_uri host")
	}
	p, err := strconv.ParseUint(ru.Port(), 10, 16)
	if err != nil || p != uint64(port) {
		return badOAuthURL("redirect_uri port")
	}
	rp := ru.Path
	if rp == "" {
		rp = "/"
	}
	if terminalUnsafe(rp) || rp != path {
		return badOAuthURL("redirect_uri path")
	}
	return nil
}

// printableURLText reports whether s, still in its raw (possibly
// percent-encoded) form, holds only valid UTF-8 without whitespace,
// backslashes, control or format characters.
func printableURLText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\\' || unicode.IsSpace(r) || terminalControlRune(r) {
			return false
		}
	}
	return true
}

// terminalUnsafe reports whether the decoded text s could drive a terminal:
// invalid UTF-8 or any control, format or line/paragraph separator rune.
func terminalUnsafe(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if terminalControlRune(r) {
			return true
		}
	}
	return false
}

func terminalControlRune(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

func (b *Broker) oauthPendingTimeout() time.Duration {
	if d := time.Duration(b.oauth.pendingTimeout.Load()); d > 0 {
		return d
	}
	return defaultOAuthPendingTimeout
}

// pendingOAuthLinks lists the links that are registered, unclaimed and not
// past their host-clock expiry, soonest expiry first. It only reads: nothing
// is consumed and no timer is touched.
func (b *Broker) pendingOAuthLinks(now time.Time) []OAuthLink {
	b.oauth.mu.Lock()
	links := make([]OAuthLink, 0, len(b.oauth.byPort))
	for _, cb := range b.oauth.byPort {
		if cb.authURL == "" || cb.served.Load() || !cb.expires.After(now) {
			continue
		}
		links = append(links, OAuthLink{Port: cb.port, Path: cb.path, URL: cb.authURL, Expires: cb.expires.UTC()})
	}
	b.oauth.mu.Unlock()
	slices.SortFunc(links, func(a, c OAuthLink) int {
		return cmp.Or(a.Expires.Compare(c.Expires), cmp.Compare(a.Port, c.Port))
	})
	return links
}

// startOAuthPendingServer serves GET /pending on the private unix socket
// b.cfg.OAuthSocket. The returned stop closes it and unlinks the socket if it
// is still the one created here; it is idempotent and also runs when ctx ends.
func (b *Broker) startOAuthPendingServer(ctx context.Context) (func(), error) {
	path := b.cfg.OAuthSocket
	if err := requirePrivateDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("oauth socket %s: %w", path, err)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, fmt.Errorf("oauth socket %s: %w", path, err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("oauth socket listen: %w", err)
	}
	// Unlinking is stop's job so it can check the path is still ours.
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("oauth socket chmod: %w", err)
	}
	created, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("oauth socket stat: %w", err)
	}

	d := b.oauthPendingTimeout()
	mux := http.NewServeMux()
	mux.HandleFunc("/pending", b.handleOAuthPending)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: d,
		ReadTimeout:       d,
		WriteTimeout:      d,
		IdleTimeout:       d,
		MaxHeaderBytes:    4 << 10,
	}
	srv.SetKeepAlivesEnabled(false)

	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			_ = srv.Close()
			_ = ln.Close()
			if cur, err := os.Lstat(path); err == nil && os.SameFile(created, cur) {
				_ = os.Remove(path)
			}
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	go func() {
		_ = srv.Serve(&connLimitListener{Listener: ln, slots: make(chan struct{}, maxOAuthPendingConns)})
	}()
	return stop, nil
}

func (b *Broker) handleOAuthPending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := json.Marshal(b.pendingOAuthLinks(time.Now()))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(append(body, '\n'))
}

// requirePrivateDir checks that dir exists and only its owner (the current
// user) can enter it, so no one else can reach a socket inside.
func requirePrivateDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("parent directory: %w", err)
	}
	if !fi.IsDir() {
		return errors.New("parent is not a directory")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("parent directory %s is accessible to group or others (mode %04o)", dir, fi.Mode().Perm())
	}
	return requireOwnedByCurrentUser(dir, fi)
}

// removeStaleSocket removes a socket left at path by an earlier run and
// refuses to touch anything else.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("stat: %w", err)
	case fi.Mode()&os.ModeSocket == 0:
		return errors.New("path exists and is not a socket")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

// connLimitListener drops connections beyond the slots it hands out.
type connLimitListener struct {
	net.Listener
	slots chan struct{}
}

func (l *connLimitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err //nolint:wrapcheck // net.Listener contract: callers match net.ErrClosed
		}
		select {
		case l.slots <- struct{}{}:
			return &slotConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			_ = c.Close()
		}
	}
}

// slotConn releases its slot once, on Close.
type slotConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *slotConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err //nolint:wrapcheck // thin net.Conn wrapper
}
