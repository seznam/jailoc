//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/netbroker"
)

var authTestNow = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

// authTestLink returns a link for a callback on port whose URL carries a
// secret marker that must never reach errors or stderr.
func authTestLink(port uint16, expiresIn time.Duration) netbroker.OAuthLink {
	return netbroker.OAuthLink{
		Port: port,
		Path: "/callback",
		URL: fmt.Sprintf("https://claude.ai/oauth/authorize?client_id=x&state=SECRETSTATE%d&redirect_uri=http%%3A%%2F%%2Flocalhost%%3A%d%%2Fcallback",
			port, port),
		Expires: authTestNow.Add(expiresIn),
	}
}

func authNow() time.Time { return authTestNow }

func TestRunAuthLinks(t *testing.T) {
	t.Parallel()
	good := authTestLink(40001, 150*time.Second)
	other := authTestLink(40002, 60*time.Second)
	expired := authTestLink(40003, -time.Second)
	atDeadline := authTestLink(40004, 0)
	bad := authTestLink(40005, time.Minute)
	bad.URL = "javascript:alert(1)//SECRETSTATE"
	dup := authTestLink(40001, time.Minute)

	many := make([]netbroker.OAuthLink, 0, 9)
	for i := range 9 {
		many = append(many, authTestLink(uint16(41000+i), time.Minute))
	}

	tests := []struct {
		name       string
		links      []netbroker.OAuthLink
		opts       authOptions
		wantOut    string
		wantErr    string
		wantErrOut []string
		wantOpened []string
	}{
		{name: "none", links: nil, wantErr: "no pending sign-in"},
		{name: "one prints raw url", links: []netbroker.OAuthLink{good}, wantOut: good.URL + "\n", wantErrOut: []string{"callback port 40001", "expires in 2m30s"}},
		{
			name: "ambiguous lists summary without urls", links: []netbroker.OAuthLink{other, good},
			wantErr:    "select one with --port",
			wantErrOut: []string{"port 40001  https://claude.ai  expires in 2m30s", "port 40002  https://claude.ai  expires in 1m0s"},
		},
		{name: "port selects", links: []netbroker.OAuthLink{other, good}, opts: authOptions{port: 40002}, wantOut: other.URL + "\n"},
		{name: "port not found", links: []netbroker.OAuthLink{good}, opts: authOptions{port: 40009}, wantErr: "no pending sign-in for callback port 40009 (pending: 40001)"},
		{name: "port expired", links: []netbroker.OAuthLink{good, expired}, opts: authOptions{port: 40003}, wantErr: "port 40003 expired"},
		{name: "expired only", links: []netbroker.OAuthLink{expired}, wantErr: "no pending sign-in"},
		{name: "expiry is exclusive", links: []netbroker.OAuthLink{atDeadline}, wantErr: "no pending sign-in"},
		{name: "expired ignored when one live", links: []netbroker.OAuthLink{expired, good}, wantOut: good.URL + "\n"},
		{name: "invalid link", links: []netbroker.OAuthLink{good, bad}, wantErr: "authorization link 2 from the broker is invalid"},
		{name: "duplicate port", links: []netbroker.OAuthLink{good, dup}, wantErr: "several authorization links for port 40001"},
		{name: "too many", links: many, wantErr: "at most 8"},
		{
			name: "open passes the same url", links: []netbroker.OAuthLink{good}, opts: authOptions{open: true},
			wantOpened: []string{good.URL}, wantErrOut: []string{"Opening sign-in page at https://claude.ai", "callback port 40001"},
		},
		{name: "open with port", links: []netbroker.OAuthLink{good, other}, opts: authOptions{open: true, port: 40002}, wantOpened: []string{other.URL}},
		{name: "open ambiguous opens nothing", links: []netbroker.OAuthLink{good, other}, opts: authOptions{open: true}, wantErr: "select one with --port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			var opened []string
			err := runAuthLinks(t.Context(), &out, &errOut, tt.links, tt.opts, authNow, func(_ context.Context, u string) error {
				opened = append(opened, u)
				return nil
			})
			assertAuthErr(t, err, tt.wantErr)
			if out.String() != tt.wantOut {
				t.Fatalf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			if strings.Join(opened, "\n") != strings.Join(tt.wantOpened, "\n") {
				t.Fatalf("opened = %q, want %q", opened, tt.wantOpened)
			}
			for _, want := range tt.wantErrOut {
				assertContains(t, errOut.String(), want)
			}
			// Secrets never reach stderr or errors, and with --open or an
			// error never stdout either.
			leaks := errOut.String()
			if err != nil {
				leaks += err.Error()
			}
			if strings.Contains(leaks, "SECRETSTATE") {
				t.Fatalf("url leaked into stderr/error: %q", leaks)
			}
			if (tt.opts.open || err != nil) && out.Len() != 0 {
				t.Fatalf("stdout must be empty, got %q", out.String())
			}
		})
	}
}

