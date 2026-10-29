package config

import (
	"fmt"
	"strconv"
)

// Profile is the agents.<name>.profile setting: where the agent harness
// keeps its state. A nil *Profile keeps it per workspace until
// `jailoc down --purge`.
type Profile struct {
	// name is a shared profile under ~/.local/share/jailoc/profiles; empty
	// means profile = false: per workspace, emptied on every VM start.
	name string
}

// NamedProfile returns the shared profile name.
func NamedProfile(name string) Profile {
	return Profile{name: name}
}

// EphemeralProfile returns profile = false.
func EphemeralProfile() Profile {
	return Profile{}
}

// Name returns the shared profile name, and false for profile = false.
func (p Profile) Name() (string, bool) {
	return p.name, p.name != ""
}

// Ephemeral reports profile = false.
func (p Profile) Ephemeral() bool {
	return p.name == ""
}

func (p *Profile) UnmarshalTOML(value any) error {
	switch value := value.(type) {
	case bool:
		if value {
			return fmt.Errorf("profile: true is not valid: use false or a profile name")
		}
		*p = EphemeralProfile()
		return nil
	case string:
		*p = NamedProfile(value)
		if !agentNameRe.MatchString(value) {
			return fmt.Errorf("profile: invalid profile name %q: must match [a-z0-9-]+", value)
		}
		return nil
	default:
		return fmt.Errorf("profile: expected false or a profile name, got %T", value)
	}
}

func (p Profile) MarshalTOML() ([]byte, error) {
	if p.name == "" {
		return []byte("false"), nil
	}
	return []byte(strconv.Quote(p.name)), nil
}
