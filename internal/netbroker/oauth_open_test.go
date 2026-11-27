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
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// wantBrowserErrBody is the only text a guest ever gets for a failed open.
const wantBrowserErrBody = "could not open the host browser\n"

// fakeOpener records every URL handed to Config.OpenURL and answers through
// fn (nil accepts).
type fakeOpener struct {
	mu   sync.Mutex
	urls []string
	fn   func(ctx context.Context, rawURL string) error
}

func (o *fakeOpener) open(ctx context.Context, rawURL string) error {
	o.mu.Lock()
	o.urls = append(o.urls, rawURL)
	fn := o.fn
	o.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, rawURL)
}

func (o *fakeOpener) calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.urls...)
}

// registerBody is register that also returns the response body.
func (e *pendingEnv) registerBody(t *testing.T, port uint16, path, authURL string) (int, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"port": port, "path": path, "url": authURL})
	if err != nil {
		t.Fatal(err)
	}
	return registerPayloadFromGuestBody(t, e.ctx, e.g, payload)
}

func assertNoSecret(t *testing.T, what, body string) {
	t.Helper()
	if strings.Contains(body, pendingSecretMark) {
		t.Fatalf("%s leaks the authorization URL: %q", what, body)
	}
}

func TestOAuthRegisterNilOpenerAnswers502AndKeepsLinkPending(t *testing.T) {
	t.Parallel()
	e := startPendingEnvOpen(t, time.Minute, nil)
	port := freePort(t)
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))

	code, body := e.registerBody(t, port, "/cb", raw)
	if code != http.StatusBadGateway {
		t.Fatalf("register status = %d, want 502", code)
	}
	if body != wantBrowserErrBody {
		t.Fatalf("body = %q, want the generic %q", body, wantBrowserErrBody)
	}
	if e.callbackOf(port) == nil {
		t.Fatal("callback was not kept after the open failed")
	}
	links := getPending(t, e.sock)
	if len(links) != 1 || links[0].URL != raw {
		t.Fatalf("links = %+v, want the one link kept pending", links)
	}
}

func TestOAuthRegisterWithoutURLNeverOpens(t *testing.T) {
	t.Parallel()
	op := &fakeOpener{}
	e := startPendingEnvOpen(t, time.Minute, op.open)
	port := freePort(t)

	// An older guest omits "url"; a newer one may send it empty. Both keep the
	// plain registration: 200, a callback, no link and no browser.
	for _, payload := range []string{
		fmt.Sprintf(`{"port":%d,"path":"/cb"}`, port),
		fmt.Sprintf(`{"port":%d,"path":"/cb","url":""}`, port),
	} {
		code, body := registerPayloadFromGuestBody(t, e.ctx, e.g, []byte(payload))
		if code != http.StatusOK {
			t.Fatalf("register %s status = %d (%q), want 200", payload, code, body)
		}
		if e.callbackOf(port) == nil {
			t.Fatalf("register %s did not open the callback", payload)
		}
		if links := getPending(t, e.sock); len(links) != 0 {
			t.Fatalf("links = %v, want none without a url", links)
		}
	}
	if got := op.calls(); len(got) != 0 {
		t.Fatalf("opener called for registrations without a url: %d calls", len(got))
	}
}

func TestOAuthRegisterMalformedURLNeverOpens(t *testing.T) {
	t.Parallel()
	op := &fakeOpener{}
	e := startPendingEnvOpen(t, time.Minute, op.open)
	port := freePort(t)
	other := loopbackRedirect("localhost", port+1, "/cb")
	cases := []struct{ name, url string }{
		{"not a url", "SECRETMARK not a url"},
		{"redirect to another port", pendingAuthURL(other)},
		{"no redirect_uri", "https://claude.ai/x?state=" + pendingSecretMark},
		{"ftp scheme", "ftp://claude.ai/x?state=" + pendingSecretMark + "&redirect_uri=" + loopbackRedirect("localhost", port, "/cb")},
		{"fragment", pendingAuthURL(loopbackRedirect("localhost", port, "/cb")) + "#" + pendingSecretMark},
	}
	for _, tc := range cases {
		code, body := e.registerBody(t, port, "/cb", tc.url)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, code)
		}
		assertNoSecret(t, tc.name+" body", body)
		if e.callbackOf(port) != nil {
			t.Fatalf("%s: created a callback", tc.name)
		}
	}
	if got := op.calls(); len(got) != 0 {
		t.Fatalf("opener called for an invalid url: %d calls", len(got))
	}
}

func TestOAuthRegisterBindFailureNeverOpens(t *testing.T) {
	t.Parallel()
	op := &fakeOpener{}
	e := startPendingEnvOpen(t, time.Minute, op.open)
	port := freePort(t)
	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("occupy host port: %v", err)
	}
	t.Cleanup(func() { _ = busy.Close() })

	code, body := e.registerBody(t, port, "/cb", pendingAuthURL(loopbackRedirect("localhost", port, "/cb")))
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
	assertNoSecret(t, "bind failure body", body)
	if got := op.calls(); len(got) != 0 {
		t.Fatalf("opener called although the callback could not bind: %d calls", len(got))
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %v, want none", links)
	}
}

