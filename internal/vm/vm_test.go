package vm

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/guest"
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

func TestBrokerArgsWebForward(t *testing.T) {
	t.Parallel()
	web := func(stateDir string) string {
		return WebSocket(stateDir) + "=" + strconv.Itoa(guest.WebRelayPort)
	}
	tests := []struct {
		name  string
		spec  Spec
		wantW bool
	}{
		{"workspace without web relay", Spec{Kind: KindWorkspace, Guest: &guest.Config{}}, false},
		{"workspace with empty ports", Spec{Kind: KindWorkspace, Guest: &guest.Config{WebRelay: &guest.WebRelay{}}}, false},
		{"workspace with ports", Spec{Kind: KindWorkspace, Guest: &guest.Config{WebRelay: &guest.WebRelay{Ports: []uint16{6080}}}}, true},
		{"workspace without guest config", Spec{Kind: KindWorkspace}, false},
		{"builder ignores web relay", Spec{Kind: KindBuilder, Guest: &guest.Config{WebRelay: &guest.WebRelay{Ports: []uint16{6080}}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.spec.StateDir = "/state"
			tt.spec.Broker = []string{"jailoc", "netbroker"}
			args := brokerArgs(tt.spec, "/state/policy.json")
			var forwards []string
			for i, a := range args {
				if a == "-forward-unix" && i+1 < len(args) {
					forwards = append(forwards, args[i+1])
				}
			}
			var gotW int
			for _, f := range forwards {
				if f == web("/state") {
					gotW++
				}
			}
			if tt.wantW && gotW != 1 || !tt.wantW && gotW != 0 {
				t.Fatalf("web forwards = %d in %v, want web=%v", gotW, forwards, tt.wantW)
			}
			if len(forwards) != 1+gotW {
				t.Fatalf("forwards = %v, want the primary forward plus optional web", forwards)
			}
		})
	}
}

func TestSocketPathsIncludeWeb(t *testing.T) {
	t.Parallel()
	spec := Spec{Kind: KindWorkspace, StateDir: "/state", Guest: &guest.Config{WebRelay: &guest.WebRelay{Ports: []uint16{6080}}}}
	got := supervisorSockets(spec)
	want := []string{nicSocket("/state"), SSHSocket("/state"), WebSocket("/state")}
	if !slices.Equal(got, want) {
		t.Fatalf("sockets = %v, want %v", got, want)
	}
	spec.Guest = nil
	if got := supervisorSockets(spec); len(got) != 2 {
		t.Fatalf("sockets without web relay = %v", got)
	}
}
