//go:build linux

package guest

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestServeLoopbackRelay(t *testing.T) {
	t.Parallel()

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	targetPort := uint16(target.Addr().(*net.TCPAddr).Port) //nolint:gosec // G115: port fits in uint16

	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				req, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("loopback:"), req...))
			}()
		}
	}()

	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var d net.Dialer
	go serveLoopbackRelay(ctx, slog.New(slog.DiscardHandler), relay, net.ParseIP("127.0.0.1"), d.DialContext)

	t.Run("relays to listening loopback port", func(t *testing.T) {
		t.Parallel()
		c, err := net.Dial("tcp", relay.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))

		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], targetPort)
		if _, err := c.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
		var status [1]byte
		if _, err := io.ReadFull(c, status[:]); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status[0] != loopbackStatusOK {
			t.Fatalf("status = %d, want %d", status[0], loopbackStatusOK)
		}
		if _, err := c.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		if err := c.(*net.TCPConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(c)
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		if string(got) != "loopback:hello" {
			t.Fatalf("response = %q, want %q", got, "loopback:hello")
		}
	})

	t.Run("returns failure byte for closed port", func(t *testing.T) {
		t.Parallel()
		// Grab and immediately close a port so nothing listens on it.
		closed, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		closedPort := uint16(closed.Addr().(*net.TCPAddr).Port) //nolint:gosec // G115: port fits in uint16
		_ = closed.Close()

		c, err := net.Dial("tcp", relay.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))

		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], closedPort)
		if _, err := c.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
		var status [1]byte
		if _, err := io.ReadFull(c, status[:]); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status[0] != loopbackStatusFailed {
			t.Fatalf("status = %d, want %d", status[0], loopbackStatusFailed)
		}
	})

	t.Run("rejects port zero", func(t *testing.T) {
		t.Parallel()
		c, err := net.Dial("tcp", relay.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))

		if _, err := c.Write([]byte{0, 0}); err != nil {
			t.Fatal(err)
		}
		var status [1]byte
		if _, err := io.ReadFull(c, status[:]); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if status[0] != loopbackStatusFailed {
			t.Fatalf("status = %d, want %d", status[0], loopbackStatusFailed)
		}
	})
}

func TestServeLoopbackRelayRejectsUnauthorizedPeer(t *testing.T) {
	t.Parallel()
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = relay.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var d net.Dialer
	// Only 192.168.127.1 is allowed; dialing from 127.0.0.1 must be closed without status byte.
	go serveLoopbackRelay(ctx, slog.New(slog.DiscardHandler), relay, net.ParseIP("192.168.127.1"), d.DialContext)

	c, err := net.Dial("tcp", relay.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	var buf [1]byte
	n, err := c.Read(buf[:])
	if err == nil || n != 0 {
		t.Fatalf("expected immediate close for unauthorized peer, got n=%d err=%v", n, err)
	}
}
