package guest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestExitRecord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status uint32
		code   int
		signal string
	}{
		{name: "success", status: 0},
		{name: "exit code", status: 2 << 8, code: 2},
		{name: "killed", status: 9, signal: "SIGKILL"},
		{name: "core dump", status: 0x80 | 11, signal: "SIGSEGV"},
		{name: "unknown signal", status: 0x7e, signal: "signal 126"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := exitRecord(42, tt.status)
			if e.Event != "exit" || e.PID != 42 || e.Code != tt.code || e.Signal != tt.signal {
				t.Errorf("exitRecord(42, %#x) = %+v, want code %d signal %q", tt.status, e, tt.code, tt.signal)
			}
		})
	}
}

func TestExecTrackerReportsLastThreadExit(t *testing.T) {
	t.Parallel()
	procs := execTracker{}
	procs.exec(10)
	procs.fork(procEvent{what: procEventFork, pid: 10, tgid: 10, childPID: 11, childTGID: 10})
	procs.fork(procEvent{what: procEventFork, pid: 10, tgid: 10, childPID: 12, childTGID: 12}) // child process, not a thread

	// The leader exits first with pthread_exit; the worker then calls exit_group(7).
	if _, done := procs.exit(procEvent{what: procEventExit, pid: 10, tgid: 10}); done {
		t.Fatal("process reported done while a thread is still running")
	}
	status, done := procs.exit(procEvent{what: procEventExit, pid: 11, tgid: 10, exitCode: 7 << 8})
	if !done || status != 7<<8 {
		t.Fatalf("last thread exit = (%#x, %v), want (%#x, true)", status, done, 7<<8)
	}
	if _, done := procs.exit(procEvent{what: procEventExit, pid: 10, tgid: 10}); done {
		t.Error("exit of a finished process reported again")
	}
	if _, done := procs.exit(procEvent{what: procEventExit, pid: 12, tgid: 12}); done {
		t.Error("exit of an untracked process reported")
	}
}

func TestExecTrackerForgetsProcessesAfterDroppedEvents(t *testing.T) {
	t.Parallel()
	procs := execTracker{}
	procs.exec(10)
	procs.fork(procEvent{what: procEventFork, pid: 10, tgid: 10, childPID: 11, childTGID: 10})

	// The exit of thread 11 is lost with the dropped events.
	procs.dropped()
	if _, done := procs.exit(procEvent{what: procEventExit, pid: 10, tgid: 10, exitCode: 3 << 8}); done {
		t.Error("exit reported from a thread count that missed dropped events")
	}

	procs.exec(20)
	if status, done := procs.exit(procEvent{what: procEventExit, pid: 20, tgid: 20, exitCode: 1 << 8}); !done || status != 1<<8 {
		t.Errorf("exit of a process started after the drop = (%#x, %v), want (%#x, true)", status, done, 1<<8)
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	values := []string{"s3cr3t-token-long", "s3cr3t-token"}
	argv := []string{"curl", "-H", "Authorization: Bearer s3cr3t-token-long", "--data", "s3cr3t-token", "short"}
	got := redact(argv, values)
	want := []string{"curl", "-H", "Authorization: Bearer [redacted]", "--data", "[redacted]", "short"}
	if !slices.Equal(got, want) {
		t.Fatalf("redact = %q, want %q", got, want)
	}
	if argv[2] != "Authorization: Bearer s3cr3t-token-long" {
		t.Fatal("redact modified its input")
	}
	if got := redact([]string{"ls"}, values); !slices.Equal(got, []string{"ls"}) {
		t.Fatalf("redact = %q", got)
	}
}

func TestLoadSecretValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"token":      "file-secret-value\n",
		"key":        "-----BEGIN KEY-----\nline-of-key-data\n-----END KEY-----\n",
		"tiny":       "abc",
		".tmp-12345": "partial-secret-value",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(secrets, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := filepath.Join(dir, "env")
	if err := os.WriteFile(env, []byte("API_TOKEN=env-secret-value\nSHORT=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadSecretValues(secrets, env)
	for _, want := range []string{"file-secret-value", "env-secret-value", "line-of-key-data"} {
		if !slices.Contains(got, want) {
			t.Errorf("values %q miss %q", got, want)
		}
	}
	for _, unwanted := range []string{"abc", "x", "partial-secret-value"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("values %q contain %q", got, unwanted)
		}
	}
	for i := 1; i < len(got); i++ {
		if len(got[i]) > len(got[i-1]) {
			t.Fatalf("values not sorted longest first: %q", got)
		}
	}
}

func TestMergeSecretValues(t *testing.T) {
	t.Parallel()
	got := mergeSecretValues([]string{"old-token-1", "shared-value"}, []string{"shared-value", "a-much-longer-new-token"})
	want := []string{"a-much-longer-new-token", "shared-value", "old-token-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeSecretValues() = %q, want %q", got, want)
	}
}
