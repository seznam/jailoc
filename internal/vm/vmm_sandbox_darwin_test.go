//go:build darwin

package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/go-microvm/runner"
)

// The test binary doubles as the jailoc shim (the default
// seatbeltShimCommand re-executes os.Executable() with "vm-seatbelt") and as
// the stand-in VM runner: the shim execs it with helperName as argv[0] and a
// JSON action as argv[1], the place where the real runner gets its config.
const (
	helperName = "seatbelt-helper"

	codeOK     = 0
	codeDenied = 10 // EPERM or EACCES: what Seatbelt returns for a denied operation
	codeFailed = 11
)

type helperAction struct {
	Action string   `json:"action"`
	Args   []string `json:"args"`
	// ConsoleLog is read from a runner.Config when the helper is spawned by
	// confinedSpawner, which passes the config as argv[1].
	ConsoleLog string `json:"console_log_path"`
}

func TestMain(m *testing.M) {
	switch {
	case len(os.Args) > 1 && os.Args[1] == "vm-seatbelt":
		err := RunSeatbeltShim(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1) // RunSeatbeltShim only returns on failure
	case filepath.Base(os.Args[0]) == helperName && len(os.Args) == 2:
		os.Exit(runHelper(os.Args[1]))
	}
	os.Exit(m.Run())
}

func runHelper(arg string) int {
	var a helperAction
	if err := json.Unmarshal([]byte(arg), &a); err != nil {
		fmt.Fprintln(os.Stderr, "helper: bad action:", err)
		return codeFailed
	}
	if a.Action == "" {
		return runSpawnProbe(a)
	}
	err := helperRun(a.Action, a.Args)
	switch {
	case err == nil:
		return codeOK
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		fmt.Fprintln(os.Stderr, err)
		return codeDenied
	default:
		fmt.Fprintln(os.Stderr, err)
		return codeFailed
	}
}