// TestRunAuthLinksRejectsEncodedAuthorityControls covers links whose raw text is
// printable ASCII but whose host percent-decodes to a terminal-driving
// character (bidi override, C1 CSI, line separator). The CLI must reject the
// whole response with the generic invalid error before it prints, opens or
// summarises anything.
func TestRunAuthLinksRejectsEncodedAuthorityControls(t *testing.T) {
	t.Parallel()
	hosts := []struct{ name, host string }{
		{name: "bidi override", host: "auth%E2%80%AE.example.test"},
		{name: "C1 control", host: "auth%C2%9B.example.test"},
		{name: "line separator", host: "auth%E2%80%A8.example.test"},
	}
	withHost := func(l netbroker.OAuthLink, host string) netbroker.OAuthLink {
		l.URL = strings.Replace(l.URL, "claude.ai", host, 1)
		return l
	}
	good := authTestLink(40002, time.Minute)
	other := authTestLink(40003, 2*time.Minute)

	modes := []struct {
		name string
		opts authOptions
	}{
		{name: "default", opts: authOptions{}},
		{name: "open", opts: authOptions{open: true}},
		{name: "open with port", opts: authOptions{open: true, port: 40002}},
	}
	for _, h := range hosts {
		bad := withHost(authTestLink(40001, time.Minute), h.host)
		lists := []struct {
			name    string
			links   []netbroker.OAuthLink
			wantErr string
		}{
			{name: "only link", links: []netbroker.OAuthLink{bad}, wantErr: "authorization link 1 from the broker is invalid"},
			{name: "bad before valid", links: []netbroker.OAuthLink{bad, good}, wantErr: "authorization link 1 from the broker is invalid"},
			{name: "bad after valid", links: []netbroker.OAuthLink{good, bad}, wantErr: "authorization link 2 from the broker is invalid"},
			// Without the bad link this list is ambiguous; validation of the
			// whole list must win over the ambiguity listing.
			{name: "bad among ambiguous valid links", links: []netbroker.OAuthLink{good, bad, other}, wantErr: "authorization link 2 from the broker is invalid"},
		}
		for _, l := range lists {
			for _, m := range modes {
				t.Run(h.name+"/"+l.name+"/"+m.name, func(t *testing.T) {
					t.Parallel()
					var out, errOut bytes.Buffer
					opened := 0
					err := runAuthLinks(t.Context(), &out, &errOut, l.links, m.opts, authNow, func(context.Context, string) error {
						opened++
						return nil
					})
					if err == nil {
						t.Fatal("error = nil, want invalid link error")
					}
					if err.Error() != l.wantErr {
						t.Fatalf("error = %q, want exactly %q", err.Error(), l.wantErr)
					}
					if out.Len() != 0 || errOut.Len() != 0 {
						t.Fatalf("nothing may be printed: stdout=%q stderr=%q", out.String(), errOut.String())
					}
					if opened != 0 {
						t.Fatalf("opener called %d times, want 0", opened)
					}
					all := err.Error() + errOut.String() + out.String()
					for _, leak := range []string{
						"SECRETSTATE", "https://", "example.test", "auth%", "claude.ai",
						"%E2%80%AE", "%C2%9B", "%E2%80%A8",
						"\u202e", "\u009b", "\u2028", "\xc2\x9b", "\xe2\x80",
					} {
						if strings.Contains(all, leak) {
							t.Fatalf("leaked %q: %q", leak, all)
						}
					}
				})
			}
		}
	}
}

