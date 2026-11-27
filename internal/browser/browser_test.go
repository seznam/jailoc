//go:build unix

package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testURL = "https://example.com/oauth?state=secret-token"

func TestCommand(t *testing.T) {
	t.Parallel()
	found := func(name string) (string, error) { return "/opt/bin/" + name, nil }
	missing := func(string) (string, error) { return "", fmt.Errorf("not found") }
	tests := []struct {
		name     string
		goos     string
		lookPath func(string) (string, error)
		want     string
		wantErr  string
	}{
		{name: "darwin is fixed", goos: "darwin", lookPath: missing, want: "/usr/bin/open"},
		{name: "linux xdg-open", goos: "linux", lookPath: found, want: "/opt/bin/xdg-open"},
		{name: "linux without xdg-open", goos: "linux", lookPath: missing, wantErr: "xdg-open was not found"},
		{name: "other", goos: "plan9", lookPath: found, wantErr: "not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Command(tt.goos, tt.lookPath)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("opener = %q, want %q", got, tt.want)
			}
		})
	}
}

// testLauncher returns a Launcher with a short grace and a fixed opener.
func testLauncher(grace time.Duration, opener string) *Launcher {
	l := NewLauncher()
	l.grace = grace
	l.command = func() (string, error) { return opener, nil }
	return l
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // G306: test script must be executable
		t.Fatal(err)
	}
	return path
}

// holdScript records its pid and URL, then runs until the release file exists.
// The release file is created on cleanup so no child outlives the test.
func holdScript(t *testing.T) (script, release, pidFile, urlFile string) {
	t.Helper()
	dir := t.TempDir()
	release = filepath.Join(dir, "release")
	pidFile = filepath.Join(dir, "pid")
	urlFile = filepath.Join(dir, "url")
	script = writeScript(t, dir, "opener", fmt.Sprintf(
		"echo $$ > '%[1]s.tmp'; mv '%[1]s.tmp' '%[1]s'; echo \"$1\" > '%[2]s'\nwhile [ ! -e '%[3]s' ]; do sleep 0.02; done",
		pidFile, urlFile, release))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	return script, release, pidFile, urlFile
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readPID(t *testing.T, pidFile string) int {
	t.Helper()
	var pid int
	eventually(t, "pid file", func() bool {
		b, err := os.ReadFile(pidFile) //nolint:gosec // G304: path is in the test temp dir
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	return pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func assertNoURL(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), "example.com") || strings.Contains(err.Error(), "secret-token")) {
		t.Fatalf("error carries the URL: %v", err)
	}
}

func TestOpenURLFastExitZero(t *testing.T) {
	// Not parallel: a concurrent fork would capture the script's writable fd
	// until exec and fail with ETXTBSY (golang/go#22315).
	dir := t.TempDir()
	urlFile := filepath.Join(dir, "url")
	script := writeScript(t, dir, "opener", fmt.Sprintf("printf '%%s\\n%%s' \"$1\" \"$#\" > '%s'", urlFile))
	l := testLauncher(5*time.Second, script)
	start := time.Now()
	if err := l.OpenURL(t.Context(), testURL); err != nil {
		t.Fatalf("OpenURL = %v, want nil", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("OpenURL waited for the grace period although the child exited")
	}
	b, err := os.ReadFile(urlFile) //nolint:gosec // G304: path is in the test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), testURL+"\n1"; got != want {
		t.Fatalf("opener got %q, want exactly one URL argument %q", got, want)
	}
	eventually(t, "slot release", func() bool { return len(l.slots) == 0 })
}

func TestOpenURLFastExitNonzero(t *testing.T) {
	// Not parallel: a concurrent fork would capture the script's writable fd
	// until exec and fail with ETXTBSY (golang/go#22315).
	script := writeScript(t, t.TempDir(), "opener", "exit 3")
	l := testLauncher(5*time.Second, script)
	err := l.OpenURL(t.Context(), testURL)
	if err == nil {
		t.Fatal("OpenURL = nil, want error")
	}
	assertNoURL(t, err)
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected context error: %v", err)
	}
	eventually(t, "slot release", func() bool { return len(l.slots) == 0 })
}

func TestOpenURLStartFailure(t *testing.T) {
	t.Parallel()
	l := testLauncher(time.Second, filepath.Join(t.TempDir(), "missing"))
	err := l.OpenURL(t.Context(), testURL)
	if err == nil {
		t.Fatal("OpenURL = nil, want error")
	}
	assertNoURL(t, err)
	if len(l.slots) != 0 {
		t.Fatalf("slots in use after start failure = %d, want 0", len(l.slots))
	}
}

func TestOpenURLCommandError(t *testing.T) {
	t.Parallel()
	l := NewLauncher()
	l.command = func() (string, error) { return "", errors.New("no opener") }
	if err := l.OpenURL(t.Context(), testURL); err == nil || err.Error() != "no opener" {
		t.Fatalf("OpenURL = %v, want command error", err)
	}
}

