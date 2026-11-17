package netbroker

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// rulePostAny allows POST to example.com; an http rule makes the flow inspected.
const rulePostAny = `{"name":"post","names":["example.com"],"http":{"methods":["POST"]}}`

// goawayUpstream is an HTTPS upstream for example.com whose first HTTP/2
// connection reads one whole request and then sends a graceful GOAWAY
// (ErrCodeNo, last stream 0) without answering it, so the client has to resend
// the request on a new connection. Later connections are served by h. The
// framing is written by hand with x/net/http2, so the GOAWAY comes at the same
// point on every run.
type goawayUpstream struct {
	conns atomic.Int32
	first chan []byte // the body the first connection saw
}

func (e *l7Env) goawayUpstream(h http.Handler) *goawayUpstream {
	e.t.Helper()
	u := &goawayUpstream{first: make(chan []byte, 1)}
	cert := e.upCA.serverCert(e.t, "example.com")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(h)
	// The first connection is taken here, below the server's own TLS; the
	// server gets the others.
	ts.Listener = listenerFunc{Listener: l, accept: func() (net.Conn, error) {
		for {
			c, err := l.Accept()
			if err != nil {
				return nil, err
			}
			if u.conns.Add(1) > 1 {
				return c, nil
			}
			go func() {
				defer func() { _ = c.Close() }()
				tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12})
				if tc.Handshake() == nil {
					u.goaway(tc)
				}
			}()
		}
	}}
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ts.EnableHTTP2 = true
	quietServer(ts)
	ts.StartTLS()
	e.t.Cleanup(ts.Close)
	e.upstream = l.Addr().String()
	return u
}

// listenerFunc is a listener with its own Accept.
type listenerFunc struct {
	net.Listener
	accept func() (net.Conn, error)
}

func (l listenerFunc) Accept() (net.Conn, error) { return l.accept() }

func (u *goawayUpstream) goaway(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.CopyN(io.Discard, c, int64(len(http2.ClientPreface))); err != nil {
		return
	}
	fr := http2.NewFramer(c, c)
	if err := fr.WriteSettings(); err != nil {
		return
	}
	var body []byte
	sent := false
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			return
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = fr.WriteSettingsAck()
			}
		case *http2.DataFrame:
			body = append(body, f.Data()...)
			// Keep the client's flow-control windows open for large bodies.
			if f.Length > 0 && !f.StreamEnded() {
				_ = fr.WriteWindowUpdate(0, f.Length)
				_ = fr.WriteWindowUpdate(f.StreamID, f.Length)
			}
			if f.StreamEnded() && !sent {
				sent = true
				u.first <- body
				if err := fr.WriteGoAway(0, http2.ErrCodeNo, nil); err != nil {
					return
				}
			}
		}
	}
}

func TestInspectReplaysBodyAfterGoAway(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		guestH2 bool
		body    string
	}{
		{"http/1.1 guest", false, `{"query":"{ viewer { login } }"}`},
		{"h2 guest", true, `{"query":"{ viewer { login } }"}`},
		{"h2 guest, large body", true, strings.Repeat("x", maxReplayBody)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			var mu sync.Mutex
			var bodies []string
			u := e.goawayUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(b))
				mu.Unlock()
				_, _ = io.WriteString(w, "done")
			}))
			e.start(ruleWebHost+","+rulePostAny, "", true)

			resp, err := e.client(tc.guestH2, 443).Post("https://example.com/graphql", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
			if got := readBody(t, resp); got != "done" {
				t.Errorf("response body = %q, want done", got)
			}
			if first := <-u.first; string(first) != tc.body {
				t.Errorf("body on the connection that sent GOAWAY: %d bytes, want %d", len(first), len(tc.body))
			}
			mu.Lock()
			if len(bodies) != 1 || bodies[0] != tc.body {
				t.Errorf("resent request bodies = %d, want one of %d bytes", len(bodies), len(tc.body))
			}
			mu.Unlock()
			if n := u.conns.Load(); n != 2 {
				t.Errorf("upstream connections = %d, want 2", n)
			}
			if evs := e.find("l7", map[string]any{"status": float64(http.StatusBadGateway)}); len(evs) != 0 {
				t.Errorf("unexpected 502 events: %v", evs)
			}
			if evs := e.find("l7", map[string]any{"method": "POST", "status": float64(http.StatusOK)}); len(evs) != 1 {
				t.Errorf("200 events = %v, want one", evs)
			}
			deadline := time.Now().Add(5 * time.Second)
			for e.broker.replay.used.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if used := e.broker.replay.used.Load(); used != 0 {
				t.Errorf("replay budget in use after the request = %d, want 0", used)
			}
		})
	}
}

func TestInspectStreamsUnbufferedBodies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		guestH2 bool
		body    func() io.Reader
		size    int
	}{
		{"oversize", true, func() io.Reader { return strings.NewReader(strings.Repeat("y", maxReplayBody+1)) }, maxReplayBody + 1},
		{"unknown length http/1.1", false, func() io.Reader { return io.MultiReader(strings.NewReader("ab"), strings.NewReader("cd")) }, 4},
		{"unknown length h2", true, func() io.Reader { return io.MultiReader(strings.NewReader("ab"), strings.NewReader("cd")) }, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newL7Env(t)
			got := make(chan int, 1)
			e.httpsUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n, _ := io.Copy(io.Discard, r.Body)
				got <- int(n)
				_, _ = io.WriteString(w, "ok")
			}))
			e.start(ruleWebHost+","+rulePostAny, "", true)
			resp, err := e.client(tc.guestH2, 443).Post("https://example.com/up", "application/octet-stream", tc.body())
			if err != nil {
				t.Fatal(err)
			}
			if body := readBody(t, resp); body != "ok" || resp.StatusCode != http.StatusOK {
				t.Fatalf("response = %d %q", resp.StatusCode, body)
			}
			if n := <-got; n != tc.size {
				t.Errorf("upstream received %d bytes, want %d", n, tc.size)
			}
			if used := e.broker.replay.used.Load(); used != 0 {
				t.Errorf("replay budget in use = %d, want 0", used)
			}
		})
	}
}

