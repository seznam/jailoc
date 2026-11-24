package netbroker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// Short socket path to stay under the 104-byte Unix socket sun_path limit on macOS.
	sockDir, err := os.MkdirTemp("", "nb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "nic.sock")
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
	var receivedHeaders atomic.Value
	guestSrv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guestHits.Add(1)
			receivedURL.Store(r.URL.RequestURI())
			receivedHeaders.Store(r.Header.Clone())
			w.Header().Set("Set-Cookie", "evil=1; Path=/")
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<script>alert(1)</script>")
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

	// 5. Valid GET callback with code= must be forwarded once, without host
	// browser credentials, and answered with the broker's own inert response.
	validReq, err := http.NewRequest(http.MethodGet, base+cbPath+"?code=secret123&state=xyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	validReq.Header.Set("Cookie", "sid=hostsecret")
	validReq.Header.Set("Authorization", "Bearer hosttok")
	validReq.Header.Set("Proxy-Authorization", "Basic abc")
	validReq.Header.Set("Origin", "http://localhost:3000")
	validReq.Header.Set("Referer", "http://localhost:3000/secret")
	resp, err = hostClient.Do(validReq)
	if err != nil {
		t.Fatalf("valid GET callback: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid callback status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(string(body), "<script>") || !strings.Contains(string(body), "Authentication complete") {
		t.Fatalf("valid callback body = %q, want the broker's fixed confirmation", body)
	}
	if v := resp.Header.Get("Set-Cookie"); v != "" {
		t.Fatalf("host response leaked guest Set-Cookie %q", v)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("host response Content-Type = %q, want text/plain", ct)
	}
	if v := resp.Header.Get("X-Content-Type-Options"); v != "nosniff" {
		t.Fatalf("host response X-Content-Type-Options = %q, want nosniff", v)
	}
	gh, _ := receivedHeaders.Load().(http.Header)
	for _, h := range []string{"Cookie", "Authorization", "Proxy-Authorization", "Origin", "Referer"} {
		if v := gh.Get(h); v != "" {
			t.Fatalf("guest received host header %s = %q, want empty", h, v)
		}
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

func TestOAuthRegisterRemappedForward(t *testing.T) {
	t.Parallel()

	t.Run("remapped forward is not an existing route", func(t *testing.T) {
		t.Parallel()
		port := freePort(t)
		ctx, _, g, _ := startTestOAuthBroker(t, time.Minute, Forward{
			Host:      netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port),
			GuestPort: 3000,
		})
		if code := registerFromGuest(t, ctx, g, port, "/cb"); code != http.StatusConflict {
			t.Fatalf("register status = %d, want 409", code)
		}
	})

	t.Run("matching forward is an existing route", func(t *testing.T) {
		t.Parallel()
		port := freePort(t)
		ctx, _, g, _ := startTestOAuthBroker(t, time.Minute, Forward{
			Host:      netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port),
			GuestPort: port,
		})
		if code := registerFromGuest(t, ctx, g, port, "/cb"); code != http.StatusOK {
			t.Fatalf("register status = %d, want 200", code)
		}
	})
}

func TestOAuthCallbackPrefersLoopbackRelay(t *testing.T) {
	t.Parallel()
	ctx, _, g, _ := startTestOAuthBroker(t, time.Minute)
	port := freePort(t)

	var nicHits, loopbackHits atomic.Int32
	nicLn, err := gonet.ListenTCP(g, tcpip.FullAddress{
		NIC:  1,
		Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 2}),
		Port: port,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("listen on fake guest NIC: %v", err)
	}
	t.Cleanup(func() { _ = nicLn.Close() })
	go func() {
		for {
			c, err := nicLn.Accept()
			if err != nil {
				return
			}
			nicHits.Add(1)
			_ = c.Close()
		}
	}()

	startFakeGuestLoopbackRelay(t, g, map[uint16]func(net.Conn){
		port: func(c net.Conn) {
			loopbackHits.Add(1)
			_, _ = http.ReadRequest(bufio.NewReader(c))
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		},
	})

	if code := registerFromGuest(t, ctx, g, port, "/cb"); code != http.StatusOK {
		t.Fatalf("register status = %d, want 200", code)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/cb?code=abc", port))
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}
	if got := loopbackHits.Load(); got != 1 {
		t.Fatalf("loopback relay hits = %d, want 1", got)
	}
	if got := nicHits.Load(); got != 0 {
		t.Fatalf("guest NIC hits = %d, want 0 (NIC listener must not shadow loopback)", got)
	}
}

func TestOAuthCallbackConcurrencyCap(t *testing.T) {
	t.Parallel()
	ctx, b, g, _ := startTestOAuthBroker(t, time.Minute)

	const n = 24
	ports := make([]uint16, n)
	for i := range ports {
		ports[i] = freePort(t)
	}
	codes := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = registerFromGuest(t, ctx, g, ports[i], "/cb")
		}()
	}
	close(start)
	wg.Wait()

	var okPorts []uint16
	var tooMany int
	for i, code := range codes {
		switch code {
		case http.StatusOK:
			okPorts = append(okPorts, ports[i])
		case http.StatusTooManyRequests:
			tooMany++
		default:
			t.Fatalf("register port %d status = %d, want 200 or 429", ports[i], code)
		}
	}
	if len(okPorts) != maxOAuthCallbacks || tooMany != n-maxOAuthCallbacks {
		t.Fatalf("200s = %d, 429s = %d, want %d and %d", len(okPorts), tooMany, maxOAuthCallbacks, n-maxOAuthCallbacks)
	}
	active := func() int {
		b.oauth.mu.Lock()
		defer b.oauth.mu.Unlock()
		return len(b.oauth.byPort)
	}
	if got := active(); got != maxOAuthCallbacks {
		t.Fatalf("active callbacks = %d, want %d", got, maxOAuthCallbacks)
	}

	// Re-registering an active port replaces it without growing the set.
	if code := registerFromGuest(t, ctx, g, okPorts[0], "/cb2"); code != http.StatusOK {
		t.Fatalf("re-register status = %d, want 200", code)
	}
	if got := active(); got != maxOAuthCallbacks {
		t.Fatalf("active callbacks after re-register = %d, want %d", got, maxOAuthCallbacks)
	}
}

