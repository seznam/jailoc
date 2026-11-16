package netbroker

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// allowOnly returns an enforceConn check that allows only name.
func allowOnly(name string) func(string) Decision {
	return func(peer string) Decision {
		if peer == name {
			return Decision{Allow: true, Reason: "rule web", Rule: "web"}
		}
		return Decision{Reason: "rule no-paste", Rule: "no-paste"}
	}
}

func TestDenyReply(t *testing.T) {
	t.Parallel()
	hello := []byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01}
	denied := Decision{Reason: "rule no-paste", Rule: "no-paste"}
	tests := []struct {
		name string
		head []byte
		info map[string]any
		d    Decision
		want string
	}{
		{
			name: "tls alert keeps the client record version",
			head: hello,
			info: map[string]any{"proto": "tls", "sni": "pastebin.com"},
			d:    denied,
			want: "\x15\x03\x01\x00\x02\x02\x31",
		},
		{
			name: "tls info without a tls record resets",
			head: []byte("GET /"),
			info: map[string]any{"proto": "tls"},
			d:    denied,
		},
		{
			name: "http names the rule",
			info: map[string]any{"proto": "http", "method": "GET", "host": "pastebin.com"},
			d:    denied,
			want: "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 42\r\nConnection: close\r\n\r\n" +
				"blocked by network policy (rule=no-paste)\n",
		},
		{
			name: "http without a rule names the reason",
			info: map[string]any{"proto": "http", "method": "POST"},
			d:    Decision{Reason: "sni-required"},
			want: "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 48\r\nConnection: close\r\n\r\n" +
				"blocked by network policy (reason=sni-required)\n",
		},
		{
			name: "head request gets no body",
			info: map[string]any{"proto": "http", "method": "HEAD"},
			d:    denied,
			want: "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 42\r\nConnection: close\r\n\r\n",
		},
		{
			name: "rule name cannot break the response",
			info: map[string]any{"proto": "http", "method": "GET"},
			d:    Decision{Rule: "a\r\nX-Evil: 1\xff" + strings.Repeat("b", 80)},
			want: "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 98\r\nConnection: close\r\n\r\n" +
				"blocked by network policy (rule=a??X-Evil: 1?" + strings.Repeat("b", 51) + ")\n",
		},
		{
			name: "malformed http request resets",
			info: map[string]any{"proto": "http", "method": "GET", "bad_authority": true},
			d:    Decision{Reason: "sni-mismatch"},
		},
		{
			name: "unidentified stream resets",
			head: []byte("SSH-2.0-OpenSSH\r\n"),
			d:    denied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := denyReply(tt.head, tt.info, tt.d)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("denyReply = %q, want nil (reset)", got)
				}
				return
			}
			if string(got) != tt.want {
				t.Fatalf("denyReply =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestEnforceConnDenyReply(t *testing.T) {
	t.Parallel()
	const deniedReq = "GET / HTTP/1.1\r\nHost: pastebin.com\r\n\r\n"

	// run sends guestData from the guest after the upstream wrote upstreamData
	// to it, and returns what the guest received and whether the broker reset
	// the connection or closed the upstream for a reply.
	run := func(t *testing.T, upstreamData, guestData string) (got []byte, aborted, closedUpstream bool, readErr error) {
		t.Helper()
		guest, broker := net.Pipe()
		t.Cleanup(func() { _ = guest.Close() })
		var abortedFlag, closedFlag atomic.Bool
		ec := &enforceConn{
			Conn:          broker,
			check:         allowOnly("allowed.example.com"),
			closeUpstream: func() { closedFlag.Store(true) },
			abort: func() {
				abortedFlag.Store(true)
				_ = broker.Close()
			},
		}
		_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
		recv := make(chan []byte, 1)
		go func() {
			if upstreamData != "" {
				b := make([]byte, len(upstreamData))
				_, _ = io.ReadFull(guest, b)
			}
			_, _ = guest.Write([]byte(guestData))
			all, _ := io.ReadAll(guest)
			recv <- all
		}()
		if upstreamData != "" {
			if _, err := ec.Write([]byte(upstreamData)); err != nil {
				t.Fatalf("upstream write: %v", err)
			}
		}
		_, readErr = ec.Read(make([]byte, 4096))
		select {
		case got = <-recv:
		case <-time.After(5 * time.Second):
			t.Fatal("guest did not see the connection close")
		}
		return got, abortedFlag.Load(), closedFlag.Load(), readErr
	}

	t.Run("denied first request gets a 403", func(t *testing.T) {
		t.Parallel()
		got, aborted, closedUpstream, err := run(t, "", deniedReq)
		if !errors.Is(err, errPolicyDenied) || aborted || !closedUpstream {
			t.Fatalf("Read err=%v aborted=%v closedUpstream=%v, want denied reply", err, aborted, closedUpstream)
		}
		if !bytes.HasPrefix(got, []byte("HTTP/1.1 403 Forbidden\r\n")) || !bytes.Contains(got, []byte("(rule=no-paste)")) {
			t.Fatalf("guest got %q, want 403 naming rule no-paste", got)
		}
	})

	t.Run("denied request after upstream data resets", func(t *testing.T) {
		t.Parallel()
		got, aborted, closedUpstream, err := run(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n", deniedReq)
		if !errors.Is(err, errPolicyDenied) || !aborted || closedUpstream {
			t.Fatalf("Read err=%v aborted=%v closedUpstream=%v, want reset", err, aborted, closedUpstream)
		}
		if len(got) != 0 {
			t.Fatalf("guest got %q after reset, want nothing", got)
		}
	})

	resets := []struct{ name, guestData string }{
		{"allowed upgrade resets", "GET / HTTP/1.1\r\nHost: allowed.example.com\r\nUpgrade: websocket\r\n\r\n"},
		{"duplicate host resets", "GET / HTTP/1.1\r\nHost: allowed.example.com\r\nHost: pastebin.com\r\n\r\n"},
		{"invalid content-length resets", "POST / HTTP/1.1\r\nHost: pastebin.com\r\nContent-Length: x\r\n\r\n"},
		// The allowed GET is forwarded upstream and has no response yet; a 403
		// would be read as its response.
		{"denied request pipelined after an allowed one resets", "GET / HTTP/1.1\r\nHost: allowed.example.com\r\n\r\nHEAD / HTTP/1.1\r\nHost: pastebin.com\r\n\r\n"},
	}
	for _, tt := range resets {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, aborted, closedUpstream, err := run(t, "", tt.guestData)
			if !errors.Is(err, errPolicyDenied) || !aborted || closedUpstream || len(got) != 0 {
				t.Fatalf("Read err=%v aborted=%v closedUpstream=%v got=%q, want reset", err, aborted, closedUpstream, got)
			}
		})
	}

	t.Run("upgrade with a denied host gets a 403", func(t *testing.T) {
		t.Parallel()
		got, aborted, _, err := run(t, "", "GET / HTTP/1.1\r\nHost: pastebin.com\r\nUpgrade: websocket\r\n\r\n")
		if !errors.Is(err, errPolicyDenied) || aborted || !bytes.HasPrefix(got, []byte("HTTP/1.1 403 Forbidden\r\n")) {
			t.Fatalf("Read err=%v aborted=%v got=%q, want 403", err, aborted, got)
		}
	})

	t.Run("writes after a denial fail", func(t *testing.T) {
		t.Parallel()
		ec := &enforceConn{denied: true}
		if _, err := ec.Write([]byte("late upstream data")); !errors.Is(err, errPolicyDenied) {
			t.Fatalf("Write after denial = %v, want errPolicyDenied", err)
		}
		if err := ec.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite after denial = %v, want nil", err)
		}
	})
}

