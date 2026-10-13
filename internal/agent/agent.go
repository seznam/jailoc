// Package agent defines the coding agent harnesses jailoc runs in workspace
// VMs: how each is installed, started and resumed, and which of its home
// directories survive restarts.
package agent

import (
	"maps"
	"slices"
)

// Default is the harness of a workspace that does not set agent.
const Default = "opencode"

// aliases are agent names that select another built-in harness.
var aliases = map[string]string{"opencode": "opencode2"}

// PluginDir is the guest directory with the jailoc plugin files of the
// workspace's agent.
const PluginDir = "/etc/jailoc-tui-plugin"

// TUIPlugin is how an agent shows the jailoc workspace indicator.
type TUIPlugin int

const (
	// NoTUIPlugin: the agent shows no jailoc indicator.
	NoTUIPlugin TUIPlugin = iota
	// OpenCode1TUI: OpenCode 1 loads the plugin listed in the tui.json that
	// OPENCODE_TUI_CONFIG points at.
	OpenCode1TUI
	// OpenCode2TUI: OpenCode 2 loads the plugins listed in the cli.json
	// content of OPENCODE_CLI_CONFIG_CONTENT.
	OpenCode2TUI
	// OmOExtension: OmO loads the extension passed with --extension, which
	// sets a footer status.
	OmOExtension
	// ClaudeStatusLine: Claude Code runs the status line command of the
	// settings file passed with --settings.
	ClaudeStatusLine
	// CopilotStatusLine: GitHub Copilot CLI runs the status line command
	// that jailoc adds to its settings.json.
	CopilotStatusLine
)

// Env returns the environment variable that makes the agent load the
// plugin, or "" when the plugin needs none.
func (p TUIPlugin) Env() string {
	switch p {
	case OpenCode1TUI:
		return "OPENCODE_TUI_CONFIG"
	case OpenCode2TUI:
		return "OPENCODE_CLI_CONFIG_CONTENT"
	default:
		return ""
	}
}

// Args returns the agent flags that load the plugin.
func (p TUIPlugin) Args() []string {
	switch p {
	case OmOExtension:
		return []string{"--extension", PluginDir + "/omo.js"}
	case ClaudeStatusLine:
		return []string{"--settings", PluginDir + "/claude-settings.json"}
	default:
		return nil
	}
}

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
	// TUIPlugin is how the agent loads the jailoc TUI plugin.
	TUIPlugin TUIPlugin
	// ConfigDir is the guest path of the agent's configuration directory and
	// ConfigGitignore the .gitignore the agent writes there on startup. jailoc
	// creates it on the host when the directory is mounted read-only.
	ConfigDir       string
	ConfigGitignore string
}

// sharedMounts are default mounts of every harness.
var sharedMounts = []string{"~/.agents:/home/agent/.agents:ro"}

// OpenCode 1 and 2 share the configuration directory.
var openCodeMounts = []string{
	"~/.config/opencode:/home/agent/.config/opencode:rw",
	"~/.opencode:/home/agent/.opencode:rw",
}

const (
	openCodeConfigDir = "/home/agent/.config/opencode"
	// OpenCode (v1.14.22+) writes this file on startup and crashes with
	// EROFS on a read-only config directory without it.
	// See: https://github.com/anomalyco/opencode/issues/23040
	openCodeConfigGitignore = "node_modules\npackage.json\npackage-lock.json\nbun.lock\n.gitignore\n"
)

var harnesses = []Harness{
	{
		Name:     "opencode1",
		Title:    "OpenCode 1",
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
		Mounts:          openCodeMounts,
		TUIPlugin:       OpenCode1TUI,
		ConfigDir:       openCodeConfigDir,
		ConfigGitignore: openCodeConfigGitignore,
	},
	{
		Name:     "opencode2",
		Title:    "OpenCode 2",
		Binary:   "opencode",
		Package:  "@opencode/cli",
		Version:  "2.2.5",
		Continue: "--continue",
		Resume:   "--session",
		// --standalone runs a private server per session instead of the
		// shared background service, so every session gets its own
		// environment, secrets included.
		Unattended: []string{"--standalone", "--auto"},
		Env:        []string{"OPENCODE_DISABLE_AUTOUPDATE=1"},
		// Separate from OpenCode 1: both keep their database in the same
		// guest paths, in incompatible formats.
		State: []StateDir{
			{Name: "opencode2-data", Path: "/home/agent/.local/share/opencode"},
			{Name: "opencode2-state", Path: "/home/agent/.local/state/opencode"},
		},
		Mounts:          openCodeMounts,
		TUIPlugin:       OpenCode2TUI,
		ConfigDir:       openCodeConfigDir,
		ConfigGitignore: openCodeConfigGitignore,
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
		Env:       []string{"CLAUDE_CONFIG_DIR=/home/agent/.claude", "DISABLE_AUTOUPDATER=1"},
		State:     []StateDir{{Name: "claude", Path: "/home/agent/.claude"}},
		TUIPlugin: ClaudeStatusLine,
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
		TUIPlugin:  CopilotStatusLine,
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
		Env:       []string{"PI_SKIP_VERSION_CHECK=1", "OMO_RUNTIME=node"},
		State:     []StateDir{{Name: "omo", Path: "/home/agent/.omo"}},
		TUIPlugin: OmOExtension,
	},
}

// Lookup returns the built-in harness name. An alias returns the harness it
// selects under the alias name.
func Lookup(name string) (Harness, bool) {
	target := name
	if a, ok := aliases[name]; ok {
		target = a
	}
	i := slices.IndexFunc(harnesses, func(h Harness) bool { return h.Name == target })
	if i < 0 {
		return Harness{}, false
	}
	h := harnesses[i]
	h.Name = name
	return h, true
}

// Names lists the built-in harnesses and aliases.
func Names() []string {
	names := slices.Sorted(maps.Keys(aliases))
	for _, h := range harnesses {
		names = append(names, h.Name)
	}
	return names
}

// All returns the built-in harnesses, without aliases.
func All() []Harness { return slices.Clone(harnesses) }

// DefaultMounts returns the mounts every workspace of the harness starts
// with.
func (h Harness) DefaultMounts() []string {
	return append(slices.Clone(h.Mounts), sharedMounts...)
}

// Command returns the command line of a new agent session: the flags that
// load the jailoc plugin, then resume the conversation id when set,
// otherwise continue the last one when cont is set, followed by args from
// the configuration.
func (h Harness) Command(args []string, id string, cont bool) []string {
	cmd := append([]string{h.Binary}, h.Unattended...)
	cmd = append(cmd, h.TUIPlugin.Args()...)
	switch {
	case id != "":
		cmd = append(cmd, h.Resume, id)
	case cont:
		cmd = append(cmd, h.Continue)
	}
	return append(cmd, args...)
}
