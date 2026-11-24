// Package agent defines the coding agent harnesses jailoc runs in workspace
// VMs: how each is installed, started and resumed, and which of its home
// directories survive restarts.
package agent

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
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
	// AgyStatusLine: Antigravity CLI runs the status line command that
	// jailoc adds to its settings.json.
	AgyStatusLine
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

// SettingsFile returns the settings file of the agent, relative to its
// state directory, to which jailoc adds the status line, and the harness
// name of its status line command. ok is false when jailoc doesn't
// edit the agent's settings.
func (p TUIPlugin) SettingsFile() (file, harness string, ok bool) {
	switch p {
	case CopilotStatusLine:
		return "settings.json", "copilot", true
	case AgyStatusLine:
		return "antigravity-cli/settings.json", "agy", true
	default:
		return "", "", false
	}
}

// Release is a native binary of the agent that the built-in image layer
// downloads from a GitHub release instead of installing an npm package.
type Release struct {
	// Repo is the GitHub repository (owner/name) that publishes the release;
	// its tags are the agent versions.
	Repo string
	// Asset is the name of the release's tar.gz asset, with {arch} for the
	// architecture: x64 or arm64.
	Asset string
	// Binary is the file in the asset that is installed as the agent's
	// Binary.
	Binary string
	// Version is the release SHA256 is for.
	Version string
	// SHA256 are the checksums of the assets of Version by architecture.
	SHA256 map[string]string
}

// AssetName returns the release asset of arch.
func (r Release) AssetName(arch string) string {
	return strings.ReplaceAll(r.Asset, "{arch}", arch)
}

// ReleaseArchs are the architectures of a Release, as in its asset names.
var ReleaseArchs = []string{"x64", "arm64"}

// StateDir is a home directory of the agent kept across VM restarts.
type StateDir struct {
	// Name is the directory under the workspace's persistent home on the
	// host.
	Name string
	// Path is the directory in the guest.
	Path string
}

// ConfigFile is a configuration file or directory of the agent under the
// home directory, copied read-only from the host home to the same place in
// the guest home.
type ConfigFile struct {
	// Path is relative to the home directory.
	Path string
	// Exclude are entries of the directory, relative to it, left out of
	// the copy.
	Exclude []string
	// Extra are files added to a directory copy, by relative path, when
	// the source does not have them or they are excluded.
	Extra map[string]string
}

// StateDefault is a top-level value of a JSON file in a state directory that
// jailoc sets when the file does not, before the VM starts.
type StateDefault struct {
	// File is the guest path of the file.
	File string
	// Key is the top-level key and Value its JSON value.
	Key   string
	Value string
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
	// Release, when set instead of Package, is the native binary of
	// Version that the built-in image layer installs.
	Release *Release
	// Continue is the flag that continues the agent's last conversation.
	Continue string
	// Resume is the flag that takes the conversation ID to resume.
	Resume string
	// RequiredArgs are the flags jailoc needs for the session to work; the
	// configuration cannot remove them.
	RequiredArgs []string
	// DefaultArgs are the flags that let the agent act without asking for
	// permission (the VM is the security boundary). The default_args setting
	// of the agent replaces them.
	DefaultArgs []string
	// Env is set in the VM for every session.
	Env []string
	// State are the home directories that survive VM restarts.
	State []StateDir
	// StateDefaults are values jailoc sets in files of the State
	// directories.
	StateDefaults []StateDefault
	// Mounts are the default mounts added before [defaults] and workspace
	// mounts.
	Mounts []string
	// ConfigFiles are the agent's host configuration files that jailoc
	// copies read-only into the guest (import_agent_config).
	ConfigFiles []ConfigFile
	// TUIPlugin is how the agent loads the jailoc TUI plugin.
	TUIPlugin TUIPlugin
	// ConfigDir is the guest path of the agent's configuration directory and
	// ConfigGitignore the .gitignore the agent writes there on startup. jailoc
	// adds it to a read-only copy or mount of the directory.
	ConfigDir       string
	ConfigGitignore string
	// Installs, when set, returns what the agent installs before its
	// interface starts, from its environment env and working directory dir:
	// jailoc shows it while the agent starts.
	Installs func(env []string, dir string) []string
	// Endpoints are the API and auth hostnames the agent contacts; they
	// become the "agent" allow rule on TCP port 443.
	Endpoints []string
	// ExplicitEndpoints marks a multi-provider agent: its Endpoints are not
	// allowed by default, the user lists the providers they use in
	// network.agent_endpoints (setup and migrate offer Providers).
	ExplicitEndpoints bool
	// Providers groups the model provider endpoints of a multi-provider
	// agent; Endpoints not in any provider are needed with every provider.
	Providers []Provider
	// Diagnose is the guest command (without the binary) that prints
	// unbuffered startup and configuration errors when the agent exits
	// before drawing or after a server error.
	Diagnose []string
}