// TestReplyAndCloseDrainsLargeUpload checks that a client still uploading a
// large denied request body finishes the upload and reads the 403, instead of
// a reset or broken pipe from a close with unread data.
func TestReplyAndCloseDrainsLargeUpload(t *testing.T) {
	t.Parallel()
	const bodySize = 16 << 20
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	reply := http403(map[string]any{"method": "POST"}, Decision{Rule: "no-paste"})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		// The broker decides on the request head, before the body arrives.
		var head []byte
		for b := make([]byte, 1); !bytes.HasSuffix(head, []byte("\r\n\r\n")); head = append(head, b[0]) {
			if _, err := c.Read(b); err != nil {
				_ = c.Close()
				return
			}
		}
		replyAndClose(c, reply)
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	head := "POST /upload HTTP/1.1\r\nHost: pastebin.com\r\nContent-Length: " + strconv.Itoa(bodySize) + "\r\n\r\n"
	if _, err := c.Write([]byte(head)); err != nil {
		t.Fatalf("write head: %v", err)
	}
	chunk := make([]byte, 64<<10)
	for sent := 0; sent < bodySize; sent += len(chunk) {
		if _, err := c.Write(chunk); err != nil {
			t.Fatalf("upload failed after %d of %d body bytes: %v", sent, bodySize, err)
		}
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read reply: %v (got %q)", err, got)
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("client got %q, want the 403", got)
	}
}
