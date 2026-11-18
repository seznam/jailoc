package vm

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// webStateDir returns a short state directory: unix socket paths are limited
// to about 104 bytes.
func webStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeWebRelay listens on the web socket and runs handler for each connection.
func fakeWebRelay(t *testing.T, stateDir string, handler func(net.Conn)) {
	t.Helper()
	l, err := net.Listen("unix", WebSocket(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				handler(c)
			}()
		}
	}()
}

func TestWebSocketPath(t *testing.T) {
	t.Parallel()
	if got, want := WebSocket("/state"), filepath.Join("/state", "web.sock"); got != want {
		t.Fatalf("WebSocket = %q, want %q", got, want)
	}
}

func TestDialWebConnected(t *testing.T) {
	t.Parallel()
	dir := webStateDir(t)
	gotPort := make(chan uint16, 1)
	fakeWebRelay(t, dir, func(c net.Conn) {
		var hdr [2]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return
		}
		gotPort <- binary.BigEndian.Uint16(hdr[:])
		_, _ = c.Write([]byte{0})
		_, _ = io.Copy(c, c)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialWeb(ctx, dir, 6080)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if p := <-gotPort; p != 6080 {
		t.Fatalf("server saw port %d", p)
	}
	// The handshake deadline must be cleared before the stream is returned.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
}

func TestDialWebClearsHandshakeDeadline(t *testing.T) {
	t.Parallel()
	dir := webStateDir(t)
	fakeWebRelay(t, dir, func(c net.Conn) {
		var hdr [2]byte
		_, _ = io.ReadFull(c, hdr[:])
		_, _ = c.Write([]byte{0})
		time.Sleep(300 * time.Millisecond)
		_, _ = c.Write([]byte("late"))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	conn, err := DialWeb(ctx, dir, 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// The ctx deadline passed while the server was idle; the stream must
	// still work because the deadline only bounds the handshake.
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "late" {
		t.Fatalf("read after ctx deadline = %q, %v", buf, err)
	}
}

func TestDialWebStatuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reply   []byte
		wantErr error
		wantMsg string
	}{
		{"denied", []byte{1}, ErrWebDenied, "denied"},
		{"unavailable", []byte{2}, ErrWebUnavailable, "unavailable"},
		{"unknown status", []byte{7}, nil, "unknown status 7"},
		{"truncated", nil, nil, "truncated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := webStateDir(t)
			fakeWebRelay(t, dir, func(c net.Conn) {
				var hdr [2]byte
				_, _ = io.ReadFull(c, hdr[:])
				_, _ = c.Write(tt.reply)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := DialWeb(ctx, dir, 8080)
			if err == nil {
				_ = conn.Close()
				t.Fatal("expected error")
			}
			if conn != nil {
				t.Fatal("non-nil conn with error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("err = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestDialWebNoSocket(t *testing.T) {
	t.Parallel()
	if _, err := DialWeb(context.Background(), webStateDir(t), 8080); err == nil {
		t.Fatal("expected error without socket")
	}
}

func TestDialWebZeroPort(t *testing.T) {
	t.Parallel()
	if _, err := DialWeb(context.Background(), webStateDir(t), 0); err == nil {
		t.Fatal("port 0 accepted")
	}
}

func TestDialWebContextCancelDuringHandshake(t *testing.T) {
	t.Parallel()
	dir := webStateDir(t)
	fakeWebRelay(t, dir, func(c net.Conn) {
		_, _ = io.Copy(io.Discard, c) // never answers
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		conn, err := DialWeb(ctx, dir, 8080)
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DialWeb ignored cancellation")
	}
}

func TestDialWebContextDeadlineDuringHandshake(t *testing.T) {
	t.Parallel()
	dir := webStateDir(t)
	fakeWebRelay(t, dir, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := DialWeb(ctx, dir, 8080)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestDialWebCancelAfterReturnKeepsConn(t *testing.T) {
	t.Parallel()
	dir := webStateDir(t)
	fakeWebRelay(t, dir, func(c net.Conn) {
		var hdr [2]byte
		_, _ = io.ReadFull(c, hdr[:])
		_, _ = c.Write([]byte{0})
		_, _ = io.Copy(c, c)
	})
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := DialWeb(ctx, dir, 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// net/http cancels the dial context once the dial returned; a pooled
	// connection must survive that.
	cancel()
	time.Sleep(50 * time.Millisecond)
	if _, err := conn.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
}
