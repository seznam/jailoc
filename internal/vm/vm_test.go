package vm

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureDisk(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "disk.img")
	size := func() int64 {
		t.Helper()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat disk: %v", err)
		}
		return st.Size()
	}
	if err := ensureDisk(path, 2<<30); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := size(); got != 2<<30 {
		t.Fatalf("created size = %d, want %d", got, int64(2<<30))
	}
	if err := ensureDisk(path, 3<<30); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if got := size(); got != 3<<30 {
		t.Fatalf("grown size = %d, want %d", got, int64(3<<30))
	}
	err := ensureDisk(path, 1<<30)
	if err == nil || !strings.Contains(err.Error(), "cannot shrink") || !strings.Contains(err.Error(), "at least 3072m") {
		t.Fatalf("shrink: got %v, want a cannot shrink error naming 3072m", err)
	}
	if got := size(); got != 3<<30 {
		t.Fatalf("size after refused shrink = %d, want %d", got, int64(3<<30))
	}
}

func TestWriteControlNudgesSSHSocket(t *testing.T) {
	t.Parallel()
	stateDir, err := os.MkdirTemp("/tmp", "jlv-") // short path: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	if err := resetControl(stateDir); err != nil {
		t.Fatalf("resetControl: %v", err)
	}
	ln, err := net.Listen("unix", SSHSocket(stateDir))
	if err != nil {
		t.Fatalf("listen ssh socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	nudged := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
			nudged <- struct{}{}
		}
	}()

	if err := WriteControl(stateDir, "shutdown", []byte("now")); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	select {
	case <-nudged:
	case <-time.After(time.Second):
		t.Fatal("WriteControl did not nudge SSH socket")
	}
	got, err := ReadControl(stateDir, "shutdown")
	if err != nil || string(got) != "now" {
		t.Fatalf("ReadControl = %q, %v; want %q", got, err, "now")
	}
}
