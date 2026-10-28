// Package agent defines the coding agent harnesses jailoc runs in workspace
// VMs: how each is installed, started and resumed, and which of its home
// directories survive restarts.
package agent

import (
	"slices"
)

// Default is the harness of a workspace that does not set agent.
const Default = "opencode"

// StateDir is a home directory of the agent kept across VM restarts.
type StateDir struct {
	// Name is the directory under the workspace's persistent home on the
	// host.
	Name string
	// Path is the directory in the guest.
	Path string
}

// Harness describes a built-in coding agent.
type Harness struct {
	// Name is the value of agent in the configuration.
	Name string
	// Title is the human-readable name.
	Title string
	// Binary is the command that starts the agent.
	Binary string
	// Package and Version are the pinned npm package the built-in image
	// layer installs.
	Package string
	Version string
	// Continue is the flag that continues the agent's last conversation.
	Continue string
	// Resume is the flag that takes the conversation ID to resume.
	Resume string
	// Unattended are the flags that let the agent act without asking for
	// permission: the VM is the security boundary.
	Unattended []string
	// Env is set in the VM for every session.
	Env []string
	// State are the home directories that survive VM restarts.
	State []StateDir
	// Mounts are the default mounts added before [defaults] and workspace
	// mounts.
	Mounts []string
	// TUIConfigEnv names the environment variable that points the agent at
	// the configuration loading the jailoc TUI plugin; empty when the agent
	// has no TUI plugin.
	TUIConfigEnv string
	// ConfigDir is the guest path of the agent's configuration directory and
	// ConfigGitignore the .gitignore the agent writes there on startup. jailoc
	// creates it on the host when the directory is mounted read-only.
	ConfigDir       string
	ConfigGitignore string
}

// sharedMounts are default mounts of every harness.
var sharedMounts = []string{"~/.agents:/home/agent/.agents:ro"}

var harnesses = []Harness{
	{
		Name:     "opencode",
		Title:    "OpenCode",
		Binary:   "opencode",
		Package:  "opencode-ai",
		Version:  "1.18.4",
		Continue: "--continue",
		Resume:   "--session",
		Env:      []string{"OPENCODE_DISABLE_AUTOUPDATE=1"},
		State: []StateDir{
			{Name: "opencode-data", Path: "/home/agent/.local/share/opencode"},
			{Name: "opencode-state", Path: "/home/agent/.local/state/opencode"},
		},
		Mounts: []string{
			"~/.config/opencode:/home/agent/.config/opencode:rw",
			"~/.opencode:/home/agent/.opencode:rw",
		},
		TUIConfigEnv: "OPENCODE_TUI_CONFIG",
		ConfigDir:    "/home/agent/.config/opencode",
		// OpenCode (v1.14.22+) writes this file on startup and crashes with
		// EROFS on a read-only config directory without it.
		// See: https://github.com/anomalyco/opencode/issues/23040
		ConfigGitignore: "node_modules\npackage.json\npackage-lock.json\nbun.lock\n.gitignore\n",
	},
	{
		Name:       "claude",
		Title:      "Claude Code",
		Binary:     "claude",
		Package:    "@anthropic-ai/claude-code",
		Version:    "2.1.312",
		Continue:   "--continue",
		Resume:     "--resume",
		Unattended: []string{"--dangerously-skip-permissions"},
		// CLAUDE_CONFIG_DIR also moves ~/.claude.json into the persistent
		// directory.
		Env:   []string{"CLAUDE_CONFIG_DIR=/home/agent/.claude", "DISABLE_AUTOUPDATER=1"},
		State: []StateDir{{Name: "claude", Path: "/home/agent/.claude"}},
	},
	{
		Name:       "copilot",
		Title:      "GitHub Copilot CLI",
		Binary:     "copilot",
		Package:    "@github/copilot",
		Version:    "1.0.105",
		Continue:   "--continue",
		Resume:     "--resume",
		Unattended: []string{"--allow-all"},
		Env:        []string{"COPILOT_AUTO_UPDATE=false"},
		State:      []StateDir{{Name: "copilot", Path: "/home/agent/.copilot"}},
	},
	{
		Name:       "omo",
		Title:      "OmO",
		Binary:     "omo",
		Package:    "omo-ai",
		Version:    "5.1.48",
		Continue:   "--continue",
		Resume:     "--session",
		Unattended: []string{"--permission-preset", "full-access"},
		// The image runs Node.js; OMO_RUNTIME=node silences the Bun notice.
		Env:        []string{"PI_SKIP_VERSION_CHECK=1", "OMO_RUNTIME=node"},
		State:      []StateDir{{Name: "omo", Path: "/home/agent/.omo"}},
	},
}

// Lookup returns the built-in harness name.
func Lookup(name string) (Harness, bool) {
	i := slices.IndexFunc(harnesses, func(h Harness) bool { return h.Name == name })
	if i < 0 {
		return Harness{}, false
	}
	return harnesses[i], true
}

// Names lists the built-in harnesses.
func Names() []string {
	names := make([]string, len(harnesses))
	for i, h := range harnesses {
		names[i] = h.Name
	}
	return names
}

// All returns the built-in harnesses.
func All() []Harness { return slices.Clone(harnesses) }

// DefaultMounts returns the mounts every workspace of the harness starts
// with.
func (h Harness) DefaultMounts() []string {
	return append(slices.Clone(h.Mounts), sharedMounts...)
}

// Command returns the command line of a new agent session: resume the
// conversation id when set, otherwise continue the last one when cont is
// set, followed by args from the configuration.
func (h Harness) Command(args []string, id string, cont bool) []string {
	cmd := append([]string{h.Binary}, h.Unattended...)
	switch {
	case id != "":
		cmd = append(cmd, h.Resume, id)
	case cont:
		cmd = append(cmd, h.Continue)
	}
	return append(cmd, args...)
}