func TestOAuthRegisterOpensOnceAfterCallbackIsReady(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	dialed := make(chan error, 1)
	op := &fakeOpener{fn: func(_ context.Context, _ string) error {
		// The browser may finish the sign-in at once: the callback listener
		// must already accept before the opener runs.
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
		if err == nil {
			_ = c.Close()
		}
		dialed <- err
		return nil
	}}
	e := startPendingEnvOpen(t, time.Minute, op.open)
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/mcp/cb")) + "&a=%7e&&x="

	code, body := e.registerBody(t, port, "/mcp/cb", raw)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if body != "" {
		t.Fatalf("202 body = %q, want empty", body)
	}
	if got := op.calls(); !reflect.DeepEqual(got, []string{raw}) {
		t.Fatalf("opener calls = %q, want exactly the registered URL once", got)
	}
	if err := <-dialed; err != nil {
		t.Fatalf("callback not ready when the opener ran: %v", err)
	}
	// Opening does not spend the link: it stays pending until claimed or expired.
	if links := getPending(t, e.sock); len(links) != 1 || links[0].URL != raw {
		t.Fatalf("links = %+v, want the opened link still pending", links)
	}
}

func TestOAuthRegisterConfiguredForwardStillOpens(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	op := &fakeOpener{}
	e := startPendingEnvOpen(t, time.Minute, op.open, Forward{
		Host:      netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port),
		GuestPort: port,
	})
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))

	code, body := e.registerBody(t, port, "/cb", raw)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d (%q), want 202", code, body)
	}
	if got := op.calls(); !reflect.DeepEqual(got, []string{raw}) {
		t.Fatalf("opener calls = %q, want the URL once", got)
	}
	if e.callbackOf(port) != nil {
		t.Fatal("a callback was bound next to the configured forward")
	}
	if links := getPending(t, e.sock); len(links) != 0 {
		t.Fatalf("links = %v, want no link for a forwarded port", links)
	}

	// Without a URL the configured forward keeps its plain 200 and no browser.
	if code := e.register(t, port, "/cb", ""); code != http.StatusOK {
		t.Fatalf("no-url status = %d, want 200", code)
	}
	if got := op.calls(); len(got) != 1 {
		t.Fatalf("opener calls = %d after a no-url registration, want 1", len(got))
	}
}

func TestOAuthRegisterConfiguredForwardNilOpener502(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	e := startPendingEnvOpen(t, time.Minute, nil, Forward{
		Host:      netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port),
		GuestPort: port,
	})
	code, body := e.registerBody(t, port, "/cb", pendingAuthURL(loopbackRedirect("localhost", port, "/cb")))
	if code != http.StatusBadGateway || body != wantBrowserErrBody {
		t.Fatalf("status = %d body = %q, want 502 and the generic body", code, body)
	}
}

func TestOAuthRegisterOpenFailureKeepsPendingAndRetrySucceeds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  func(rawURL string) error
	}{
		{"failure carrying the url", func(u string) error { return fmt.Errorf("opener failed for %s", u) }},
		{"capacity", func(string) error { return errors.New("too many browser openers are still running") }},
		{"canceled", func(string) error { return context.Canceled }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			fail := true
			op := &fakeOpener{}
			op.fn = func(_ context.Context, u string) error {
				mu.Lock()
				defer mu.Unlock()
				if fail {
					return tc.err(u)
				}
				return nil
			}
			e := startPendingEnvOpen(t, time.Minute, op.open)
			port := freePort(t)
			raw := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))

			code, body := e.registerBody(t, port, "/cb", raw)
			if code != http.StatusBadGateway || body != wantBrowserErrBody {
				t.Fatalf("status = %d body = %q, want 502 and the generic body", code, body)
			}
			assertNoSecret(t, "failure body", body)
			first := e.callbackOf(port)
			if first == nil || first.served.Load() {
				t.Fatal("callback not retained after the failed open")
			}
			if links := getPending(t, e.sock); len(links) != 1 || links[0].URL != raw {
				t.Fatalf("links = %+v, want the link kept pending for the CLI", links)
			}

			mu.Lock()
			fail = false
			mu.Unlock()
			if code, body := e.registerBody(t, port, "/cb", raw); code != http.StatusAccepted || body != "" {
				t.Fatalf("retry status = %d body = %q, want 202 and empty", code, body)
			}
			if got := op.calls(); len(got) != 2 {
				t.Fatalf("opener calls = %d, want 2 (failed + retry)", len(got))
			}
			if links := getPending(t, e.sock); len(links) != 1 || links[0].URL != raw {
				t.Fatalf("links after retry = %+v, want one", links)
			}
		})
	}
}

