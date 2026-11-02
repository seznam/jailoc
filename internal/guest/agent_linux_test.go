package guest

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestServeRelayHalfClose(t *testing.T) {
	t.Parallel()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		req, _ := io.ReadAll(c)
		_, _ = c.Write(append([]byte("echo:"), req...))
	}()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go serveRelay(ctx, slog.New(slog.DiscardHandler), l, upstream.Addr().String(), "test relay")

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(got) != "echo:ping" {
		t.Fatalf("response = %q, want %q", got, "echo:ping")
	}
}