// sharedMounts are default mounts of every harness.
var sharedMounts = []string{"~/.agents:/home/agent/.agents:ro"}

// omoConfig are the user configuration files OmO and the OmO OpenCode 1 plugin
// read from ~/.omo.
var omoConfig = []ConfigFile{
	{Path: ".omo/omo.jsonc"},
	{Path: ".omo/omo.json"},
}

// openCodeConfig is the configuration OpenCode 1 and 2 share. node_modules
// holds packages OpenCode installed for the host's platform; service.json
// holds the host background service's password and environment.
var openCodeConfig = []ConfigFile{{
	Path:    ".config/opencode",
	Exclude: []string{"node_modules", "service.json"},
}}

var openCode1Config = append(slices.Clone(openCodeConfig), omoConfig...)

// openCode2Config disables the OpenCode 2 background service in the read-only
// copy so CLI commands run without --standalone (such as opencode debug config)
// use a private server instead of failing with EROFS while writing service.json.
var openCode2Config = []ConfigFile{{
	Path:    ".config/opencode",
	Exclude: []string{"node_modules", "service.json"},
	Extra:   map[string]string{"service.json": openCode2ServiceConfig},
}}

const (
	openCodeConfigDir = "/home/agent/.config/opencode"
	// OpenCode (v1.14.22+) writes this file on startup and crashes with
	// EROFS on a read-only config directory without it.
	// See: https://github.com/anomalyco/opencode/issues/23040
	openCodeConfigGitignore = "node_modules\npackage.json\npackage-lock.json\nbun.lock\n.gitignore\n"
	// OpenCode 2's background service writes a generated password to
	// service.json in the config directory and crashes with EROFS when it is
	// read-only. Disabling the service makes every CLI command run standalone.
	// See: https://github.com/anomalyco/opencode/issues/56359
	openCode2ServiceConfig = "{\"disabled\":true}\n"
	openCodePrintLogsFlag  = "--print-logs"
)

// Provider is a model provider and the endpoints the agent reaches it at.
type Provider struct {
	Name      string
	Endpoints []string
}

// llmProviders are the model providers of multi-provider coding agents
// (OpenCode and OmO), named after their OpenCode provider IDs.
var llmProviders = []Provider{
	{Name: "anthropic", Endpoints: []string{"api.anthropic.com"}},
	{Name: "openai", Endpoints: []string{"api.openai.com", "auth.openai.com", "chatgpt.com"}},
	{Name: "google", Endpoints: []string{"generativelanguage.googleapis.com", "oauth2.googleapis.com"}},
	{Name: "github-copilot", Endpoints: []string{"api.githubcopilot.com", "*.githubcopilot.com"}},
	{Name: "openrouter", Endpoints: []string{"openrouter.ai"}},
	{Name: "opencode", Endpoints: []string{"opencode.ai", "*.opencode.ai"}},
}

// llmEndpoints are the model and auth endpoints of llmProviders plus the
// models.dev model catalog that OpenCode loads with every provider.
var llmEndpoints = append(providerEndpoints(llmProviders), "models.dev")

func providerEndpoints(providers []Provider) []string {
	var out []string
	for _, p := range providers {
		out = append(out, p.Endpoints...)
	}
	return out
}

// ProviderNames returns the names of the agent's providers.
func (h Harness) ProviderNames() []string {
	names := make([]string, len(h.Providers))
	for i, p := range h.Providers {
		names[i] = p.Name
	}
	return names
}