// directRegister posts a URL-bearing registration straight to the handler with
// the given broker and request contexts.
func directRegister(b *Broker, bctx, rctx context.Context, port uint16, rawURL string) *deadlineRecorder {
	payload, _ := json.Marshal(map[string]any{"port": port, "path": "/cb", "url": rawURL})
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(string(payload))).WithContext(rctx)
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	b.handleOAuthRegister(bctx, rec, req)
	return rec
}

// deadlineRecorder lets http.ResponseController set the read deadline the
// handler asks for.
type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (*deadlineRecorder) SetReadDeadline(time.Time) error { return nil }

func TestOAuthRegisterShutdownContext(t *testing.T) {
	t.Parallel()
	// Each subtest owns its forwarded host port: they run in parallel.
	forwarded := func(t *testing.T) (uint16, Forward, string) {
		t.Helper()
		port := freePort(t)
		return port, Forward{Host: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port), GuestPort: port},
			pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))
	}

	t.Run("stopped before the call", func(t *testing.T) {
		t.Parallel()
		port, forward, raw := forwarded(t)
		op := &fakeOpener{}
		e := startPendingEnvOpen(t, time.Minute, op.open, forward)
		bctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := directRegister(e.b, bctx, context.Background(), port, raw)
		if rec.Code != http.StatusBadGateway || rec.Body.String() != wantBrowserErrBody {
			t.Fatalf("status = %d body = %q, want 502 and the generic body", rec.Code, rec.Body.String())
		}
		if got := op.calls(); len(got) != 0 {
			t.Fatalf("opener called after shutdown: %d calls", len(got))
		}
	})

	for _, tc := range []string{"broker", "request"} {
		t.Run("canceled in flight by "+tc, func(t *testing.T) {
			t.Parallel()
			port, forward, raw := forwarded(t)
			started := make(chan struct{})
			op := &fakeOpener{fn: func(ctx context.Context, _ string) error {
				close(started)
				<-ctx.Done()
				return fmt.Errorf("blocked opener: %w", ctx.Err())
			}}
			e := startPendingEnvOpen(t, time.Minute, op.open, forward)
			bctx, bcancel := context.WithCancel(context.Background())
			defer bcancel()
			rctx, rcancel := context.WithCancel(context.Background())
			defer rcancel()
			done := make(chan *deadlineRecorder, 1)
			go func() { done <- directRegister(e.b, bctx, rctx, port, raw) }()
			<-started
			if tc == "broker" {
				bcancel()
			} else {
				rcancel()
			}
			select {
			case rec := <-done:
				if rec.Code != http.StatusBadGateway || rec.Body.String() != wantBrowserErrBody {
					t.Fatalf("status = %d body = %q, want 502 and the generic body", rec.Code, rec.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not return after its context ended")
			}
		})
	}
}

func TestOAuthRegisterBlockedOpenerHoldsNoLocks(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	started := make(chan struct{})
	release := make(chan struct{})
	op := &fakeOpener{fn: func(context.Context, string) error {
		close(started)
		<-release
		return nil
	}}
	e := startPendingEnvOpen(t, time.Minute, op.open)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	guestHit := make(chan struct{})
	startFakeGuestLoopbackRelay(t, e.g, map[uint16]func(net.Conn){
		port: func(c net.Conn) {
			_, _ = http.ReadRequest(bufio.NewReader(c))
			close(guestHit)
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		},
	})
	raw := pendingAuthURL(loopbackRedirect("localhost", port, "/cb"))

	registered := make(chan int, 1)
	go func() {
		code, _ := e.registerBody(t, port, "/cb", raw)
		registered <- code
	}()
	<-started

	// The opener is blocked mid-registration. A reload, the pending list and
	// the callback itself must all go through meanwhile.
	reloaded := make(chan []error, 1)
	go func() { reloaded <- e.b.ApplyRuntime(e.ctx, &Runtime{}) }()
	select {
	case errs := <-reloaded:
		if len(errs) != 0 {
			t.Fatalf("ApplyRuntime errors = %v", errs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ApplyRuntime blocked behind the browser opener")
	}
	if links := getPending(t, e.sock); len(links) != 1 || links[0].URL != raw {
		t.Fatalf("links = %+v, want the link listed while the opener blocks", links)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/cb?code=abc", port))
	if err != nil {
		t.Fatalf("callback during blocked opener: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}
	select {
	case <-guestHit:
	case <-time.After(5 * time.Second):
		t.Fatal("callback never reached the guest")
	}

	select {
	case code := <-registered:
		t.Fatalf("registration answered %d before the opener returned", code)
	default:
	}
	close(release)
	select {
	case code := <-registered:
		if code != http.StatusAccepted {
			t.Fatalf("register status = %d, want 202", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not finish after the opener returned")
	}
}
