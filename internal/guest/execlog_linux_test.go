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
