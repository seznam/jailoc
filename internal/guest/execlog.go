package guest

import "time"

// ExecLogFile is the process log in the control share; the host reads it
// for `jailoc strace`. The guest rotates it to ExecLogFile+".1" at
// execLogMax bytes.
const ExecLogFile = "exec.jsonl"

// ExecRecord is one process start recorded in the exec log. Fields the guest
// could not read (the process exited first) are empty.
type ExecRecord struct {
	Time time.Time `json:"time"`
	PID  int       `json:"pid"`
	PPID int       `json:"ppid,omitempty"`
	UID  int       `json:"uid"`
	Comm string    `json:"comm,omitempty"`
	Cwd  string    `json:"cwd,omitempty"`
	Argv []string  `json:"argv,omitempty"`
	// Exe is the resolved executable path.
	Exe string `json:"exe,omitempty"`
}