func TestRunAuthLinksOpenerFailureNeverCarriesURL(t *testing.T) {
	t.Parallel()
	link := authTestLink(40001, time.Minute)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		openErr error
		want    string
	}{
		{name: "arbitrary error echoing the url", ctx: t.Context(), openErr: fmt.Errorf("exec failed for %s", link.URL), want: "the browser opener failed"},
		{name: "wrapped arbitrary error", ctx: t.Context(), openErr: fmt.Errorf("outer: %w", &os.PathError{Op: "open", Path: link.URL, Err: os.ErrNotExist}), want: "the browser opener failed"},
		{name: "specific opener error is replaced by the generic one", ctx: t.Context(), openErr: errors.New("the browser opener timed out"), want: "the browser opener failed"},
		{name: "canceled context is preserved", ctx: canceled, openErr: fmt.Errorf("killed %s", link.URL), want: "canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			err := runAuthLinks(tt.ctx, &out, &errOut, []netbroker.OAuthLink{link}, authOptions{open: true}, authNow, func(context.Context, string) error {
				return tt.openErr
			})
			assertAuthErr(t, err, "open sign-in page")
			assertContains(t, err.Error(), tt.want)
			if tt.ctx.Err() != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("context cause lost: %v", err)
			}
			for _, leak := range []string{"SECRETSTATE", link.URL, "claude.ai/oauth"} {
				if strings.Contains(err.Error()+errOut.String()+out.String(), leak) {
					t.Fatalf("leaked %q: err=%v stderr=%q stdout=%q", leak, err, errOut.String(), out.String())
				}
			}
			if errors.Unwrap(err) != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("error chain keeps the opener error: %v", errors.Unwrap(err))
			}
		})
	}
}

func TestRunAuthLinksRechecksExpiryAtSelectionTime(t *testing.T) {
	t.Parallel()
	link := authTestLink(40001, time.Second)
	var calls atomic.Int32
	now := func() time.Time {
		// Live while filtering, expired by the time the link is used.
		if calls.Add(1) <= 2 {
			return authTestNow
		}
		return authTestNow.Add(2 * time.Second)
	}
	var out, errOut bytes.Buffer
	err := runAuthLinks(t.Context(), &out, &errOut, []netbroker.OAuthLink{link}, authOptions{open: true}, now, func(context.Context, string) error {
		t.Fatal("opener must not run for an expired link")
		return nil
	})
	assertAuthErr(t, err, "expired")
}

func TestAuthPortOption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		changed bool
		port    int
		want    int
		wantErr string
	}{
		{name: "unset", want: 0},
		{name: "valid", changed: true, port: 40001, want: 40001},
		{name: "max", changed: true, port: 65535, want: 65535},
		{name: "zero", changed: true, port: 0, wantErr: "out of range"},
		{name: "negative", changed: true, port: -1, wantErr: "out of range"},
		{name: "too big", changed: true, port: 65536, wantErr: "out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := authPortOption(tt.changed, tt.port)
			assertAuthErr(t, err, tt.wantErr)
			if got != tt.want {
				t.Fatalf("port = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAuthCommandHelp(t *testing.T) {
	t.Parallel()
	cmd, _, err := rootCmd.Find([]string{"auth"})
	if err != nil || cmd != authCmd {
		t.Fatalf("auth command not registered: %v", err)
	}
	for _, name := range []string{"port", "open"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s", name)
		}
	}
	for _, name := range []string{"copy", "wait", "json"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Fatalf("unexpected --%s", name)
		}
	}
	help := cmd.UsageString() + cmd.Long
	for _, want := range []string{"auth [workspace]", "--open", "--port"} {
		assertContains(t, help, want)
	}
	if cmd.Short == "" || cmd.Long == "" {
		t.Fatal("auth needs a short and a long description")
	}
	for _, name := range []string{"port", "open"} {
		if cmd.Flags().Lookup(name).Usage == "" {
			t.Fatalf("--%s has no usage text", name)
		}
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Fatal("two workspace arguments must be rejected")
	}
}

// authSocketServer serves handler on a Unix socket in a short temp path and
// returns the socket path.
func authSocketServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "jlauth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "oauth.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return sock
}

func authJSONHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/pending" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestFetchOAuthLinks(t *testing.T) {
	t.Parallel()
	link := authTestLink(40001, time.Minute)
	one, err := json.Marshal([]netbroker.OAuthLink{link})
	if err != nil {
		t.Fatal(err)
	}
	huge := `[{"port":1,"path":"/","url":"https://x/` + strings.Repeat("a", authMaxBody) + `","expires":"2026-11-27T12:00:00Z"}]`

	tests := []struct {
		name      string
		handler   http.Handler
		wantLinks int
		wantErr   string
	}{
		{name: "empty", handler: authJSONHandler(`[]`)},
		{name: "null is malformed", handler: authJSONHandler(`null`), wantErr: "malformed"},
		{name: "one", handler: authJSONHandler(string(one)), wantLinks: 1},
		{name: "trailing data", handler: authJSONHandler(string(one) + ` []`), wantErr: "malformed"},
		{name: "trailing garbage", handler: authJSONHandler(string(one) + `x`), wantErr: "malformed"},
		{name: "unknown field", handler: authJSONHandler(`[{"port":1,"path":"/","url":"https://x/","expires":"2026-11-27T12:00:00Z","extra":1}]`), wantErr: "malformed"},
		{name: "not json", handler: authJSONHandler(`<html>SECRETSTATE`), wantErr: "malformed"},
		{name: "object instead of array", handler: authJSONHandler(`{}`), wantErr: "malformed"},
		{name: "oversize", handler: authJSONHandler(huge), wantErr: "too large"},
		{
			name: "server error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "SECRETSTATE", http.StatusInternalServerError)
			}),
			wantErr: "status 500",
		},
		{name: "old broker 404", handler: http.NotFoundHandler(), wantErr: errAuthNoSocket.Error()},
		{
			name: "redirect is not followed",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/pending" {
					http.Redirect(w, r, "/other", http.StatusFound)
					return
				}
				_, _ = w.Write([]byte(`[]`))
			}),
			wantErr: "status 302",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sock := authSocketServer(t, tt.handler)
			links, err := fetchOAuthLinks(t.Context(), sock, 5*time.Second)
			assertAuthErr(t, err, tt.wantErr)
			if err != nil && strings.Contains(err.Error(), "SECRETSTATE") {
				t.Fatalf("response content leaked into error: %v", err)
			}
			if len(links) != tt.wantLinks {
				t.Fatalf("got %d links, want %d", len(links), tt.wantLinks)
			}
		})
	}
}

// TestFetchOAuthLinksLegitimateMaximumResponse feeds what the broker's
// json.Marshal produces for eight valid links of the largest allowed URL:
// every & becomes a 6-byte \u0026, far beyond a plain 8 KiB per link.
func TestFetchOAuthLinksLegitimateMaximumResponse(t *testing.T) {
	t.Parallel()
	const maxURL = 8192
	links := make([]netbroker.OAuthLink, 0, authMaxLinks)
	for i := range authMaxLinks {
		port := uint16(40000 + i)
		u := fmt.Sprintf("https://claude.ai/oauth/authorize?redirect_uri=http%%3A%%2F%%2Flocalhost%%3A%d%%2Fcallback", port)
		for len(u)+4 <= maxURL {
			u += "&p=1"
		}
		u += strings.Repeat("&", maxURL-len(u))
		links = append(links, netbroker.OAuthLink{Port: port, Path: "/callback", URL: u, Expires: authTestNow.Add(time.Minute)})
	}
	body, err := json.Marshal(links)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 8*(8<<10+512) {
		t.Fatalf("fixture is only %d bytes; it must exceed the old 8*(8KiB+512) bound", len(body))
	}
	if len(body) > authMaxBody {
		t.Fatalf("legitimate response of %d bytes exceeds the bound %d", len(body), authMaxBody)
	}
	got, err := fetchOAuthLinks(t.Context(), authSocketServer(t, authJSONHandler(string(body))), 5*time.Second)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if err := validateAuthLinks(got); err != nil {
		t.Fatalf("legitimate links must validate: %v", err)
	}
	if len(got) != authMaxLinks {
		t.Fatalf("got %d links, want %d", len(got), authMaxLinks)
	}
}

func TestFetchOAuthLinksMissingSocket(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/tmp", "jlauth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	_, err = fetchOAuthLinks(t.Context(), filepath.Join(dir, "none.sock"), time.Second)
	if err != errAuthNoSocket {
		t.Fatalf("err = %v, want errAuthNoSocket", err)
	}
}

func TestFetchOAuthLinksTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	sock := authSocketServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release) })
	start := time.Now()
	_, err := fetchOAuthLinks(t.Context(), sock, 200*time.Millisecond)
	assertAuthErr(t, err, "did not answer in time")
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not enforced: %v", time.Since(start))
	}
}

func TestNewAuthClientIgnoresProxyEnvironment(t *testing.T) {
	t.Parallel()
	c := newAuthClient("/nonexistent.sock", time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("transport must have no proxy, got %#v", c.Transport)
	}
	if c.CheckRedirect == nil {
		t.Fatal("redirects must be disabled")
	}
}

// Not parallel: t.Setenv.
func TestFetchOAuthLinksBypassesProxy(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	t.Cleanup(proxy.Close)
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	sock := authSocketServer(t, authJSONHandler(`[]`))
	if _, err := fetchOAuthLinks(t.Context(), sock, 5*time.Second); err != nil {
		t.Fatalf("fetch through the unix socket failed: %v", err)
	}
	if proxied.Load() != 0 {
		t.Fatal("request went through the environment proxy")
	}
}