func TestOpenURLStillRunningAfterGrace(t *testing.T) {
	// Not parallel: a concurrent fork would capture the script's writable fd
	// until exec and fail with ETXTBSY (golang/go#22315).
	script, release, pidFile, urlFile := holdScript(t)
	l := testLauncher(20*time.Millisecond, script)
	if err := l.OpenURL(t.Context(), testURL); err != nil {
		t.Fatalf("OpenURL = %v, want nil for a child still running after the grace", err)
	}
	pid := readPID(t, pidFile)
	if !alive(pid) {
		t.Fatal("opener was killed after the grace period")
	}
	if len(l.slots) != 1 {
		t.Fatalf("slots in use while the child runs = %d, want 1", len(l.slots))
	}
	eventually(t, "url file", func() bool { _, err := os.Stat(urlFile); return err == nil })

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "slot release after exit", func() bool { return len(l.slots) == 0 })
	// Reaped: the pid no longer exists (not even as a zombie).
	eventually(t, "child reaped", func() bool { return !alive(pid) })
}

func TestOpenURLCapacity(t *testing.T) {
	// Not parallel: a concurrent fork would capture the script's writable fd
	// until exec and fail with ETXTBSY (golang/go#22315).
	l := testLauncher(10*time.Millisecond, "")
	var releases []string
	for i := range maxLive {
		script, release, pidFile, _ := holdScript(t)
		l.command = func() (string, error) { return script, nil }
		if err := l.OpenURL(t.Context(), testURL); err != nil {
			t.Fatalf("opener %d: OpenURL = %v, want nil", i, err)
		}
		readPID(t, pidFile)
		releases = append(releases, release)
	}
	if len(l.slots) != maxLive {
		t.Fatalf("slots in use = %d, want %d", len(l.slots), maxLive)
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	extra := writeScript(t, dir, "opener", fmt.Sprintf("echo x > '%s'", marker))
	l.command = func() (string, error) { return extra, nil }
	err := l.OpenURL(t.Context(), testURL)
	if err == nil {
		t.Fatal("OpenURL beyond the cap = nil, want error")
	}
	assertNoURL(t, err)
	time.Sleep(100 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("opener started although the cap was reached")
	}

	// Releasing one child frees exactly one slot.
	if err := os.WriteFile(releases[0], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "one slot free", func() bool { return len(l.slots) == maxLive-1 })
	if err := l.OpenURL(t.Context(), testURL); err != nil {
		t.Fatalf("OpenURL after a slot was freed = %v, want nil", err)
	}
	for _, r := range releases[1:] {
		if err := os.WriteFile(r, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "all slots free", func() bool { return len(l.slots) == 0 })
}

func TestOpenURLCancelledBeforeStart(t *testing.T) {
	// Not parallel: a concurrent fork would capture the script's writable fd
	// until exec and fail with ETXTBSY (golang/go#22315).
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	script := writeScript(t, dir, "opener", fmt.Sprintf("echo x > '%s'", marker))
	l := testLauncher(time.Second, script)
	called := false
	inner := l.command
	l.command = func() (string, error) { called = true; return inner() }

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := l.OpenURL(ctx, testURL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenURL = %v, want context.Canceled", err)
	}
	assertNoURL(t, err)
	if called {
		t.Fatal("opener was selected for a cancelled context")
	}
	if len(l.slots) != 0 {
		t.Fatalf("slots in use = %d, want 0", len(l.slots))
	}
	time.Sleep(100 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("opener started for a cancelled context")
	}

	// launch itself also refuses a cancelled context without taking a slot.
	if err := l.launch(ctx, script, testURL); !errors.Is(err, context.Canceled) {
		t.Fatalf("launch = %v, want context.Canceled", err)
	}
	if len(l.slots) != 0 {
		t.Fatalf("slots in use = %d, want 0", len(l.slots))
	}
}

func TestOpenURLCancelledAfterStart(t *testing.T) {
	// Not parallel: a concurrent fork would capture the script's writable fd
	// until exec and fail with ETXTBSY (golang/go#22315).
	script, release, pidFile, _ := holdScript(t)
	l := testLauncher(time.Minute, script)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errc := make(chan error, 1)
	go func() { errc <- l.OpenURL(ctx, testURL) }()
	pid := readPID(t, pidFile)
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("OpenURL = %v, want context.Canceled", err)
		}
		assertNoURL(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("OpenURL did not return after cancellation")
	}
	if !alive(pid) {
		t.Fatal("cancellation killed the already started opener")
	}
	if len(l.slots) != 1 {
		t.Fatalf("slots in use = %d, want 1 until the child exits", len(l.slots))
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "slot release after exit", func() bool { return len(l.slots) == 0 })
	eventually(t, "child reaped", func() bool { return !alive(pid) })
}