func TestBufferBody(t *testing.T) {
	t.Parallel()
	newReq := func(body io.Reader, length int64, hdr map[string]string) *http.Request {
		r, err := http.NewRequest(http.MethodPost, "https://example.com/", body)
		if err != nil {
			t.Fatal(err)
		}
		// Like a server request: a one-shot body, no GetBody.
		r.Body, r.GetBody, r.ContentLength = io.NopCloser(body), nil, length
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	big := strings.Repeat("z", maxReplayBody+1)
	tests := []struct {
		name     string
		req      func() *http.Request
		buffered bool
		limit    int64 // budget already in use
	}{
		{"small", func() *http.Request { return newReq(strings.NewReader("hello"), 5, nil) }, true, 0},
		{"exactly the cap", func() *http.Request {
			return newReq(strings.NewReader(big[:maxReplayBody]), maxReplayBody, nil)
		}, true, 0},
		{"over the cap", func() *http.Request { return newReq(strings.NewReader(big), int64(len(big)), nil) }, false, 0},
		{"unknown length", func() *http.Request { return newReq(strings.NewReader("hello"), -1, nil) }, false, 0},
		{"no body", func() *http.Request {
			r := newReq(strings.NewReader(""), 0, nil)
			r.Body = http.NoBody
			return r
		}, false, 0},
		{"expect continue", func() *http.Request {
			return newReq(strings.NewReader("hello"), 5, map[string]string{"Expect": "100-Continue"})
		}, false, 0},
		{"upgrade", func() *http.Request {
			return newReq(strings.NewReader("hello"), 5, map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"})
		}, false, 0},
		{"budget exhausted", func() *http.Request { return newReq(strings.NewReader("hello"), 5, nil) }, false, maxReplayBudget - 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &inspectFlow{b: &Broker{}}
			f.b.replay.used.Store(tc.limit)
			r := tc.req()
			release, err := f.bufferBody(r)
			if err != nil {
				t.Fatal(err)
			}
			if (r.GetBody != nil) != tc.buffered {
				t.Fatalf("GetBody set = %v, want %v", r.GetBody != nil, tc.buffered)
			}
			if tc.buffered {
				if used := f.b.replay.used.Load(); used != r.ContentLength {
					t.Errorf("budget in use while buffered = %d, want %d", used, r.ContentLength)
				}
				want, _ := io.ReadAll(r.Body)
				for range 2 {
					rc, err := r.GetBody()
					if err != nil {
						t.Fatal(err)
					}
					if got, _ := io.ReadAll(rc); !bytes.Equal(got, want) || int64(len(got)) != r.ContentLength {
						t.Errorf("GetBody replayed %d bytes, want %d", len(got), r.ContentLength)
					}
				}
			}
			release()
			if used := f.b.replay.used.Load(); used != tc.limit {
				t.Errorf("budget in use after release = %d, want %d", used, tc.limit)
			}
		})
	}
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, errors.New("guest reset the stream")
	}
	n := min(r.n, len(p))
	for i := range n {
		p[i] = 'a'
	}
	r.n -= n
	return n, nil
}

func TestBufferBodyReadFailure(t *testing.T) {
	t.Parallel()
	f := &inspectFlow{b: &Broker{}}
	r, err := http.NewRequest(http.MethodPost, "https://example.com/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	r.Body, r.GetBody, r.ContentLength = io.NopCloser(&failingReader{n: 3}), nil, 10
	release, err := f.bufferBody(r)
	if err == nil || release != nil {
		t.Fatalf("bufferBody = (%v, %v), want an error and no release", release != nil, err)
	}
	if !strings.Contains(err.Error(), "read request body") {
		t.Errorf("error = %v, want read request body context", err)
	}
	if r.GetBody != nil {
		t.Error("GetBody set after a failed read")
	}
	if used := f.b.replay.used.Load(); used != 0 {
		t.Errorf("budget in use after a failed read = %d, want 0", used)
	}
}

func TestByteBudget(t *testing.T) {
	t.Parallel()
	var b byteBudget
	if !b.acquire(60, 100) {
		t.Fatal("acquire(60) of 100 failed")
	}
	if b.acquire(41, 100) {
		t.Fatal("acquire(41) succeeded with 60 of 100 in use")
	}
	if !b.acquire(40, 100) {
		t.Fatal("acquire(40) failed with 60 of 100 in use")
	}
	if b.acquire(1, 100) {
		t.Fatal("acquire(1) succeeded with the budget full")
	}
	b.release(60)
	if !b.acquire(60, 100) {
		t.Fatal("acquire(60) failed after release(60)")
	}
	b.release(100)
	if used := b.used.Load(); used != 0 {
		t.Fatalf("used = %d after releasing everything", used)
	}
}

func TestByteBudgetConcurrent(t *testing.T) {
	t.Parallel()
	var b byteBudget
	var wg sync.WaitGroup
	var over atomic.Bool
	for range 32 {
		wg.Go(func() {
			for range 500 {
				if b.acquire(10, 100) {
					if b.used.Load() > 100 {
						over.Store(true)
					}
					b.release(10)
				}
			}
		})
	}
	wg.Wait()
	if over.Load() {
		t.Error("budget exceeded its limit")
	}
	if used := b.used.Load(); used != 0 {
		t.Errorf("used = %d after all releases", used)
	}
}
