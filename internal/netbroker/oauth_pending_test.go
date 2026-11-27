package netbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const pendingSecretMark = "SECRETMARK"

// pendingAuthURL is a realistic authorization URL whose redirect_uri targets
// the loopback callback on port and path (already percent-encoded by the
// caller via url.QueryEscape in the redirect).
func pendingAuthURL(redirect string) string {
	return "https://claude.ai/oauth/authorize?code=true&client_id=abc&response_type=code&redirect_uri=" +
		url.QueryEscape(redirect) + "&scope=a+b%20c&state=" + pendingSecretMark
}

func loopbackRedirect(host string, port uint16, path string) string {
	return fmt.Sprintf("http://%s:%d%s", host, port, path)
}

func TestValidateOAuthLink(t *testing.T) {
	t.Parallel()
	const port = 19876
	good := pendingAuthURL(loopbackRedirect("localhost", port, "/callback"))
	link := func(raw string) OAuthLink {
		return OAuthLink{Port: port, Path: "/callback", URL: raw}
	}
	valid := []struct {
		name string
		link OAuthLink
	}{
		{"localhost https", link(good)},
		{"plain http authorization url", link(strings.Replace(good, "https://", "http://", 1))},
		{"127.0.0.1", link(pendingAuthURL(loopbackRedirect("127.0.0.1", port, "/callback")))},
		{"IPv6 loopback", link(pendingAuthURL(fmt.Sprintf("http://[::1]:%d/callback", port)))},
		{"upper-case host and scheme in redirect", link(pendingAuthURL(fmt.Sprintf("HTTP://LOCALHOST:%d/callback", port)))},
		{"empty redirect path is /", OAuthLink{Port: port, Path: "/", URL: pendingAuthURL(fmt.Sprintf("http://localhost:%d", port))}},
		{"decoded redirect path", OAuthLink{Port: port, Path: "/a b", URL: pendingAuthURL(fmt.Sprintf("http://localhost:%d/a%%20b", port))}},
		{"lower-case hex, empty pairs and values", link(good + "&x=&&y&z=%7e%2b")},
		{"non-canonical port digits", link(pendingAuthURL("http://localhost:019876/callback"))},
	}
	for _, tt := range valid {
		t.Run("valid "+tt.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateOAuthLink(tt.link); err != nil {
				t.Fatalf("ValidateOAuthLink: %v", err)
			}
		})
	}

	enc := url.QueryEscape
	base := "https://claude.ai/oauth/authorize?state=" + pendingSecretMark + "&redirect_uri="
	okRedirect := enc(loopbackRedirect("localhost", port, "/callback"))
	invalid := []struct {
		name string
		link OAuthLink
	}{
		{"empty url", link("")},
		{"relative url", link("/oauth/authorize?redirect_uri=" + okRedirect)},
		{"scheme-relative url", link("//claude.ai/oauth/authorize?redirect_uri=" + okRedirect)},
		{"ftp scheme", link("ftp://claude.ai/x?state=" + pendingSecretMark + "&redirect_uri=" + okRedirect)},
		{"javascript scheme", link("javascript:alert(1)//?redirect_uri=" + okRedirect)},
		{"empty host", link("https:///x?state=" + pendingSecretMark + "&redirect_uri=" + okRedirect)},
		{"opaque", link("https:claude.ai/x?state=" + pendingSecretMark + "&redirect_uri=" + okRedirect)},
		{"userinfo", link("https://user:pw" + pendingSecretMark + "@claude.ai/x?redirect_uri=" + okRedirect)},
		{"fragment", link(base + okRedirect + "#frag")},
		{"empty fragment", link(base + okRedirect + "#")},
		{"space", link(base + okRedirect + "&a=b c")},
		{"tab", link(base + okRedirect + "&a=b\tc")},
		{"newline", link(base + okRedirect + "&a=b\nc")},
		{"escape byte", link(base + okRedirect + "&a=\x1b[31m")},
		{"del byte", link(base + okRedirect + "&a=\x7f")},
		{"backslash", link(base + okRedirect + "&a=b\\c")},
		{"unicode space", link(base + okRedirect + "&a=b\u00a0c")},
		{"bidi override", link(base + okRedirect + "&a=\u202e")},
		{"invalid utf8", link(base + okRedirect + "&a=\xff")},
		{"too long", link(base + okRedirect + "&a=" + strings.Repeat("x", maxOAuthURLBytes))},
		{"no redirect_uri", link("https://claude.ai/x?state=" + pendingSecretMark)},
		{"no query", link("https://claude.ai/x")},
		{"empty redirect_uri", link(base)},
		{"duplicate redirect_uri", link(base + okRedirect + "&redirect_uri=" + okRedirect)},
		{"duplicate encoded key", link(base + okRedirect + "&redirect%5Furi=" + okRedirect)},
		{"duplicate with evil second", link(base + okRedirect + "&redirect_uri=" + enc("http://evil.example/"))},
		{"malformed query escape", link(base + okRedirect + "&a=%zz")},
		{"truncated query escape", link(base + okRedirect + "&a=%4")},
		{"semicolon separator", link(base + okRedirect + ";a=b")},
		{"malformed path escape", link("https://claude.ai/%zz?state=" + pendingSecretMark + "&redirect_uri=" + okRedirect)},
		{"decoded control in value", link(base + okRedirect + "&a=%1b%5b31m")},
		{"decoded NUL in value", link(base + okRedirect + "&a=%00")},
		{"decoded bidi in key", link(base + okRedirect + "&%E2%80%AEa=b")},
		{"decoded invalid utf8", link(base + okRedirect + "&a=%ff")},
		{"decoded control in url path", link("https://claude.ai/a%1bb?state=" + pendingSecretMark + "&redirect_uri=" + okRedirect)},
		{"redirect https", link(base + enc(fmt.Sprintf("https://localhost:%d/callback", port)))},
		{"redirect non-loopback host", link(base + enc(loopbackRedirect("evil.example", port, "/callback")))},
		{"redirect lookalike host", link(base + enc(loopbackRedirect("localhost.evil.example", port, "/callback")))},
		{"redirect private ip", link(base + enc(loopbackRedirect("10.0.0.1", port, "/callback")))},
		{"redirect IPv6 zone", link(base + enc(fmt.Sprintf("http://[::1%%25eth0]:%d/callback", port)))},
		{"redirect userinfo", link(base + enc(fmt.Sprintf("http://localhost:%d@evil.example/callback", port)))},
		{"redirect userinfo localhost", link(base + enc(fmt.Sprintf("http://u@localhost:%d/callback", port)))},
		{"redirect opaque", link(base + enc("http:localhost"))},
		{"redirect fragment", link(base + enc(loopbackRedirect("localhost", port, "/callback")+"#x"))},
		{"redirect query", link(base + enc(loopbackRedirect("localhost", port, "/callback")+"?x=1"))},
		{"redirect empty query", link(base + enc(loopbackRedirect("localhost", port, "/callback")+"?"))},
		{"redirect missing port", link(base + enc("http://localhost/callback"))},
		{"redirect port mismatch", link(base + enc(loopbackRedirect("localhost", port+1, "/callback")))},
		{"redirect port overflow", link(base + enc("http://localhost:99999/callback"))},
		{"redirect path mismatch", link(base + enc(loopbackRedirect("localhost", port, "/other")))},
		{"redirect path prefix", link(base + enc(loopbackRedirect("localhost", port, "/callback/x")))},
		{"redirect path case", link(base + enc(loopbackRedirect("localhost", port, "/Callback")))},
		{"redirect empty path mismatch", link(base + enc(fmt.Sprintf("http://localhost:%d", port)))},
		{"redirect whitespace", link(base + enc(loopbackRedirect("localhost", port, "/call back")))},
		{"redirect plus is space", link(base + okRedirect + "+x")},
		{"redirect backslash", link(base + enc(fmt.Sprintf("http://localhost:%d\\@evil.example/callback", port)))},
		{"redirect decoded control path", OAuthLink{Port: port, Path: "/cb\x1b", URL: base + enc(loopbackRedirect("localhost", port, "/cb%1b"))}},
		{"redirect malformed escape", link(base + enc(loopbackRedirect("localhost", port, "/cb%zz")))},
		{"binding port zero", OAuthLink{Port: 0, Path: "/callback", URL: good}},
		{"binding privileged port", OAuthLink{Port: 80, Path: "/callback", URL: pendingAuthURL(loopbackRedirect("localhost", 80, "/callback"))}},
		{"binding relative path", OAuthLink{Port: port, Path: "callback", URL: good}},
		{"binding empty path", OAuthLink{Port: port, Path: "", URL: good}},
		{"binding path with query", OAuthLink{Port: port, Path: "/callback?x", URL: good}},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOAuthLink(tt.link)
			if err == nil {
				t.Fatalf("ValidateOAuthLink accepted %q", tt.link.URL)
			}
			// Errors are generic: they never carry the URL, any part of it, or
			// the standard library's own (URL-echoing) error text.
			msg := err.Error()
			for _, leak := range []string{pendingSecretMark, "claude.ai", "evil.example", "redirect_uri=", "%", "\x1b"} {
				if strings.Contains(msg, leak) {
					t.Fatalf("error %q echoes %q", msg, leak)
				}
			}
			if tt.link.URL != "" && strings.Contains(msg, tt.link.URL) {
				t.Fatalf("error %q echoes the raw URL", msg)
			}
		})
	}
}

