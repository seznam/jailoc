package guest

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRewriteEntries(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "passwd")
	in := "root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000::/home/agent:/bin/bash\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	err := rewriteEntries(path, func(f []string) bool {
		if f[0] != "agent" {
			return false
		}
		f[2], f[3] = "1001", "118"
		return true
	})
	if err != nil {
		t.Fatalf("rewriteEntries: %v", err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(in, "agent:x:1000:1000:", "agent:x:1001:118:", 1)
	if string(got) != want {
		t.Fatalf("passwd = %q, want %q", got, want)
	}
	if err := rewriteEntries(filepath.Join(t.TempDir(), "missing"), func([]string) bool { return true }); err != nil {
		t.Fatalf("missing file: %v", err)
	}
}

func TestWakeListener(t *testing.T) {
	t.Parallel()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newWakeListener(raw)
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case <-ln.wake:
	case <-time.After(time.Second):
		t.Fatal("wakeListener did not signal wake on Accept")
	}

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("Accept did not return")
	}

	// Closing the listener closes tracked connections so srv.Close does not block.
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := client.Read(buf[:]); err == nil {
		t.Fatal("client connection still open after wakeListener.Close")
	}
	_ = serverConn.Close()
}
