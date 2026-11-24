package netbroker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	testLoopbackRelayPort = 7024
	testOAuthCallbackPort = 7025
)

// startFakeGuestLoopbackRelay serves the guest-side loopback relay protocol on
// g's GuestIP:testLoopbackRelayPort, routing each requested port to a handler
// registered in handlers.
func startFakeGuestLoopbackRelay(t *testing.T, g *stack.Stack, handlers map[uint16]func(net.Conn)) {
	t.Helper()
	ln, err := gonet.ListenTCP(g, tcpip.FullAddress{
		NIC:  1,
		Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 2}),
		Port: testLoopbackRelayPort,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("listen fake guest loopback relay: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				var hdr [2]byte
				if _, err := io.ReadFull(c, hdr[:]); err != nil {
					return
				}
				port := binary.BigEndian.Uint16(hdr[:])
				h, ok := handlers[port]
				if !ok {
					_, _ = c.Write([]byte{1})
					return
				}
				if _, err := c.Write([]byte{0}); err != nil {
					return
				}
				h(c)
			}()
		}
	}()
}

// registerFromGuest calls POST http://192.168.127.1:7025/register from inside
// the fake guest stack.
func registerFromGuest(t *testing.T, ctx context.Context, g *stack.Stack, port uint16, path string) int {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"port": port, "path": path})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
				return gonet.DialContextTCP(dctx, g, tcpip.FullAddress{
					NIC:  1,
					Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}),
					Port: testOAuthCallbackPort,
				}, ipv4.ProtocolNumber)
			},
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://192.168.127.1:%d/register", testOAuthCallbackPort), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("register from guest: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func startTestOAuthBroker(t *testing.T, ttl time.Duration, forwards ...Forward) (context.Context, *Broker, *stack.Stack, *syncBuf) {
	t.Helper()
	var logBuf syncBuf
	statusR, statusW := io.Pipe()
	t.Cleanup(func() {
		_ = statusR.Close()
		_ = statusW.Close()
	})
	b, err := New(Config{
		Subnet:            netip.MustParsePrefix("192.168.127.0/24"),
		Gateway:           netip.MustParseAddr("192.168.127.1"),
		GuestIP:           netip.MustParseAddr("192.168.127.2"),
		Forwards:          forwards,
		LoopbackRelayPort: testLoopbackRelayPort,
		OAuthCallbackPort: testOAuthCallbackPort,
		OAuthCallbackTTL:  ttl,
		Log:               &logBuf,
		Status:            statusW,
	}, DenyAll("test"))
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "nic.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Run(ctx, l) }()
	sc := bufio.NewScanner(statusR)
	if !sc.Scan() || sc.Text() != "ready" {
		t.Fatalf("broker did not report ready: %q (%v)", sc.Text(), sc.Err())
	}
	go func() {
		for sc.Scan() {
		}
	}()
	g := fakeGuest(t, ctx, sock)
	return ctx, b, g, &logBuf
}

func TestForwardLoopbackRelayFallback(t *testing.T) {
	t.Parallel()
	hostPort := freePort(t)
	guestPort := uint16(18123)

	ctx, _, g, _ := startTestOAuthBroker(t, time.Minute, Forward{
		Host:      netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), hostPort),
		GuestPort: guestPort,
	})

	// Nothing listens on 192.168.127.2:18123 directly; only the loopback relay
	// serves guestPort (simulating a guest app bound strictly to 127.0.0.1).
	startFakeGuestLoopbackRelay(t, g, map[uint16]func(net.Conn){
		guestPort: func(c net.Conn) {
			req := make([]byte, 4)
			_, _ = io.ReadFull(c, req)
			_, _ = c.Write(append([]byte("via-loopback:"), req...))
		},
	})

	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", hostPort))
	if err != nil {
		t.Fatalf("dial host forward: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(got) != "via-loopback:ping" {
		t.Fatalf("response = %q, want %q", got, "via-loopback:ping")
	}
}

