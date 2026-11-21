package vm

import (
	"context"
	"errors"
	"net"
	"net/http"
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
	if err := ensureDisk(path, 2<<30, "docker_disk"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := size(); got != 2<<30 {
		t.Fatalf("created size = %d, want %d", got, int64(2<<30))
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // G304: disk inside the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	const offset = 1 << 20
	const content = "existing Docker disk content"
	if _, err := f.WriteAt([]byte(content), offset); err != nil {
		t.Fatalf("write disk content: %v", err)
	}
	checkContent := func() {
		t.Helper()
		got := make([]byte, len(content))
		if _, err := f.ReadAt(got, offset); err != nil {
			t.Fatalf("read disk content: %v", err)
		}
		if string(got) != content {
			t.Fatalf("disk content = %q, want %q", got, content)
		}
	}
	if err := ensureDisk(path, 3<<30, "docker_disk"); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if got := size(); got != 3<<30 {
		t.Fatalf("grown size = %d, want %d", got, int64(3<<30))
	}
	checkContent()
	err = ensureDisk(path, 1<<30, "docker_disk")
	if err == nil || !strings.Contains(err.Error(), "cannot shrink") || !strings.Contains(err.Error(), "at least 3072m") {
		t.Fatalf("shrink: got %v, want a cannot shrink error naming 3072m", err)
	}
	if got := size(); got != 3<<30 {
		t.Fatalf("size after refused shrink = %d, want %d", got, int64(3<<30))
	}
	checkContent()
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

func TestEnsureDiskShrinkGuidance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		spec    Spec
		want    string
		notWant string
	}{
		{"shared builder", Spec{Kind: KindBuilder, Workspace: "builder"}, "[vm] builder_disk", "docker_disk"},
		{"isolated builder", Spec{Kind: KindBuilder, Workspace: "ws-builder"}, "docker_disk", "builder_disk"},
		{"workspace docker", Spec{Kind: KindWorkspace, Workspace: "ws"}, "docker_disk", "builder_disk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "disk.img")
			if err := ensureDisk(path, 3<<30, diskSetting(tt.spec)); err != nil {
				t.Fatal(err)
			}
			err := ensureDisk(path, 1<<30, diskSetting(tt.spec))
			if err == nil {
				t.Fatal("shrink accepted")
			}
			msg := err.Error()
			if !strings.Contains(msg, tt.want) || strings.Contains(msg, tt.notWant) || !strings.Contains(msg, "at least 3072m") {
				t.Errorf("error %q must name %s and 3072m, not %s", msg, tt.want, tt.notWant)
			}
			if strings.Contains(msg, "remove") {
				t.Errorf("error %q suggests removing the disk", msg)
			}
		})
	}
}

// staleConsoleSpec is a builder spec whose existing data disk is larger than
// the requested size, so Run fails in disk preparation before any broker or VM
// boot. The previous boot's console.log is left behind in the microvm dir.
func staleConsoleSpec(t *testing.T) (Spec, string) {
	t.Helper()
	stateDir, err := os.MkdirTemp("/tmp", "jlr-") // short path: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	if err := ensureDisk(diskPath(stateDir), 2<<20, "docker_disk"); err != nil {
		t.Fatal(err)
	}
	console := filepath.Join(stateDir, "microvm", "console.log")
	if err := os.MkdirAll(filepath.Dir(console), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := "level=fatal msg=\"docker: no space left on device\"\n"
	if err := os.WriteFile(console, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	return Spec{Kind: KindBuilder, Workspace: "builder", StateDir: stateDir, DiskSize: 1 << 20}, console
}

func TestRunResetsStaleConsoleBeforeDiskPreparation(t *testing.T) {
	t.Parallel()
	spec, console := staleConsoleSpec(t)
	err := Run(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "cannot shrink") {
		t.Fatalf("Run = %v, want the data disk shrink refusal", err)
	}
	if data, rerr := os.ReadFile(console); rerr == nil { //nolint:gosec // G304: console inside the test's own temporary directory
		t.Errorf("stale console.log survived the failed Run: %q", data)
	} else if !errors.Is(rerr, os.ErrNotExist) {
		t.Fatal(rerr)
	}
	if st, serr := os.Stat(diskPath(spec.StateDir)); serr != nil || st.Size() != 2<<20 {
		t.Errorf("data disk changed: %v, %v", st, serr)
	}
}

func TestRunReportsConsoleResetFailure(t *testing.T) {
	t.Parallel()
	spec, console := staleConsoleSpec(t)
	// A console.log that cannot be removed: a non-empty directory.
	if err := os.Remove(console); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(console, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "console") || strings.Contains(err.Error(), "cannot shrink") {
		t.Fatalf("Run = %v, want a console reset error before disk preparation", err)
	}
}

// pingServer serves the Docker ping endpoint on a unix socket.
func pingServer(t *testing.T, status int) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "jlv-") // short path: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestWaitDocker(t *testing.T) {
	t.Parallel()
	t.Run("ready", func(t *testing.T) {
		t.Parallel()
		if err := waitDocker(t.Context(), pingServer(t, http.StatusOK), time.Minute); err != nil {
			t.Fatalf("waitDocker: %v", err)
		}
	})
	t.Run("cancelled while unready", func(t *testing.T) {
		t.Parallel()
		sock := pingServer(t, http.StatusServiceUnavailable)
		ctx, cancel := context.WithCancelCause(t.Context())
		time.AfterFunc(100*time.Millisecond, func() { cancel(errStartupVMExited) })
		start := time.Now()
		err := waitDocker(ctx, sock, time.Minute)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitDocker = %v, want context.Canceled", err)
		}
		if took := time.Since(start); took > 900*time.Millisecond {
			t.Fatalf("waitDocker took %s to notice the cancellation", took)
		}
		if got := context.Cause(ctx); !errors.Is(got, errStartupVMExited) {
			t.Fatalf("cause = %v", got)
		}
	})
}
