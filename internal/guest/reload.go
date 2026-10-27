package guest

import "strings"

// Reload carries the guest settings the host can change without a restart.
// The host writes it as ReloadFile in ControlDir; the guest init applies it
// and answers with ReloadDoneFile carrying the same Seq.
type Reload struct {
	Seq      int64  `json:"seq"`
	CABundle string `json:"ca_bundle,omitempty"`
	SSHAgent bool   `json:"ssh_agent,omitempty"`
}

// ReloadDone reports the result of applying the Reload with Seq.
type ReloadDone struct {
	Seq   int64  `json:"seq"`
	Error string `json:"error,omitempty"`
}

const (
	// ReloadFile is the reload request in ControlDir.
	ReloadFile = "reload.json"
	// ReloadDoneFile is the reload answer in ControlDir.
	ReloadDoneFile = "reload-done.json"
)

// lastWins drops earlier entries of keys that appear again later in env, so
// a later KEY=VALUE overrides an earlier one for every libc.
func lastWins(env []string) []string {
	last := make(map[string]int, len(env))
	for i, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		last[key] = i
	}
	out := make([]string, 0, len(last))
	for i, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if last[key] == i {
			out = append(out, kv)
		}
	}
	return out
}
