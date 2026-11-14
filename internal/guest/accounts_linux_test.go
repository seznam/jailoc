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
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("client connection timed out instead of closing after wakeListener.Close")
	}
	_ = serverConn.Close()
}

func TestEnsureAccount(t *testing.T) {
	t.Parallel()
	const imagePasswd = "root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000::/home/agent:/bin/bash\n" //nolint:gosec // G101: passwd fixture, no credentials
	const imageGroup = "root:x:0:\nagent:x:1000:\n"
	tests := []struct {
		name       string
		cfg        Config
		passwd     string
		wantGID    uint32
		wantPasswd string
		wantGroup  string
	}{
		{
			name:       "image entry keeps its GID",
			cfg:        Config{User: "agent", UID: 1000, GID: 1002, Home: "/home/agent"},
			passwd:     imagePasswd,
			wantGID:    1000,
			wantPasswd: imagePasswd,
			wantGroup:  imageGroup,
		},
		{ //nolint:gosec // G101: passwd fixture, no credentials
			name:       "host IDs replace the image GID",
			cfg:        Config{User: "agent", UID: 1000, GID: 1002, Home: "/home/agent", hostIDs: true},
			passwd:     imagePasswd,
			wantGID:    1002,
			wantPasswd: "root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1002::/home/agent:/bin/bash\n",
			wantGroup:  "root:x:0:\nagent:x:1002:\n",
		},
		{
			name:       "host IDs matching the image leave files alone",
			cfg:        Config{User: "agent", UID: 1000, GID: 1000, Home: "/home/agent", hostIDs: true},
			passwd:     imagePasswd,
			wantGID:    1000,
			wantPasswd: imagePasswd,
			wantGroup:  imageGroup,
		},
		{ //nolint:gosec // G101: passwd fixture, no credentials
			name:       "named entry is renumbered",
			cfg:        Config{User: "agent", UID: 1001, GID: 1002, Home: "/home/agent"},
			passwd:     imagePasswd,
			wantGID:    1002,
			wantPasswd: "root:x:0:0:root:/root:/bin/bash\nagent:x:1001:1002::/home/agent:/bin/bash\n",
			wantGroup:  "root:x:0:\nagent:x:1002:\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			etc := t.TempDir()
			if err := os.WriteFile(filepath.Join(etc, "passwd"), []byte(tt.passwd), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(etc, "group"), []byte(imageGroup), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := tt.cfg
			acct, err := ensureAccount(&cfg, etc)
			if err != nil {
				t.Fatalf("ensureAccount: %v", err)
			}
			if acct.name != "agent" || acct.uid != cfg.UID || acct.gid != tt.wantGID {
				t.Errorf("account = %+v, want agent %d:%d", acct, cfg.UID, tt.wantGID)
			}
			for file, want := range map[string]string{"passwd": tt.wantPasswd, "group": tt.wantGroup} {
				got, err := os.ReadFile(filepath.Join(etc, file)) //nolint:gosec // G304: test file
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != want {
					t.Errorf("%s = %q, want %q", file, got, want)
				}
			}
		})
	}
}
