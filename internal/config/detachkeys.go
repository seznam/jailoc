package config

import (
	"fmt"

	"github.com/moby/term"
)

// DefaultDetachKeys detaches from an agent session like tmux does: Ctrl-b,
// then d.
const DefaultDetachKeys = "ctrl-b,d"

// ParseDetachKeys parses a comma-separated key sequence such as "ctrl-b,d"
// into the bytes the terminal sends for it, in the format of docker's
// --detach-keys.
func ParseDetachKeys(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("detach_keys: must not be empty")
	}
	seq, err := term.ToBytes(s)
	if err != nil {
		return nil, fmt.Errorf("detach_keys: %w: use ctrl-<letter>, ctrl-@, ctrl-[, ctrl-\\, ctrl-], ctrl-^, ctrl-_ or a single character", err)
	}
	return seq, nil
}