func helperRun(action string, a []string) error {
	arg := func(i int) string {
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	switch action {
	case "read":
		_, err := os.ReadFile(arg(0))
		return err
	case "stat":
		_, err := os.Lstat(arg(0))
		return err
	case "readdir":
		_, err := os.ReadDir(arg(0))
		return err
	case "write":
		f, err := os.OpenFile(arg(0), os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		_, err = f.WriteString("written")
		return errors.Join(err, f.Close())
	case "create":
		f, err := os.OpenFile(arg(0), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	case "remove":
		return os.Remove(arg(0))
	case "connect-unix", "connect-tcp":
		c, err := net.DialTimeout(strings.TrimPrefix(action, "connect-"), arg(0), 2*time.Second)
		if err != nil {
			return err
		}
		return c.Close()
	case "send-udp":
		c, err := net.Dial("udp", arg(0))
		if err != nil {
			return err
		}
		_, err = c.Write([]byte("x"))
		return errors.Join(err, c.Close())
	case "bind-unix":
		l, err := net.Listen("unix", arg(0))
		if err != nil {
			return err
		}
		return l.Close()
	}
	return fmt.Errorf("unknown action %q", action)
}

// runSpawnProbe is the stand-in runner of the confinedSpawner tests: it
// reports its argv[0] and DYLD_LIBRARY_PATH in the console log, says "ready"
// on stdout (the runner log) and waits for SIGTERM.
func runSpawnProbe(a helperAction) int {
	report := fmt.Sprintf("dyld=%s\nargv0=%s\n", os.Getenv("DYLD_LIBRARY_PATH"), os.Args[0])
	if err := os.WriteFile(a.ConsoleLog, []byte(report), 0o600); err != nil { //nolint:gosec // G703: test-controlled path
		fmt.Fprintln(os.Stderr, "helper:", err)
		return codeFailed
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	fmt.Println("ready")
	<-term
	return codeOK
}

// fixture is a VM state tree like the one the runner gets, plus a directory
// the runner has no business in.
type fixture struct {
	base string
	cfg  runner.Config
	// Listening sockets: the broker's NIC socket, one the runner may not
	// connect to, and a loopback TCP listener.
	otherSock, tcpAddr string
}

// newFixture builds the tree under /tmp: short enough for Unix socket paths
// (104 bytes on macOS) and behind the /tmp -> /private/tmp symlink, so every
// test also exercises path canonicalization.
func newFixture(t *testing.T) fixture {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "jlsb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{"ro", "rw", "outside", "lib", "rootfs", "state/microvm"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		"ro/file", "ro/write-me", "ro/remove-me",
		"rw/file", "rw/write-me", "rw/remove-me",
		"outside/secret", "lib/libkrun.dylib", "rootfs/file",
		"state/docker.img", "state/tmp.img", "state/tmp.img.lock", "state/base.img",
	} {
		if err := os.WriteFile(filepath.Join(base, f), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "outside", "secret"), filepath.Join(base, "rw", "link-to-secret")); err != nil {
		t.Fatal(err)
	}

	f := fixture{base: base}
	listen := func(network, addr string) net.Listener {
		l, err := net.Listen(network, addr)
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
				_ = c.Close()
			}
		}()
		return l
	}
	nic := filepath.Join(base, "nic.sock")
	listen("unix", nic)
	f.otherSock = filepath.Join(base, "other.sock")
	listen("unix", f.otherSock)
	f.tcpAddr = listen("tcp", "127.0.0.1:0").Addr().String()

	f.cfg = runner.Config{
		RunnerPath: exe,
		LibDir:     filepath.Join(base, "lib"),
		RootPath:   filepath.Join(base, "rootfs"),
		NetSocket:  nic,
		VirtioFS: []runner.VirtioFSMount{
			{Tag: "rw", HostPath: filepath.Join(base, "rw")},
			{Tag: "ro", HostPath: filepath.Join(base, "ro"), ReadOnly: true},
		},
		Disks: []runner.Disk{
			{ID: "docker", HostPath: filepath.Join(base, "state", "docker.img")},
			{ID: "tmp", HostPath: filepath.Join(base, "state", "tmp.img")},
			{ID: "base", HostPath: filepath.Join(base, "state", "base.img"), ReadOnly: true},
		},
		ConsoleLog: filepath.Join(base, "state", "microvm", "console.log"),
		VMLogPath:  filepath.Join(base, "state", "microvm", "vm.log"),
		VsockPorts: []runner.VsockPort{{Port: 1, SocketPath: filepath.Join(base, "state", "vsock.sock")}},
	}
	return f
}

func (f fixture) path(rel string) string { return filepath.Join(f.base, rel) }

// runConfined runs the test binary under the profile of cfg through the real
// shim and returns its exit code and stderr.
func runConfined(t *testing.T, cfg runner.Config, action string, args ...string) (int, string) {
	t.Helper()
	profile, err := seatbeltProfile(cfg, cfg.RunnerPath)
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	actionJSON, err := json.Marshal(helperAction{Action: action, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := seatbeltShim(cfg.RunnerPath, helperName, actionJSON)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := launchSeatbelt(cmd, profile); err != nil {
		t.Fatalf("launch %s %v: %v", action, args, err)
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return codeOK, stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stderr.String()
	default:
		t.Fatalf("wait %s %v: %v", action, args, err)
		return 0, ""
	}
}

func TestConfinedRunnerAccess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	secret := f.path("outside/secret")
	tests := []struct {
		name   string
		action string
		args   []string
		want   int
	}{
		{"read ro share", "read", []string{f.path("ro/file")}, codeOK},
		{"stat ro share file", "stat", []string{f.path("ro/file")}, codeOK},
		{"list ro share", "readdir", []string{f.path("ro")}, codeOK},
		{"write ro share", "write", []string{f.path("ro/write-me")}, codeDenied},
		{"create in ro share", "create", []string{f.path("ro/new")}, codeDenied},
		{"remove from ro share", "remove", []string{f.path("ro/remove-me")}, codeDenied},

		{"read rw share", "read", []string{f.path("rw/file")}, codeOK},
		{"write rw share", "write", []string{f.path("rw/write-me")}, codeOK},
		{"create in rw share", "create", []string{f.path("rw/new")}, codeOK},
		{"remove from rw share", "remove", []string{f.path("rw/remove-me")}, codeOK},

		{"read root fs", "read", []string{f.path("rootfs/file")}, codeOK},
		{"create in root fs", "create", []string{f.path("rootfs/new")}, codeOK},

		{"read lib dir", "read", []string{f.path("lib/libkrun.dylib")}, codeOK},
		{"write lib dir", "write", []string{f.path("lib/libkrun.dylib")}, codeDenied},

		{"write disk", "write", []string{f.path("state/docker.img")}, codeOK},
		{"write tmp disk", "write", []string{f.path("state/tmp.img")}, codeOK},
		{"delete tmp disk", "remove", []string{f.path("state/tmp.img")}, codeDenied},
		{"read read-only disk", "read", []string{f.path("state/base.img")}, codeOK},
		{"write read-only disk", "write", []string{f.path("state/base.img")}, codeDenied},
		{"write tmp disk lock file", "write", []string{f.path("state/tmp.img.lock")}, codeDenied},
		{"create file next to tmp disk", "create", []string{f.path("state/tmp.img.new")}, codeDenied},
		{"list state dir", "readdir", []string{f.path("state")}, codeDenied},
		{"create console log", "create", []string{f.cfg.ConsoleLog}, codeOK},
		{"create other file next to console log", "create", []string{f.path("state/microvm/other.log")}, codeDenied},

		{"read outside file", "read", []string{secret}, codeDenied},
		{"stat outside file (metadata)", "stat", []string{secret}, codeDenied},
		{"stat outside dir (metadata)", "stat", []string{f.path("outside")}, codeDenied},
		{"list outside dir", "readdir", []string{f.path("outside")}, codeDenied},
		{"list parent of shares", "readdir", []string{f.base}, codeDenied},
		{"read outside via .. in rw share", "read", []string{f.path("rw") + "/../outside/secret"}, codeDenied},
		{"read outside via .. in ro share", "read", []string{f.path("ro") + "/../outside/secret"}, codeDenied},
		{"create outside via .. in root fs", "create", []string{f.path("rootfs") + "/../outside/planted"}, codeDenied},
		{"read outside via symlink in rw share", "read", []string{f.path("rw/link-to-secret")}, codeDenied},
		{"read outside via /tmp alias", "read", []string{"/tmp/" + filepath.Base(f.base) + "/outside/secret"}, codeDenied},

		{"connect to broker socket", "connect-unix", []string{f.cfg.NetSocket}, codeOK},
		{"connect to other unix socket", "connect-unix", []string{f.otherSock}, codeDenied},
		{"connect to TCP", "connect-tcp", []string{f.tcpAddr}, codeDenied},
		{"send UDP", "send-udp", []string{f.tcpAddr}, codeDenied},
		{"bind vsock socket", "bind-unix", []string{f.cfg.VsockPorts[0].SocketPath}, codeOK},
		{"bind other unix socket in rw share", "bind-unix", []string{f.path("rw/other-bind.sock")}, codeDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, stderr := runConfined(t, f.cfg, tt.action, tt.args...)
			if got != tt.want {
				t.Errorf("%s %v: exit %d, want %d\nstderr: %s", tt.action, tt.args, got, tt.want, strings.TrimSpace(stderr))
			}
		})
	}
}

// TestConfinedRunnerWithoutShares checks that nothing is reachable by
// default: without the share and disk rules the files stay unreadable.
func TestConfinedRunnerWithoutShares(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cfg := f.cfg
	cfg.VirtioFS = nil
	cfg.Disks = nil
	for _, rel := range []string{"ro/file", "rw/file", "state/docker.img"} {
		if got, stderr := runConfined(t, cfg, "read", f.path(rel)); got != codeDenied {
			t.Errorf("read %s: exit %d, want %d\nstderr: %s", rel, got, codeDenied, strings.TrimSpace(stderr))
		}
	}
}

func TestConfinedSpawnerRunsRunnerUnderProfile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cfg := f.cfg
	cfg.ProcessName = helperName

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	proc, err := confinedSpawner{}.Spawn(ctx, cfg)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = proc.Stop(context.Background()) })
	if proc.PID() <= 1 || !proc.IsAlive() {
		t.Fatalf("PID = %d, IsAlive = %v, want a live process", proc.PID(), proc.IsAlive())
	}

	waitFor(t, "runner to report ready in the runner log", func() bool {
		log, err := os.ReadFile(cfg.VMLogPath)
		return err == nil && strings.Contains(string(log), "ready")
	})
	report, err := os.ReadFile(cfg.ConsoleLog)
	if err != nil {
		t.Fatal(err)
	}
	// DYLD_LIBRARY_PATH reaches the runner through the shim, and argv[0] is
	// the configured process name.
	want := "dyld=" + cfg.LibDir + "\nargv0=" + helperName + "\n"
	if string(report) != want {
		t.Errorf("runner report = %q, want %q", report, want)
	}

	if err := proc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if proc.IsAlive() {
		t.Error("runner is alive after Stop")
	}
	if err := proc.Stop(ctx); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestConfinedSpawnerFailsClosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	t.Run("broken profile", func(t *testing.T) {
		t.Parallel()
		marker := f.path("rw/ran")
		actionJSON, err := json.Marshal(helperAction{Action: "create", Args: []string{marker}})
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := seatbeltShim(f.cfg.RunnerPath, helperName, actionJSON)
		if err != nil {
			t.Fatal(err)
		}
		err = launchSeatbelt(cmd, "(version 1)\n(deny default)\n(this is not valid SBPL")
		if err == nil {
			_ = cmd.Wait()
			t.Fatal("launch accepted a broken profile")
		}
		if !strings.Contains(err.Error(), "Seatbelt") {
			t.Errorf("error = %v, want the Seatbelt failure", err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("runner ran despite the broken profile")
		}
	})

	t.Run("runner cannot be executed", func(t *testing.T) {
		t.Parallel()
		cfg := f.cfg
		cfg.RunnerPath = f.path("lib/libkrun.dylib") // exists, but is not executable
		_, err := confinedSpawner{}.Spawn(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "exec") {
			t.Fatalf("Spawn error = %v, want an exec failure", err)
		}
	})

	t.Run("missing runner", func(t *testing.T) {
		t.Parallel()
		cfg := f.cfg
		cfg.RunnerPath = f.path("no-such-runner")
		if _, err := (confinedSpawner{}).Spawn(context.Background(), cfg); err == nil {
			t.Fatal("Spawn succeeded without a runner")
		}
	})

	t.Run("profile cannot be built", func(t *testing.T) {
		t.Parallel()
		cfg := f.cfg
		cfg.VirtioFS = []runner.VirtioFSMount{{Tag: "bad"}} // no host path
		if _, err := (confinedSpawner{}).Spawn(context.Background(), cfg); err == nil {
			t.Fatal("Spawn succeeded with an invalid share")
		}
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
