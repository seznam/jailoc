package vm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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

func TestBrokerArgsProxyForward(t *testing.T) {
	t.Parallel()
	proxy := func(stateDir string) string {
		return ProxySocket(stateDir) + "=" + strconv.Itoa(guest.ProxyRelayPort)
	}
	tests := []struct {
		name  string
		spec  Spec
		wantP bool
	}{
		{"workspace without proxy relay", Spec{Kind: KindWorkspace, Guest: &guest.Config{}}, false},
		{"workspace with empty ports", Spec{Kind: KindWorkspace, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{}}}, true},
		{"workspace with ports", Spec{Kind: KindWorkspace, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{6080}}}}, true},
		{"workspace without guest config", Spec{Kind: KindWorkspace}, false},
		{"builder ignores proxy relay", Spec{Kind: KindBuilder, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{6080}}}}, false},
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
			var gotP int
			for _, f := range forwards {
				if f == proxy("/state") {
					gotP++
				}
			}
			if tt.wantP && gotP != 1 || !tt.wantP && gotP != 0 {
				t.Fatalf("proxy forwards = %d in %v, want proxy=%v", gotP, forwards, tt.wantP)
			}
			if len(forwards) != 1+gotP {
				t.Fatalf("forwards = %v, want the primary forward plus optional proxy", forwards)
			}
		})
	}
}