func TestOAuthCallbackSingleShotAndFiltering(t *testing.T) {
	t.Parallel()
	ctx, _, g, logBuf := startTestOAuthBroker(t, time.Minute)
	port := freePort(t)
	const cbPath = "/mcp/oauth/callback"

	var guestHits atomic.Int32
	var receivedURL atomic.Value
	guestSrv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guestHits.Add(1)
			receivedURL.Store(r.URL.RequestURI())
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "login ok")
		}),
	}

	// Serve the guest HTTP callback server behind the loopback relay.
	chLn := newChanListener()
	t.Cleanup(func() { _ = chLn.Close() })
	go func() { _ = guestSrv.Serve(chLn) }()

	startFakeGuestLoopbackRelay(t, g, map[uint16]func(net.Conn){
		port: func(c net.Conn) {
			chLn.enqueue(c)
			// Keep handler alive until conn closes? No, Serve owns c now.
			// Block until c is closed by http.Server so defer c.Close() doesn't
			// close it prematurely.
			<-chLn.closed
		},
	})

	if code := registerFromGuest(t, ctx, g, port, cbPath); code != http.StatusOK {
		t.Fatalf("register status = %d, want 200", code)
	}

	hostClient := &http.Client{Timeout: 5 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// 1. Wrong path must return 404 without forwarding or consuming the slot.
	resp, err := hostClient.Get(base + "/favicon.ico")
	if err != nil {
		t.Fatalf("GET /favicon.ico: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong path status = %d, want 404", resp.StatusCode)
	}

	// 2. Non-GET method must return 405 without forwarding or consuming the slot.
	resp, err = hostClient.Post(base+cbPath+"?code=abc", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", resp.StatusCode)
	}

	// 3. Missing code/error query param must return 404 without forwarding.
	resp, err = hostClient.Get(base + cbPath + "?state=only")
	if err != nil {
		t.Fatalf("GET without code/error: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing code/error status = %d, want 404", resp.StatusCode)
	}

	// 4. Upgrade header must be rejected with 400 without forwarding.
	upReq, _ := http.NewRequest(http.MethodGet, base+cbPath+"?code=abc", nil)
	upReq.Header.Set("Upgrade", "websocket")
	resp, err = hostClient.Do(upReq)
	if err != nil {
		t.Fatalf("GET with Upgrade: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Upgrade status = %d, want 400", resp.StatusCode)
	}

	if hits := guestHits.Load(); hits != 0 {
		t.Fatalf("rejected requests reached guest %d times, want 0", hits)
	}

	// 5. Valid GET callback with code= must be forwarded once and return guest body.
	resp, err = hostClient.Get(base + cbPath + "?code=secret123&state=xyz")
	if err != nil {
		t.Fatalf("valid GET callback: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "login ok" {
		t.Fatalf("valid callback = %d %q, want 200 %q", resp.StatusCode, body, "login ok")
	}
	if got := receivedURL.Load(); got != cbPath+"?code=secret123&state=xyz" {
		t.Fatalf("guest received URI = %v, want %q", got, cbPath+"?code=secret123&state=xyz")
	}
	if hits := guestHits.Load(); hits != 1 {
		t.Fatalf("guest hits = %d, want 1", hits)
	}

	// 6. Host listener must be closed immediately afterwards.
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("host listener on port %d stayed open after single-shot callback", port)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(logBuf.String(), `"event":"oauth-callback"`) {
		t.Fatalf("expected oauth-callback event in broker log, got %s", logBuf.String())
	}
}

func TestOAuthCallbackValidationAndExpiry(t *testing.T) {
	t.Parallel()
	ctx, _, g, _ := startTestOAuthBroker(t, 150*time.Millisecond)

	// Privileged port rejected.
	if code := registerFromGuest(t, ctx, g, 80, "/cb"); code != http.StatusBadRequest {
		t.Fatalf("port 80 status = %d, want 400", code)
	}
	// Bad path rejected.
	if code := registerFromGuest(t, ctx, g, 19876, "no-leading-slash"); code != http.StatusBadRequest {
		t.Fatalf("bad path status = %d, want 400", code)
	}

	// Short TTL closes the host listener automatically.
	port := freePort(t)
	if code := registerFromGuest(t, ctx, g, port, "/cb"); code != http.StatusOK {
		t.Fatalf("register status = %d, want 200", code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("host listener on port %d did not expire", port)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestOAuthCallbackConcurrencyCap(t *testing.T) {
	t.Parallel()
	ctx, _, g, _ := startTestOAuthBroker(t, time.Minute)

	for range maxOAuthCallbacks {
		p := freePort(t)
		if code := registerFromGuest(t, ctx, g, p, "/cb"); code != http.StatusOK {
			t.Fatalf("register port %d status = %d, want 200", p, code)
		}
	}
	extra := freePort(t)
	if code := registerFromGuest(t, ctx, g, extra, "/cb"); code != http.StatusTooManyRequests {
		t.Fatalf("over-cap register status = %d, want 429", code)
	}
}

// chanListener feeds already-accepted connections to an http.Server in tests.
type chanListener struct {
	ch     chan net.Conn
	closed chan struct{}
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn, 4), closed: make(chan struct{})}
}

func (l *chanListener) enqueue(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.closed:
		_ = c.Close()
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}
