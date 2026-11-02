package guest

import "testing"

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