func TestOAuthRegisterFlowLimitAndTimeouts(t *testing.T) {
	t.Parallel()

	dialRegister := func(t *testing.T, ctx context.Context, g *stack.Stack) net.Conn {
		t.Helper()
		c, err := gonet.DialContextTCP(ctx, g, tcpip.FullAddress{
			NIC:  1,
			Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}),
			Port: testOAuthCallbackPort,
		}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatalf("dial registration endpoint: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	t.Run("flow limit", func(t *testing.T) {
		t.Parallel()
		ctx, b, g, logBuf := startTestOAuthBroker(t, time.Minute)
		b.tcpFlows.n.Store(maxTCPFlows)
		t.Cleanup(func() { b.tcpFlows.n.Store(0) })

		c := dialRegister(t, ctx, g)
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(c, "POST /register HTTP/1.1\r\nHost: 192.168.127.1\r\nContent-Length: 2\r\n\r\n{}")
		got, err := io.ReadAll(c)
		if len(got) != 0 {
			t.Fatalf("registration served at flow limit: %q", got)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("connection stayed open at flow limit: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(logBuf.String(), `"event":"flow-limit"`) {
			if time.Now().After(deadline) {
				t.Fatalf("expected flow-limit event in broker log, got %s", logBuf.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("flow released on close", func(t *testing.T) {
		t.Parallel()
		ctx, b, g, _ := startTestOAuthBroker(t, time.Minute)
		if code := registerFromGuest(t, ctx, g, freePort(t), "/cb"); code != http.StatusOK {
			t.Fatalf("register status = %d, want 200", code)
		}
		deadline := time.Now().Add(2 * time.Second)
		for b.tcpFlows.n.Load() != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("tcp flows held after registration closed: %d", b.tcpFlows.n.Load())
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("body read timeout", func(t *testing.T) {
		t.Parallel()
		ctx, b, g, _ := startTestOAuthBroker(t, time.Minute)
		b.oauth.registerTimeout.Store(int64(100 * time.Millisecond))

		c := dialRegister(t, ctx, g)
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.WriteString(c, "POST /register HTTP/1.1\r\nHost: 192.168.127.1:7025\r\nContent-Length: 10\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		// The body never arrives; the server must give up and close.
		_, err := io.ReadAll(c)
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("server held a stalled registration open: %v", err)
		}
	})
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