// ProviderEndpoints returns the agent's Endpoints for the providers called
// names, plus the endpoints shared by all providers, in Endpoints order.
// Nil names select every provider.
func (h Harness) ProviderEndpoints(names []string) ([]string, error) {
	if names == nil {
		return slices.Clone(h.Endpoints), nil
	}
	skip := map[string]bool{}
	for _, p := range h.Providers {
		if !slices.Contains(names, p.Name) {
			for _, e := range p.Endpoints {
				skip[e] = true
			}
		}
	}
	for _, n := range names {
		if !slices.Contains(h.ProviderNames(), n) {
			return nil, fmt.Errorf("unknown provider %q (choose from %s)", n, strings.Join(h.ProviderNames(), ", "))
		}
	}
	out := []string{}
	for _, e := range h.Endpoints {
		if !skip[e] {
			out = append(out, e)
		}
	}
	return out, nil
}

// ParseProviders splits a comma or space separated provider answer and
// checks the names against h. An empty answer selects every provider (nil).
func (h Harness) ParseProviders(answer string) ([]string, error) {
	fields := strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(fields) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(f)
		if !slices.Contains(h.ProviderNames(), f) {
			return nil, fmt.Errorf("unknown provider %q (choose from %s)", f, strings.Join(h.ProviderNames(), ", "))
		}
		if !slices.Contains(names, f) {
			names = append(names, f)
		}
	}
	return names, nil
}