func TestSocketPathsIncludeProxy(t *testing.T) {
	t.Parallel()
	spec := Spec{Kind: KindWorkspace, StateDir: "/state", Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{6080}}}}
	got := supervisorSockets(spec)
	want := []string{nicSocket("/state"), SSHSocket("/state"), consoleSocket("/state"), ProxySocket("/state")}
	if !slices.Equal(got, want) {
		t.Fatalf("sockets = %v, want %v", got, want)
	}
	spec.Guest.ProxyRelay.Ports = nil
	if got := supervisorSockets(spec); !slices.Equal(got, want) {
		t.Fatalf("sockets with an empty relay = %v, want %v", got, want)
	}
	spec.Guest = nil
	if got := supervisorSockets(spec); len(got) != 3 {
		t.Fatalf("sockets without proxy relay = %v", got)
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
	for _, p := range ConsoleLogPaths(stateDir) {
		if err := os.WriteFile(p, []byte(stale), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Spec{Kind: KindBuilder, Workspace: "builder", StateDir: stateDir, DiskSize: 1 << 20}, console
}

func TestRunResetsStaleConsoleBeforeDiskPreparation(t *testing.T) {
	t.Parallel()
	spec, _ := staleConsoleSpec(t)
	err := Run(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "cannot shrink") {
		t.Fatalf("Run = %v, want the data disk shrink refusal", err)
	}
	for _, p := range ConsoleLogPaths(spec.StateDir) {
		if data, rerr := os.ReadFile(p); rerr == nil { //nolint:gosec // G304: console inside the test's own temporary directory
			t.Errorf("stale %s survived the failed Run: %q", filepath.Base(p), data)
		} else if !errors.Is(rerr, os.ErrNotExist) {
			t.Fatal(rerr)
		}
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

func TestStateAndCacheDiskPaths(t *testing.T) {
	t.Parallel()
	if got, want := StateDiskPath("/s"), "/s/state.img"; got != want {
		t.Errorf("StateDiskPath = %q, want %q", got, want)
	}
	if got, want := CacheDiskPath("/s"), "/s/cache.img"; got != want {
		t.Errorf("CacheDiskPath = %q, want %q", got, want)
	}
}

func TestStateAndCacheDiskSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		spec      Spec
		wantState int64
		wantCache int64
	}{
		{"defaults", Spec{}, DefaultStateDiskSize, DefaultCacheDiskSize},
		{"explicit", Spec{StateDiskSize: 1 << 30, CacheDiskSize: 2 << 30}, 1 << 30, 2 << 30},
		{"negative falls back", Spec{StateDiskSize: -1, CacheDiskSize: -1}, DefaultStateDiskSize, DefaultCacheDiskSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := stateDiskSize(tt.spec); got != tt.wantState {
				t.Errorf("stateDiskSize = %d, want %d", got, tt.wantState)
			}
			if got := cacheDiskSize(tt.spec); got != tt.wantCache {
				t.Errorf("cacheDiskSize = %d, want %d", got, tt.wantCache)
			}
		})
	}
}

func TestPrepareGuestStateAndCacheDevices(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "jailoc-guest")
	if err := os.WriteFile(bin, []byte("guest"), 0o700); err != nil { //nolint:gosec // G306: test binary
		t.Fatal(err)
	}
	readCfg := func(t *testing.T, spec Spec) guest.Config {
		t.Helper()
		spec.StateDir = t.TempDir()
		spec.GuestBinary = bin
		files, err := prepareGuest(spec, nil)
		if err != nil {
			t.Fatalf("prepareGuest: %v", err)
		}
		var cfg guest.Config
		if err := json.Unmarshal(files[guest.ConfigPath].data, &cfg); err != nil {
			t.Fatalf("decode guest config: %v", err)
		}
		return cfg
	}
	t.Run("no disks", func(t *testing.T) {
		t.Parallel()
		cfg := readCfg(t, Spec{Guest: &guest.Config{}})
		if cfg.State != nil || cfg.Cache != nil {
			t.Fatalf("State = %+v, Cache = %+v, want nil", cfg.State, cfg.Cache)
		}
	})
	t.Run("devices follow attach order and keep state settings", func(t *testing.T) {
		t.Parallel()
		in := &guest.StateDisk{Agent: "claude", Dirs: []string{"a"}, Ephemeral: true}
		spec := Spec{Docker: true, StateDisk: true, CacheDisk: true, Guest: &guest.Config{State: in}}
		cfg := readCfg(t, spec)
		want := guest.StateDisk{Disk: "/dev/vdb", Agent: "claude", Dirs: []string{"a"}, Ephemeral: true}
		if cfg.State == nil || !reflect.DeepEqual(*cfg.State, want) {
			t.Fatalf("State = %+v, want %+v", cfg.State, want)
		}
		if cfg.Cache == nil || cfg.Cache.Disk != "/dev/vdc" {
			t.Fatalf("Cache = %+v, want disk /dev/vdc", cfg.Cache)
		}
		if in.Disk != "" {
			t.Fatalf("input StateDisk mutated: %+v", in)
		}
	})
	t.Run("state disk without prior config", func(t *testing.T) {
		t.Parallel()
		cfg := readCfg(t, Spec{StateDisk: true, Guest: &guest.Config{}})
		if cfg.State == nil || cfg.State.Disk != "/dev/vda" || cfg.Cache != nil {
			t.Fatalf("State = %+v, Cache = %+v", cfg.State, cfg.Cache)
		}
	})
}

func TestBuilderOptionsCABundle(t *testing.T) {
	t.Parallel()
	_, caPath := tlsRegistry(t)
	base := Spec{Kind: KindBuilder, Workspace: "builder", StateDir: t.TempDir()}

	plain, err := builderOptions(base, nil)
	if err != nil {
		t.Fatalf("builderOptions without CA bundle: %v", err)
	}
	withCA := base
	withCA.CABundle = caPath
	got, err := builderOptions(withCA, nil)
	if err != nil {
		t.Fatalf("builderOptions with CA bundle: %v", err)
	}
	if len(got) != len(plain)+1 {
		t.Errorf("options with CA bundle = %d, want %d (one image fetcher more)", len(got), len(plain)+1)
	}

	missing := base
	missing.CABundle = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := builderOptions(missing, nil); err == nil {
		t.Error("builderOptions with an unreadable CA bundle: error = nil, want error")
	}
}
