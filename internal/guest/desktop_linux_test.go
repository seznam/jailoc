package guest

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDesktopSpecs(t *testing.T) {
	t.Parallel()
	specs := desktopSpecs()
	var names []string
	for _, s := range specs {
		names = append(names, s.name)
	}
	if want := []string{"Xtigervnc", "openbox", "websockify"}; !slices.Equal(names, want) {
		t.Fatalf("start order = %v, want %v", names, want)
	}
	tests := []struct {
		name string
		idx  int
		want []string
	}{
		{"xtigervnc", 0, []string{"Xtigervnc", ":1", "-localhost", "-SecurityTypes", "None", "-rfbport", "5901", "-nolisten", "tcp", "-UseIPv6=0", "-geometry", "1280x800", "-depth", "24"}},
		{"openbox", 1, []string{"dbus-run-session", "--", "openbox", "--startup", "xterm"}},
		{"websockify", 2, []string{"websockify", "--web", "/usr/share/novnc", "127.0.0.1:6080", "127.0.0.1:5901"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !slices.Equal(specs[tt.idx].argv, tt.want) {
				t.Errorf("argv = %q, want %q", specs[tt.idx].argv, tt.want)
			}
		})
	}
	// Openbox starts only once the X display accepts connections.
	if specs[0].ready == nil || specs[2].ready == nil {
		t.Error("Xtigervnc and websockify need readiness checks")
	}
}

func TestDesktopEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	if err := os.WriteFile(envFile, []byte("TOKEN=from-file\nDISPLAY=:9\n\nEMPTYLINE_OK=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	acct := account{name: "agent", uid: 1000, gid: 1000, home: "/home/agent", shell: "/bin/bash"}
	base := []string{"PATH=/usr/bin", "TOKEN=from-base", "HOME=/wrong", "XAUTHORITY=/stale"}

	env := desktopEnv(base, envFile, acct)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Errorf("duplicate %s in %q", k, env)
		}
		got[k] = v
	}
	for k, want := range map[string]string{
		"PATH": "/usr/bin", "TOKEN": "from-file", "DISPLAY": ":1", "HOME": "/home/agent",
		"USER": "agent", "LOGNAME": "agent", "SHELL": "/bin/bash", "EMPTYLINE_OK": "1",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	// A missing EnvFile is not an error.
	if env := desktopEnv(base, filepath.Join(dir, "missing"), acct); !slices.Contains(env, "TOKEN=from-base") {
		t.Errorf("missing env file dropped base entries: %q", env)
	}
}

