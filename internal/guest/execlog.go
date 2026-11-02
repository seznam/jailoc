package guest

import "time"

// ExecLogFile is the process log in the control share; the host reads it
// for `jailoc strace`. The guest rotates it to ExecLogFile+".1" at
// execLogMax bytes.
const ExecLogFile = "exec.jsonl"

// ExecRecord is one process start or exit recorded in the exec log. Fields the
// guest could not read (the process exited first) are empty.
type ExecRecord struct {
	Time time.Time `json:"time"`
	// Event is "exit" for an exit record and empty for a process start.
	Event string   `json:"event,omitempty"`
	PID   int      `json:"pid"`
	PPID  int      `json:"ppid,omitempty"`
	UID   int      `json:"uid"`
	Comm  string   `json:"comm,omitempty"`
	Cwd   string   `json:"cwd,omitempty"`
	Argv  []string `json:"argv,omitempty"`
	// Exe is the resolved executable path.
	Exe string `json:"exe,omitempty"`
	// Code is the exit status of an exit record, Signal the name of the
	// signal that killed the process instead.
	Code   int    `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}