func TestHostOpenerCommand(t *testing.T) {
	t.Parallel()
	found := func(name string) (string, error) { return "/opt/bin/" + name, nil }
	missing := func(string) (string, error) { return "", fmt.Errorf("not found") }
	tests := []struct {
		name     string
		goos     string
		lookPath func(string) (string, error)
		want     string
		wantErr  string
	}{
		{name: "darwin is fixed", goos: "darwin", lookPath: missing, want: "/usr/bin/open"},
		{name: "linux xdg-open", goos: "linux", lookPath: found, want: "/opt/bin/xdg-open"},
		{name: "linux without xdg-open", goos: "linux", lookPath: missing, wantErr: "xdg-open was not found"},
		{name: "other", goos: "plan9", lookPath: found, wantErr: "not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := hostOpenerCommand(tt.goos, tt.lookPath)
			assertAuthErr(t, err, tt.wantErr)
			if got != tt.want {
				t.Fatalf("opener = %q, want %q", got, tt.want)
			}
		})
	}
}

// authOpenerScript writes an executable shell script and returns its path.
func authOpenerScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	return path
}

func TestOpenURLWith(t *testing.T) {
	t.Parallel()
	u := authTestLink(40001, time.Minute).URL
	dir := t.TempDir()
	marker := filepath.Join(dir, "args")
	record := authOpenerScript(t, dir, "record", `printf '%s\n' "$#" "$@" > `+marker)
	if err := openURLWith(t.Context(), record, u, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker) //nolint:gosec // G304: test temp path
	if err != nil {
		t.Fatal(err)
	}
	if want := "1\n" + u + "\n"; string(got) != want {
		t.Fatalf("opener args = %q, want %q", got, want)
	}

	t.Run("failure is generic and silent", func(t *testing.T) {
		t.Parallel()
		leaky := authOpenerScript(t, t.TempDir(), "leaky", `echo "$1"; echo "$1" >&2; exit 3`)
		err := openURLWith(t.Context(), leaky, u, 5*time.Second)
		assertAuthErr(t, err, "the browser opener failed")
		if strings.Contains(err.Error(), "SECRETSTATE") {
			t.Fatalf("opener output leaked: err=%v", err)
		}
	})
	t.Run("timeout is bounded", func(t *testing.T) {
		t.Parallel()
		slow := authOpenerScript(t, t.TempDir(), "slow", `exec sleep 30`)
		start := time.Now()
		err := openURLWith(t.Context(), slow, u, 200*time.Millisecond)
		assertAuthErr(t, err, "timed out")
		if time.Since(start) > 5*time.Second {
			t.Fatalf("not bounded: %v", time.Since(start))
		}
	})
	t.Run("missing opener", func(t *testing.T) {
		t.Parallel()
		err := openURLWith(t.Context(), filepath.Join(t.TempDir(), "none"), u, time.Second)
		assertAuthErr(t, err, "cannot be started")
	})
}

// Not parallel: t.Setenv. The default path must never exec the opener; an
// explicit --open runs it exactly once with the URL as the single argument.
func TestAuthOpenerViaPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PATH-resolved xdg-open is Linux only; macOS uses the fixed /usr/bin/open")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "calls")
	authOpenerScript(t, dir, "xdg-open", `printf 'argc=%s\n' "$#" >> `+marker+`; printf 'arg=%s\n' "$@" >> `+marker)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BROWSER", "echo hijacked;")

	link := authTestLink(40001, time.Minute)
	links := []netbroker.OAuthLink{link}
	var out, errOut bytes.Buffer

	if err := runAuthLinks(t.Context(), &out, &errOut, links, authOptions{}, authNow, hostOpenURL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("default invocation executed the opener")
	}
	if out.String() != link.URL+"\n" {
		t.Fatalf("stdout = %q", out.String())
	}

	out.Reset()
	if err := runAuthLinks(t.Context(), &out, &errOut, links, authOptions{open: true}, authNow, hostOpenURL); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker) //nolint:gosec // G304: test temp path
	if err != nil {
		t.Fatal(err)
	}
	if want := "argc=1\narg=" + link.URL + "\n"; string(got) != want {
		t.Fatalf("opener calls = %q, want %q", got, want)
	}
	if out.Len() != 0 {
		t.Fatalf("--open wrote to stdout: %q", out.String())
	}
}

func assertAuthErr(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want != "" && err == nil:
		t.Fatalf("error = nil, want containing %q", want)
	case want != "":
		assertContains(t, err.Error(), want)
	}
}