func TestValidateOAuthLinkIgnoresExpiry(t *testing.T) {
	t.Parallel()
	l := OAuthLink{Port: 19876, Path: "/cb", URL: pendingAuthURL(loopbackRedirect("localhost", 19876, "/cb"))}
	if err := ValidateOAuthLink(l); err != nil {
		t.Fatalf("zero Expires rejected: %v", err)
	}
	l.Expires = time.Unix(1, 0)
	if err := ValidateOAuthLink(l); err != nil {
		t.Fatalf("past Expires rejected: %v", err)
	}
}

func TestOAuthLinkJSONContract(t *testing.T) {
	t.Parallel()
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	got, err := json.Marshal(OAuthLink{Port: 19876, Path: "/cb", URL: "https://a/b", Expires: exp})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"port":19876,"path":"/cb","url":"https://a/b","expires":"2030-01-02T03:04:05Z"}`
	if string(got) != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}

// pendingEnv is a running broker with its pending socket and fake guest.
type pendingEnv struct {
	ctx    context.Context
	cancel context.CancelFunc
	b      *Broker
	g      *stack.Stack
	log    *syncBuf
	sock   string
}

func startPendingEnv(t *testing.T, ttl time.Duration, forwards ...Forward) *pendingEnv {
	t.Helper()
	// Short socket path to stay under the sun_path limit on macOS.
	dir, err := os.MkdirTemp("", "np")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "oauth.sock")
	ctx, cancel, b, g, logBuf := startTestOAuthBrokerCancel(t, func(c *Config) {
		c.OAuthCallbackTTL = ttl
		c.Forwards = forwards
		c.OAuthSocket = sock
	})
	return &pendingEnv{ctx: ctx, cancel: cancel, b: b, g: g, log: logBuf, sock: sock}
}

func (e *pendingEnv) register(t *testing.T, port uint16, path, authURL string) int {
	t.Helper()
	return registerFromGuestURL(t, e.ctx, e.g, port, path, authURL)
}

func unixClient(sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}
}

func getPendingRaw(t *testing.T, sock string) (*http.Response, []byte) {
	t.Helper()
	resp, err := unixClient(sock).Get("http://oauth/pending")
	if err != nil {
		t.Fatalf("GET /pending: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read /pending: %v", err)
	}
	return resp, body
}

func getPending(t *testing.T, sock string) []OAuthLink {
	t.Helper()
	resp, body := getPendingRaw(t, sock)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /pending status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var links []OAuthLink
	if err := json.Unmarshal(body, &links); err != nil {
		t.Fatalf("decode /pending %q: %v", body, err)
	}
	if links == nil {
		t.Fatalf("/pending body %q decodes to null, want an array", body)
	}
	return links
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// callbackOf returns the registered callback of port, or nil.
func (e *pendingEnv) callbackOf(port uint16) *oauthCallback {
	e.b.oauth.mu.Lock()
	defer e.b.oauth.mu.Unlock()
	return e.b.oauth.byPort[port]
}

// storedURL reads the URL held by cb under the oauth lock.
func (e *pendingEnv) storedURL(cb *oauthCallback) string {
	e.b.oauth.mu.Lock()
	defer e.b.oauth.mu.Unlock()
	return cb.authURL
}

func TestOAuthPendingEmptyIsArray(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	_, body := getPendingRaw(t, e.sock)
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("empty body = %q, want []", body)
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %v, want none", links)
	}
}

func TestOAuthPendingRoutes(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	c := unixClient(e.sock)
	for _, tt := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/pending", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/pending", http.StatusMethodNotAllowed},
		{http.MethodGet, "/other", http.StatusNotFound},
		{http.MethodGet, "/", http.StatusNotFound},
		{http.MethodGet, "/pending/extra", http.StatusNotFound},
	} {
		req, err := http.NewRequest(tt.method, "http://oauth"+tt.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tt.method, tt.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Fatalf("%s %s status = %d, want %d", tt.method, tt.path, resp.StatusCode, tt.want)
		}
	}
}

func TestOAuthPendingListsRegisteredURLByteExact(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	// Odd but valid bytes: lower-case escapes, empty pairs and values, a literal
	// '&' sequence JSON would escape, repeated unrelated keys.
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/mcp/cb")) + "&a=1&a=2&&x=&y&z=%7e%2b%2f&amp=<>&q=%e2%9c%93"
	before := time.Now()
	if code := e.register(t, port, "/mcp/cb", raw); code != http.StatusOK {
		t.Fatalf("register status = %d, want 200", code)
	}
	after := time.Now()

	links := getPending(t, e.sock)
	if len(links) != 1 {
		t.Fatalf("links = %v, want exactly one", links)
	}
	l := links[0]
	if l.Port != port || l.Path != "/mcp/cb" || l.URL != raw {
		t.Fatalf("link = %+v, want port %d path /mcp/cb url %q", l, port, raw)
	}
	if err := ValidateOAuthLink(l); err != nil {
		t.Fatalf("listed link does not validate: %v", err)
	}
	if l.Expires.Before(before.Add(time.Minute)) || l.Expires.After(after.Add(time.Minute)) {
		t.Fatalf("Expires = %v, want host clock now+TTL in [%v, %v]", l.Expires, before.Add(time.Minute), after.Add(time.Minute))
	}

	// Reads do not consume the link and never refresh its expiry.
	for range 3 {
		again := getPending(t, e.sock)
		if !reflect.DeepEqual(again, links) {
			t.Fatalf("second read = %+v, want %+v", again, links)
		}
	}
	if cb := e.callbackOf(port); cb == nil || cb.served.Load() {
		t.Fatal("reading /pending consumed or served the callback")
	}
}

func TestOAuthPendingOlderGuestPayloadHasNoLink(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	if code := e.register(t, port, "/cb", ""); code != http.StatusOK {
		t.Fatalf("register status = %d, want 200", code)
	}
	if e.callbackOf(port) == nil {
		t.Fatal("older payload did not open the callback")
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %v, want none for a payload without url", links)
	}
}

func TestOAuthRegisterInvalidURLRejectedBeforeBindOrReplace(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	good := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))

	// An invalid URL does not bind a fresh port...
	bad := pendingAuthURL(loopbackRedirect("localhost", port+1, "/cb"))
	if code := e.register(t, port, "/cb", bad); code != http.StatusBadRequest {
		t.Fatalf("register mismatched URL status = %d, want 400", code)
	}
	if e.callbackOf(port) != nil {
		t.Fatal("invalid URL created a callback")
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("invalid URL bound the host port")
	}

	// ...and does not replace an active registration.
	if code := e.register(t, port, "/cb", good); code != http.StatusOK {
		t.Fatalf("register status = %d, want 200", code)
	}
	first := e.callbackOf(port)
	if code := e.register(t, port, "/cb", bad); code != http.StatusBadRequest {
		t.Fatalf("re-register invalid status = %d, want 400", code)
	}
	if got := e.callbackOf(port); got != first || first.served.Load() {
		t.Fatal("invalid URL replaced or stopped the active callback")
	}
	links := getPending(t, e.sock)
	if len(links) != 1 || links[0].URL != good {
		t.Fatalf("links = %+v, want the original link", links)
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err != nil {
		t.Fatalf("original host listener closed: %v", err)
	} else {
		_ = c.Close()
	}
}

func TestOAuthRegisterBodyAndURLCaps(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	prefix := pendingAuthURL(loopbackRedirect("localhost", port, "/cb")) + "&pad="

	// A URL at the raw-byte cap whose every '&' the guest's json.Marshal
	// escapes as \u0026 still fits the body cap.
	atCap := prefix + strings.Repeat("&", maxOAuthURLBytes-len(prefix))
	if len(atCap) != maxOAuthURLBytes {
		t.Fatalf("test URL length = %d, want %d", len(atCap), maxOAuthURLBytes)
	}
	if code := e.register(t, port, "/cb", atCap); code != http.StatusOK {
		t.Fatalf("register at cap status = %d, want 200", code)
	}
	if links := getPending(t, e.sock); len(links) != 1 || links[0].URL != atCap {
		t.Fatalf("at-cap link not listed byte-exact")
	}

	port2 := freePort(t)
	over := pendingAuthURL(loopbackRedirect("localhost", port2, "/cb")) + "&pad=" + strings.Repeat("x", maxOAuthURLBytes)
	if code := e.register(t, port2, "/cb", over); code != http.StatusBadRequest {
		t.Fatalf("register over cap status = %d, want 400", code)
	}
	if e.callbackOf(port2) != nil {
		t.Fatal("over-cap URL opened a callback")
	}

	huge := fmt.Sprintf(`{"port":%d,"path":"/cb","url":"%s"}`, port2, strings.Repeat("x", maxOAuthRegisterBodyBytes))
	if code := registerPayloadFromGuest(t, e.ctx, e.g, []byte(huge)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("register over body cap status = %d, want 413", code)
	}
}

func TestOAuthRegisterConfiguredForwardHasNoPendingEntry(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	e := startPendingEnv(t, time.Minute, Forward{
		Host:      netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port),
		GuestPort: port,
	})
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))
	if code := e.register(t, port, "/cb", raw); code != http.StatusOK {
		t.Fatalf("register status = %d, want 200", code)
	}
	if e.callbackOf(port) != nil {
		t.Fatal("configured forward got a synthetic callback entry")
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %v, want none for a configured forward", links)
	}
}

func TestOAuthPendingFailedBindIsNotListed(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	if code := e.register(t, port, "/cb", pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))); code != http.StatusConflict {
		t.Fatalf("register status = %d, want 409", code)
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %v, want none after failed bind", links)
	}
	if e.callbackOf(port) != nil {
		t.Fatal("failed bind left a callback registered")
	}
}

func TestOAuthPendingSupersede(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	a := pendingAuthURL(loopbackRedirect("localhost", port, "/a")) + "&n=a"
	b := pendingAuthURL(loopbackRedirect("localhost", port, "/b")) + "&n=b"

	if code := e.register(t, port, "/a", a); code != http.StatusOK {
		t.Fatalf("register a status = %d", code)
	}
	cbA := e.callbackOf(port)
	if code := e.register(t, port, "/b", b); code != http.StatusOK {
		t.Fatalf("register b status = %d", code)
	}
	if got := e.storedURL(cbA); got != "" {
		t.Fatalf("superseded callback still holds a URL (%d bytes)", len(got))
	}
	links := getPending(t, e.sock)
	if len(links) != 1 || links[0].URL != b || links[0].Path != "/b" {
		t.Fatalf("links = %+v, want only the replacement", links)
	}

	// An older guest payload without url supersedes too and leaves nothing listed.
	cbB := e.callbackOf(port)
	if code := e.register(t, port, "/c", ""); code != http.StatusOK {
		t.Fatalf("register c status = %d", code)
	}
	if got := e.storedURL(cbB); got != "" {
		t.Fatalf("superseded callback still holds a URL (%d bytes)", len(got))
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %+v, want none after urlless supersede", links)
	}
}

func TestOAuthPendingExpiry(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, 200*time.Millisecond)
	port := freePort(t)
	if code := e.register(t, port, "/cb", pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))); code != http.StatusOK {
		t.Fatalf("register status = %d", code)
	}
	cb := e.callbackOf(port)
	if links := getPending(t, e.sock); len(links) != 1 {
		t.Fatalf("links = %v, want one before expiry", links)
	}
	eventually(t, "link to expire", func() bool { return len(getPending(t, e.sock)) == 0 })
	eventually(t, "URL to be cleared", func() bool { return e.storedURL(cb) == "" })
}

func TestOAuthPendingExpiredNotListedBeforeTimerFires(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	if code := e.register(t, port, "/cb", pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))); code != http.StatusOK {
		t.Fatalf("register status = %d", code)
	}
	cb := e.callbackOf(port)
	e.b.oauth.mu.Lock()
	cb.expires = time.Now().Add(-time.Second)
	e.b.oauth.mu.Unlock()
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %v, want an expired link hidden by the host clock", links)
	}
}

func TestOAuthPendingCancelAndShutdown(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	p1, p2 := freePort(t), freePort(t)
	for _, p := range []uint16{p1, p2} {
		if code := e.register(t, p, "/cb", pendingAuthURL(loopbackRedirect("localhost", p, "/cb"))); code != http.StatusOK {
			t.Fatalf("register %d status = %d", p, code)
		}
	}
	cb1, cb2 := e.callbackOf(p1), e.callbackOf(p2)

	e.b.cancelOAuthPort(p1)
	if got := e.storedURL(cb1); got != "" {
		t.Fatal("cancelled callback still holds a URL")
	}
	links := getPending(t, e.sock)
	if len(links) != 1 || links[0].Port != p2 {
		t.Fatalf("links = %+v, want only port %d", links, p2)
	}

	e.cancel()
	eventually(t, "URL cleared on shutdown", func() bool { return e.storedURL(cb2) == "" })
	eventually(t, "socket removed on shutdown", func() bool {
		_, err := os.Lstat(e.sock)
		return errors.Is(err, os.ErrNotExist)
	})
	e.b.oauth.mu.Lock()
	n := len(e.b.oauth.byPort)
	e.b.oauth.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d callbacks left after shutdown", n)
	}
}

func TestOAuthPendingClaimedBeforeSlowResponse(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	hit := make(chan struct{})
	release := make(chan struct{})
	startFakeGuestLoopbackRelay(t, e.g, map[uint16]func(net.Conn){
		port: func(c net.Conn) {
			_, _ = http.ReadRequest(bufio.NewReader(c))
			close(hit)
			<-release
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		},
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))
	if code := e.register(t, port, "/cb", raw); code != http.StatusOK {
		t.Fatalf("register status = %d", code)
	}
	cb := e.callbackOf(port)
	if links := getPending(t, e.sock); len(links) != 1 {
		t.Fatalf("links = %v, want one before the callback", links)
	}

	done := make(chan int, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/cb?code=abc", port))
		if err != nil {
			done <- -1
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case <-hit:
	case <-time.After(5 * time.Second):
		t.Fatal("callback never reached the guest")
	}

	// The guest is still answering: the callback entry exists, yet it was
	// claimed, so its URL is gone and it is no longer offered.
	if e.callbackOf(port) != cb {
		t.Fatal("callback entry removed before the slow response finished")
	}
	if got := e.storedURL(cb); got != "" {
		t.Fatal("claimed callback still holds its URL during the slow response")
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %+v, want none once claimed", links)
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", code)
	}
}

func TestOAuthPendingBridgeRegisterPendingCallbackNoURLInLog(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	port := freePort(t)
	var hits atomic.Int32
	startFakeGuestLoopbackRelay(t, e.g, map[uint16]func(net.Conn){
		port: func(c net.Conn) {
			hits.Add(1)
			_, _ = http.ReadRequest(bufio.NewReader(c))
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		},
	})
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))
	if code := e.register(t, port, "/cb", raw); code != http.StatusOK {
		t.Fatalf("register status = %d", code)
	}
	links := getPending(t, e.sock)
	if len(links) != 1 || links[0].URL != raw || links[0].Port != port {
		t.Fatalf("links = %+v, want the registered link", links)
	}

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/cb?code=abc&state=x", port))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("callback status = %d hits = %d, want 200 and 1", resp.StatusCode, hits.Load())
	}
	eventually(t, "served event", func() bool { return strings.Contains(e.log.String(), `"action":"served"`) })
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %+v, want none after the callback", links)
	}
	logged := e.log.String()
	for _, leak := range []string{pendingSecretMark, "claude.ai", "client_id", "redirect_uri", "authorize", `"url"`} {
		if strings.Contains(logged, leak) {
			t.Fatalf("broker log leaks %q: %s", leak, logged)
		}
	}
}

func TestOAuthPendingSocketModeAndCleanup(t *testing.T) {
	t.Parallel()
	e := startPendingEnv(t, time.Minute)
	fi, err := os.Lstat(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want a unix socket with 0600", fi.Mode())
	}
	e.cancel()
	eventually(t, "socket removal", func() bool {
		_, err := os.Lstat(e.sock)
		return errors.Is(err, os.ErrNotExist)
	})
}

func newBareBroker(t *testing.T, sock string) *Broker {
	t.Helper()
	cfg := Config{
		Subnet:      netip.MustParsePrefix("192.168.127.0/24"),
		Gateway:     netip.MustParseAddr("192.168.127.1"),
		GuestIP:     netip.MustParseAddr("192.168.127.2"),
		OAuthSocket: sock,
		Log:         io.Discard,
	}
	b, err := New(cfg, DenyAll("test"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func shortDir(t *testing.T, mode os.FileMode) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "np")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOAuthPendingStopDoesNotRemoveOtherSocket(t *testing.T) {
	t.Parallel()
	dir := shortDir(t, 0o700)
	sock := filepath.Join(dir, "oauth.sock")
	b := newBareBroker(t, sock)
	stop, err := b.startOAuthPendingServer(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// A different process takes over the path.
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	other, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if ul, ok := other.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	stop()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("stop removed a socket it did not create: %v", err)
	}
	stop() // idempotent
}

func TestOAuthPendingStartRejectsUnsafeLocations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		sock  func(t *testing.T) string
		check func(t *testing.T, sock string)
	}{
		{"parent group readable", func(t *testing.T) string {
			return filepath.Join(shortDir(t, 0o750), "oauth.sock")
		}, nil},
		{"parent world accessible", func(t *testing.T) string {
			return filepath.Join(shortDir(t, 0o755), "oauth.sock")
		}, nil},
		{"parent group writable", func(t *testing.T) string {
			return filepath.Join(shortDir(t, 0o770), "oauth.sock")
		}, nil},
		{"parent missing", func(t *testing.T) string {
			return filepath.Join(shortDir(t, 0o700), "missing", "oauth.sock")
		}, nil},
		{"parent is a file", func(t *testing.T) string {
			f := filepath.Join(shortDir(t, 0o700), "file")
			if err := os.WriteFile(f, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(f, "oauth.sock")
		}, nil},
		{"path is a regular file", func(t *testing.T) string {
			p := filepath.Join(shortDir(t, 0o700), "oauth.sock")
			if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, func(t *testing.T, sock string) {
			if data, err := os.ReadFile(sock); err != nil || string(data) != "keep" { //nolint:gosec // G304: path inside this test's private temp dir
				t.Fatalf("regular file was touched: %q, %v", data, err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sock := tt.sock(t)
			stop, err := newBareBroker(t, sock).startOAuthPendingServer(context.Background())
			if err == nil {
				stop()
				t.Fatal("start accepted an unsafe socket location")
			}
			if tt.check != nil {
				tt.check(t, sock)
			}
		})
	}
}

func TestOAuthPendingStartReplacesStaleSocket(t *testing.T) {
	t.Parallel()
	dir := shortDir(t, 0o700)
	sock := filepath.Join(dir, "oauth.sock")
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("stale socket not left behind: %v", err)
	}
	stop, err := newBareBroker(t, sock).startOAuthPendingServer(context.Background())
	if err != nil {
		t.Fatalf("start over stale socket: %v", err)
	}
	if links := getPending(t, sock); len(links) != 0 {
		t.Fatalf("links = %v", links)
	}
	stop()
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left after stop: %v", err)
	}
}

func TestOAuthPendingConnectionBound(t *testing.T) {
	t.Parallel()
	dir := shortDir(t, 0o700)
	sock := filepath.Join(dir, "oauth.sock")
	b := newBareBroker(t, sock)
	b.oauth.pendingTimeout.Store(int64(300 * time.Millisecond))
	stop, err := b.startOAuthPendingServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	// Idle connections beyond the cap are dropped at once; the ones within it
	// are dropped by the read deadline. Nothing stays open.
	const n = maxOAuthPendingConns + 6
	conns := make([]net.Conn, 0, n)
	for range n {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		conns = append(conns, c)
	}
	var closedFast int
	for _, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err != nil && !isTimeout(err) {
			closedFast++
		}
	}
	if closedFast < n-maxOAuthPendingConns {
		t.Fatalf("closed %d idle connections immediately, want at least %d over the cap", closedFast, n-maxOAuthPendingConns)
	}
	for i, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil || isTimeout(err) {
			t.Fatalf("idle connection %d stayed open past the deadline: %v", i, err)
		}
	}
	// Capacity is released.
	if links := getPending(t, sock); len(links) != 0 {
		t.Fatalf("links = %v", links)
	}
}

func TestMainOAuthSocketFlag(t *testing.T) {
	t.Parallel()
	dir := shortDir(t, 0o700)
	policy := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policy, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := shortDir(t, 0o755)
	err := Main([]string{
		"-socket", filepath.Join(dir, "nic.sock"),
		"-policy", policy,
		"-oauth-socket", filepath.Join(open, "oauth.sock"),
	})
	if err == nil || !strings.Contains(err.Error(), "oauth") {
		t.Fatalf("Main with a non-private -oauth-socket parent = %v, want an oauth error", err)
	}
	if _, serr := os.Lstat(filepath.Join(dir, "nic.sock")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("nic socket left after the early return: %v", serr)
	}
}