var harnesses = []Harness{
	{
		Name:     "opencode1",
		Title:    "OpenCode 1",
		Binary:   "opencode",
		Package:  "opencode-ai",
		Version:  "1.19.5",
		Continue: "--continue",
		Resume:   "--session",
		Env:      []string{"OPENCODE_DISABLE_AUTOUPDATE=1"},
		State: []StateDir{
			{Name: "opencode-data", Path: "/home/agent/.local/share/opencode"},
			{Name: "opencode-state", Path: "/home/agent/.local/state/opencode"},
			// OpenCode 1 commonly runs with the OmO (oh-my-openagent)
			// plugin, which writes migration locks, configuration and
			// state to ~/.omo.
			{Name: "opencode1-omo", Path: "/home/agent/.omo"},
		},
		ConfigFiles:       openCode1Config,
		TUIPlugin:         OpenCode1TUI,
		ConfigDir:         openCodeConfigDir,
		ConfigGitignore:   openCodeConfigGitignore,
		Installs:          openCode1Installs,
		Endpoints:         llmEndpoints,
		ExplicitEndpoints: true,
		Providers:         llmProviders,
		Diagnose:          []string{openCodePrintLogsFlag},
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
		RequiredArgs: []string{"--standalone"},
		DefaultArgs:  []string{"--auto"},
		Env:          []string{"OPENCODE_DISABLE_AUTOUPDATE=1"},
		// Separate from OpenCode 1: both keep their database in the same
		// guest paths, in incompatible formats.
		State: []StateDir{
			{Name: "opencode2-data", Path: "/home/agent/.local/share/opencode"},
			{Name: "opencode2-state", Path: "/home/agent/.local/state/opencode"},
		},
		ConfigFiles:       openCode2Config,
		TUIPlugin:         OpenCode2TUI,
		ConfigDir:         openCodeConfigDir,
		ConfigGitignore:   openCodeConfigGitignore,
		Endpoints:         llmEndpoints,
		ExplicitEndpoints: true,
		Providers:         llmProviders,
		Diagnose:          []string{"--standalone", openCodePrintLogsFlag},
	},
	{
		Name:        "claude",
		Title:       "Claude Code",
		Binary:      "claude",
		Package:     "@anthropic-ai/claude-code",
		Version:     "2.1.318",
		Continue:    "--continue",
		Resume:      "--resume",
		DefaultArgs: []string{"--dangerously-skip-permissions"},
		// CLAUDE_CONFIG_DIR also moves ~/.claude.json into the persistent
		// directory. CLAUDE_CODE_SANDBOXED makes Claude Code trust the
		// working directory without its trust prompt, which background
		// sessions (--bg, used to switch sessions) otherwise require.
		Env:   []string{"CLAUDE_CONFIG_DIR=/home/agent/.claude", "CLAUDE_CODE_SANDBOXED=1", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"},
		State: []StateDir{{Name: "claude", Path: "/home/agent/.claude"}},
		// Background sessions refuse --dangerously-skip-permissions until
		// its disclaimer was accepted interactively once.
		StateDefaults: []StateDefault{{File: "/home/agent/.claude/.claude.json", Key: "bypassPermissionsModeAccepted", Value: "true"}},
		ConfigFiles: []ConfigFile{
			{Path: ".claude/CLAUDE.md"},
			{Path: ".claude/RTK.md"},
			{Path: ".claude/settings.json"},
			{Path: ".claude/agents"},
			{Path: ".claude/commands"},
			{Path: ".claude/hooks"},
			{Path: ".claude/skills"},
			{Path: ".claude/output-styles"},
		},
		TUIPlugin: ClaudeStatusLine,
		Endpoints: []string{"api.anthropic.com", "claude.ai", "console.anthropic.com", "platform.claude.com"},
	},
	{
		Name:        "copilot",
		Title:       "GitHub Copilot CLI",
		Binary:      "copilot",
		Package:     "@github/copilot",
		Version:     "1.0.111",
		Continue:    "--continue",
		Resume:      "--resume",
		DefaultArgs: []string{"--allow-all"},
		Env:         []string{"COPILOT_AUTO_UPDATE=false"},
		State:       []StateDir{{Name: "copilot", Path: "/home/agent/.copilot"}},
		// Not config.json or settings.json: Copilot CLI writes its login
		// and preferences there, and jailoc sets the status line and
		// storeTokenPlaintext in settings.json.
		ConfigFiles: []ConfigFile{
			{Path: ".copilot/copilot-instructions.md"},
			{Path: ".copilot/agents"},
			{Path: ".copilot/hooks"},
			{Path: ".copilot/skills"},
			{Path: ".copilot/mcp-config.json"},
		},
		TUIPlugin: CopilotStatusLine,
		Endpoints: []string{"api.github.com", "github.com", "api.githubcopilot.com", "*.githubcopilot.com", "copilot-proxy.githubusercontent.com"},
	},
	{
		Name:        "omo",
		Title:       "OmO",
		Binary:      "omo",
		Package:     "omo-ai",
		Version:     "5.1.60",
		Continue:    "--continue",
		Resume:      "--session",
		DefaultArgs: []string{"--permission-preset", "full-access"},
		// The image runs Node.js; OMO_RUNTIME=node silences the Bun notice.
		Env:               []string{"PI_SKIP_VERSION_CHECK=1", "OMO_RUNTIME=node"},
		State:             []StateDir{{Name: "omo", Path: "/home/agent/.omo"}},
		ConfigFiles:       omoConfig,
		TUIPlugin:         OmOExtension,
		Endpoints:         llmEndpoints,
		ExplicitEndpoints: true,
		Providers:         llmProviders,
	},
	{
		Name:    "agy",
		Title:   "Antigravity CLI",
		Binary:  "agy",
		Version: "1.5.0",
		Release: &Release{
			Repo:    "google-antigravity/antigravity-cli",
			Asset:   "agy_cli_linux_{arch}.tar.gz",
			Binary:  "antigravity",
			Version: "1.5.0",
			SHA256: map[string]string{
				"x64":   "f65e365a67be49abdaf92f5669c3157bd4b9c569f6b9f448340b00ca261b0f01",
				"arm64": "87e73a5ab4ecb3a9668660d259dafd13f5443149da0113664d59129a21a62ba4",
			},
		},
		Continue:    "--continue",
		Resume:      "--conversation",
		DefaultArgs: []string{"--dangerously-skip-permissions"},
		Env:         []string{"AGY_CLI_DISABLE_AUTO_UPDATE=1"},
		// Settings, conversations and the OAuth token, which falls back to
		// a file without a keyring. Gemini CLI shares ~/.gemini, so only
		// the global instructions are copied from the host.
		State:       []StateDir{{Name: "antigravity", Path: "/home/agent/.gemini"}},
		ConfigFiles: []ConfigFile{{Path: ".gemini/GEMINI.md"}},
		TUIPlugin:   AgyStatusLine,
		Endpoints:   []string{"generativelanguage.googleapis.com", "cloudcode-pa.googleapis.com", "oauth2.googleapis.com", "accounts.google.com"},
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

// Command returns the command line of a new agent session: the required and
// default flags, the flags that load the jailoc plugin, then resume the conversation id when set,
// otherwise continue the last one when cont is set, followed by args from
// the configuration.
func (h Harness) Command(args []string, id string, cont bool) []string {
	cmd := append([]string{h.Binary}, h.RequiredArgs...)
	cmd = append(cmd, h.DefaultArgs...)
	cmd = append(cmd, h.TUIPlugin.Args()...)
	switch {
	case id != "":
		cmd = append(cmd, h.Resume, id)
	case cont:
		cmd = append(cmd, h.Continue)
	}
	return append(cmd, args...)
}