// TestSessionEnvDesktop cannot be parallel: it sets variables in the process
// environment that sessionEnv reads.
func TestSessionEnvDesktop(t *testing.T) {
	t.Setenv("DISPLAY", ":7")
	t.Setenv("XAUTHORITY", "")
	tests := []struct {
		name string
		cfg  *Config
		want []string // DISPLAY entries
	}{
		{"without desktop", &Config{}, []string{"DISPLAY=:7"}},
		{"desktop forces display", &Config{Desktop: &Desktop{}}, []string{"DISPLAY=:1"}},
		{"desktop wins over configured", &Config{Desktop: &Desktop{}, Env: []string{"DISPLAY=:3"}}, []string{"DISPLAY=:1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := sessionEnv(tt.cfg)
			var got []string
			for _, kv := range env {
				if strings.HasPrefix(kv, "DISPLAY=") {
					got = append(got, kv)
				}
				if strings.HasPrefix(kv, "XAUTHORITY=") && tt.cfg.Desktop != nil && kv != "XAUTHORITY=" {
					t.Errorf("desktop invented %s without creating an authority", kv)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("DISPLAY entries = %q, want %q", got, tt.want)
			}
		})
	}
}

func reap(t *testing.T, pid int) {
	t.Helper()
	var ws syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func alive(pid int) bool {
	state, err := processState(pid)
	return err == nil && state != 'Z'
}

func TestRunDesktopStopKillsProcessGroups(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	ready := func() error { return nil }
	specs := []desktopSpec{
		// The leader ignores TERM, so only the bounded KILL ends it; its
		// background child shares the process group and must die too.
		{name: "stubborn", argv: []string{"sh", "-c", "trap '' TERM; sleep 60 & echo $! > " + childPID + "; wait"}, ready: ready},
		{name: "plain", argv: []string{"sleep", "60"}, ready: ready},
	}
	d, err := runDesktop(discardLogger(), specs, desktopRun{
		env: os.Environ(), dir: dir, logDir: dir, termWait: 200 * time.Millisecond, readyWait: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	pids := []int{d.procs[0].pid, d.procs[1].pid}
	waitFor(t, func() bool { _, err := os.Stat(childPID); return err == nil })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := root.ReadFile("child.pid")
	_ = root.Close() // read-only root; the read error is checked below
	if err != nil {
		t.Fatal(err)
	}
	child := atoiTrim(t, string(b))
	for _, pid := range append(pids, child) {
		if !alive(pid) {
			t.Fatalf("pid %d not running before stop", pid)
		}
	}
	d.stop(discardLogger())
	for _, pid := range pids {
		reap(t, pid)
	}
	waitFor(t, func() bool { return !alive(child) })
	for _, pid := range pids {
		if alive(pid) {
			t.Errorf("pid %d survived stop", pid)
		}
	}
	d.stop(discardLogger()) // idempotent
}

func TestRunDesktopFailureStopsStarted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ok := func() error { return nil }
	specs := []desktopSpec{
		{name: "first", argv: []string{"sleep", "60"}, ready: ok},
		{name: "dies", argv: []string{"sh", "-c", "exit 3"}, ready: func() error { time.Sleep(time.Second); return nil }},
		{name: "never", argv: []string{"sleep", "61"}, ready: ok},
	}
	var firstPID int
	run := desktopRun{env: os.Environ(), dir: dir, logDir: dir, termWait: 200 * time.Millisecond, readyWait: 5 * time.Second,
		started: func(name string, pid int) {
			if name == "first" {
				firstPID = pid
			}
			if name == "never" {
				t.Error("program after the failed one was started")
			}
		}}
	d, err := runDesktop(discardLogger(), specs, run)
	if err == nil || !strings.Contains(err.Error(), "dies") {
		t.Fatalf("err = %v, want failure naming the program that exited", err)
	}
	if d != nil {
		t.Error("failed start returned a desktop")
	}
	reap(t, firstPID)
	waitFor(t, func() bool { return !alive(firstPID) })
}

func TestRunDesktopMissingProgram(t *testing.T) {
	t.Parallel()
	specs := []desktopSpec{{name: "ghost", argv: []string{"jailoc-no-such-desktop-binary"}, ready: func() error { return nil }}}
	_, err := runDesktop(discardLogger(), specs, desktopRun{env: os.Environ(), dir: t.TempDir(), logDir: t.TempDir(), termWait: time.Second, readyWait: time.Second})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want error naming the missing program", err)
	}
}

func TestRunDesktopReadyTimeout(t *testing.T) {
	t.Parallel()
	specs := []desktopSpec{{name: "slow", argv: []string{"sleep", "60"}, ready: func() error { return os.ErrNotExist }}}
	var pid int
	_, err := runDesktop(discardLogger(), specs, desktopRun{env: os.Environ(), dir: t.TempDir(), logDir: t.TempDir(), termWait: 200 * time.Millisecond, readyWait: 300 * time.Millisecond,
		started: func(_ string, p int) { pid = p }})
	if err == nil || !strings.Contains(err.Error(), "slow") {
		t.Fatalf("err = %v, want readiness timeout naming the program", err)
	}
	reap(t, pid)
	waitFor(t, func() bool { return !alive(pid) })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func atoiTrim(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range strings.TrimSpace(s) {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestComposeProgramEnvDesktopDisplay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	if err := os.WriteFile(envFile, []byte("DISPLAY=:9\nTOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"PATH=/usr/bin", "DISPLAY=:1"}
	extra := []string{"DISPLAY=:5", "MODE=x"}
	tests := []struct {
		name        string
		desktop     bool
		wantDisplay string
	}{
		// Without a desktop the layering is unchanged: the last source wins.
		{"no desktop keeps user override", false, ":5"},
		{"desktop wins over EnvFile and -e", true, ":1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := composeProgramEnv(base, envFile, extra, tt.desktop)
			var displays []string
			for _, kv := range env {
				if strings.HasPrefix(kv, "DISPLAY=") {
					displays = append(displays, kv)
				}
			}
			if want := []string{"DISPLAY=" + tt.wantDisplay}; !slices.Equal(displays, want) {
				t.Errorf("DISPLAY entries = %q, want %q", displays, want)
			}
			for _, want := range []string{"TOKEN=secret", "MODE=x", "PATH=/usr/bin"} {
				if !slices.Contains(env, want) {
					t.Errorf("env lost %q: %q", want, env)
				}
			}
		})
	}
}

func TestDesktopConfigured(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec // G306: test file
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"desktop set", write("on.json", `{"desktop":{}}`), true},
		{"desktop absent", write("off.json", `{"user":"agent"}`), false},
		{"invalid json", write("bad.json", `{`), false},
		{"missing file", filepath.Join(dir, "missing.json"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := desktopConfigured(tt.path); got != tt.want {
				t.Errorf("desktopConfigured = %v, want %v", got, tt.want)
			}
		})
	}
}
