package webgateway

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// RFC 6455 section 1.3 sample key and its Sec-WebSocket-Accept value, so the
// test upstream needs no SHA-1 to complete the handshake.
const (
	testSecWebSocketKey    = "dGhlIHNhbXBsZSBub25jZQ=="
	testSecWebSocketAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
)

// The test upstream speaks one real RFC 6455 masked text frame and echoes it.
func websocketEcho(w http.ResponseWriter, r *http.Request) {
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }() // the gateway may already have closed the hijacked connection
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return
	}
	if r.Header.Get("Sec-WebSocket-Key") != testSecWebSocketKey {
		return
	}
	_, err = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSet-Cookie: __Host-jailoc=forged; Secure; Path=/\r\n\r\n", testSecWebSocketAccept)
	if err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	frame := make([]byte, 8)
	if _, err := io.ReadFull(rw, frame); err != nil {
		return
	}
	if frame[0] != 0x81 || frame[1] != 0x82 {
		return
	}
	if _, err := rw.Write([]byte{0x81, 2, frame[6] ^ frame[2], frame[7] ^ frame[3]}); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	// Remain upgraded until gateway revokes the owning session.
	_, _ = io.Copy(io.Discard, rw)
}

func openWebsocket(t *testing.T, x *testGateway, cookie *http.Cookie) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", x.addr, &tls.Config{RootCAs: x.pool, ServerName: hostA, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() }) // the gateway may already have closed the connection
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nOrigin: https://%s\r\nCookie: %s=%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", hostA, hostA, cookie.Name, cookie.Value, testSecWebSocketKey)
	if err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(conn)
	res, err := http.ReadResponse(rd, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 101 || res.Header.Get("Sec-WebSocket-Accept") != testSecWebSocketAccept || len(res.Cookies()) != 0 {
		t.Fatalf("upgrade: %d %+v", res.StatusCode, res.Header)
	}
	if _, err := conn.Write([]byte{0x81, 0x82, 1, 2, 3, 4, 'h' ^ 1, 'i' ^ 2}); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4)
	if _, err := io.ReadFull(rd, frame); err != nil {
		t.Fatal(err)
	}
	if string(frame) != string([]byte{0x81, 2, 'h', 'i'}) {
		t.Fatalf("echo frame: %v", frame)
	}
	return conn, rd
}

func TestWebsocketRevocation(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"logout", "cancel", "expiry"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			x := startGateway(t, http.HandlerFunc(websocketEcho), func(g *Gateway) {
				if reason == "expiry" {
					g.sessionTTL = 400 * time.Millisecond
				}
			})
			cookie := loginCookie(t, x, hostA)
			conn, rd := openWebsocket(t, x, cookie)
			switch reason {
			case "logout":
				r := newRequest(t, "POST", hostA, "/.jailoc/logout")
				r.Header.Set("Origin", "https://"+hostA)
				r.AddCookie(cookie)
				request(t, x.client(t, hostA), r, 303)
			case "cancel":
				x.cancel()
			case "expiry": // The fixed deadline must actively close an idle upgrade.
			}
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := rd.ReadByte(); err == nil {
				t.Fatal("revoked websocket remains open")
			} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("websocket closure timed out")
			}
			if reason == "expiry" {
				r := newRequest(t, "GET", hostA, "/")
				r.AddCookie(cookie)
				request(t, x.client(t, hostA), r, 303)
			}
		})
	}
}

func TestUnsafeRequestIsNeverReplayed(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	x := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_ = conn.Close() // the connection is dropped on purpose; the close error is irrelevant
	}), nil)
	cookie := loginCookie(t, x, hostA)
	r := newRequest(t, "POST", hostA, "/action?secret=not-for-logs")
	r.Header.Set("Origin", "https://"+hostA)
	r.Header.Set("Idempotency-Key", "app-key")
	r.AddCookie(cookie)
	res := request(t, x.client(t, hostA), r, 502)
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || strings.Contains(string(body), "secret") || strings.Contains(string(body), "/action") {
		t.Fatalf("replayed or leaked request: calls=%d body=%q", calls.Load(), body)
	}
}

func TestLogoutClosesActiveHTTPConnection(t *testing.T) {
	t.Parallel()
	entered, closed := make(chan struct{}), make(chan struct{})
	x := startGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(closed)
	}), nil)
	cookie := loginCookie(t, x, hostA)
	r := newRequest(t, "GET", hostA, "/slow")
	r.AddCookie(cookie)
	client := x.client(t, hostA)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Only the return of the revoked request matters, not its outcome.
		if res, err := client.Do(r); err == nil {
			_ = res.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not reached")
	}
	r = newRequest(t, "POST", hostA, "/.jailoc/logout")
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://"+hostA)
	request(t, x.client(t, hostA), r, 303)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("logout left upstream HTTP connection open")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked HTTP request did not return")
	}
}
