package workspace_test

import (
	"slices"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func commandOf(t *testing.T, cfg *config.Config, name string, args []string) []string {
	t.Helper()
	r, err := workspace.Resolve(cfg, name)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", name, err)
	}
	return r.Agent.Command(append(slices.Clone(r.AgentArgs), args...), "", false)
}

func TestResolveBuiltinDefaultArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name    string
		agent   string
		setting *[]string
		want    []string
	}{
		{"opencode default unchanged", "opencode", nil, []string{"opencode", "--standalone", "--auto"}},
		{"opencode cleared", "opencode", &[]string{}, []string{"opencode", "--standalone"}},
		{"opencode replaced", "opencode", &[]string{"--mine"}, []string{"opencode", "--standalone", "--mine"}},
		{"opencode2 cleared", "opencode2", &[]string{}, []string{"opencode", "--standalone"}},
		{"opencode1 default unchanged", "opencode1", nil, []string{"opencode"}},
		{"opencode1 replaced", "opencode1", &[]string{"--mine"}, []string{"opencode", "--mine"}},
		{"claude default unchanged", "claude", nil, []string{"claude", "--dangerously-skip-permissions", "--settings", agent.PluginDir + "/claude-settings.json"}},
		{"claude cleared", "claude", &[]string{}, []string{"claude", "--settings", agent.PluginDir + "/claude-settings.json"}},
		{"claude replaced", "claude", &[]string{"--permission-mode", "plan"}, []string{"claude", "--permission-mode", "plan", "--settings", agent.PluginDir + "/claude-settings.json"}},
		{"omo cleared", "omo", &[]string{}, []string{"omo", "--extension", agent.PluginDir + "/omo.js"}},
		{"copilot cleared", "copilot", &[]string{}, []string{"copilot"}},
		{"agy cleared", "agy", &[]string{}, []string{"agy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := config.Workspace{Paths: []string{"/tmp/a"}, Agent: tt.agent}
			if tt.setting != nil {
				ws.Agents = map[string]config.AgentSettings{tt.agent: {DefaultArgs: *tt.setting}}
			}
			cfg := &config.Config{Workspaces: map[string]config.Workspace{"w": ws}}
			if got := commandOf(t, cfg, "w", nil); !slices.Equal(got, tt.want) {
				t.Fatalf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDefaultArgsKeepsArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{Workspaces: map[string]config.Workspace{"w": {
		Paths: []string{"/tmp/a"},
		Agent: "opencode",
		Agents: map[string]config.AgentSettings{"opencode": {
			DefaultArgs: []string{"--mine"},
			Args:        []string{"--model", "x"},
		}},
	}}}
	r, err := workspace.Resolve(cfg, "w")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{"opencode", "--standalone", "--mine", "--session", "s1", "--model", "x"}
	if got := r.Agent.Command(r.AgentArgs, "s1", false); !slices.Equal(got, want) {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestResolveCustomAgentDefaultArgsPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	set := func(v []string) config.AgentSettings { return config.AgentSettings{DefaultArgs: v} }
	custom := func(v []string) config.AgentSettings {
		return config.AgentSettings{From: "claude", DefaultArgs: v}
	}
	// The layers, highest first: workspace selected, defaults selected,
	// workspace base, defaults base, harness default.
	tests := []struct {
		name     string
		wsSel    *config.AgentSettings
		defSel   *config.AgentSettings
		wsBase   *config.AgentSettings
		defBase  *config.AgentSettings
		wantArgs []string
	}{
		{"builtin default", nil, nil, nil, nil, []string{"--dangerously-skip-permissions"}},
		{"defaults base", nil, nil, nil, new(set([]string{"--d-base"})), []string{"--d-base"}},
		{"workspace base over defaults base", nil, nil, new(set([]string{"--w-base"})), new(set([]string{"--d-base"})), []string{"--w-base"}},
		{"defaults selected over bases", nil, new(custom([]string{"--d-sel"})), new(set([]string{"--w-base"})), new(set([]string{"--d-base"})), []string{"--d-sel"}},
		{"workspace selected over all", new(custom([]string{"--w-sel"})), new(custom([]string{"--d-sel"})), new(set([]string{"--w-base"})), new(set([]string{"--d-base"})), []string{"--w-sel"}},
		{"empty workspace selected wins", new(custom([]string{})), new(custom([]string{"--d-sel"})), new(set([]string{"--w-base"})), new(set([]string{"--d-base"})), nil},
		{"nil selected falls through to harness default", new(custom(nil)), nil, nil, nil, []string{"--dangerously-skip-permissions"}},
		{"empty workspace base wins over defaults base", nil, nil, new(set([]string{})), new(set([]string{"--d-base"})), nil},
		{"empty defaults base wins over harness", nil, nil, nil, new(set([]string{})), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := config.Workspace{Paths: []string{"/tmp/a"}, Agent: "mine", Agents: map[string]config.AgentSettings{}}
			defs := config.Defaults{Agents: map[string]config.AgentSettings{}}
			sel := config.AgentSettings{From: "claude"}
			if tt.wsSel != nil {
				ws.Agents["mine"] = *tt.wsSel
			} else {
				ws.Agents["mine"] = sel
			}
			if tt.defSel != nil {
				defs.Agents["mine"] = *tt.defSel
			} else {
				defs.Agents["mine"] = sel
			}
			if tt.wsBase != nil {
				ws.Agents["claude"] = *tt.wsBase
			}
			if tt.defBase != nil {
				defs.Agents["claude"] = *tt.defBase
			}
			cfg := &config.Config{Defaults: defs, Workspaces: map[string]config.Workspace{"w": ws}}
			want := append(append([]string{"claude"}, tt.wantArgs...), "--settings", agent.PluginDir+"/claude-settings.json")
			if got := commandOf(t, cfg, "w", nil); !slices.Equal(got, want) {
				t.Fatalf("command = %q, want %q", got, want)
			}
		})
	}
}

func TestResolveOpenCodeAliasTablesIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"alias": {Paths: []string{"/tmp/a"}, Agent: "opencode", Agents: map[string]config.AgentSettings{
			"opencode2": {DefaultArgs: []string{}},
		}},
		"v2": {Paths: []string{"/tmp/b"}, Agent: "opencode2", Agents: map[string]config.AgentSettings{
			"opencode": {DefaultArgs: []string{}},
		}},
		"own": {Paths: []string{"/tmp/c"}, Agent: "opencode", Agents: map[string]config.AgentSettings{
			"opencode": {DefaultArgs: []string{}},
		}},
	}}
	tests := []struct {
		name string
		want []string
	}{
		{"alias", []string{"opencode", "--standalone", "--auto"}},
		{"v2", []string{"opencode", "--standalone", "--auto"}},
		{"own", []string{"opencode", "--standalone"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandOf(t, cfg, tt.name, nil); !slices.Equal(got, tt.want) {
				t.Fatalf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveDefaultArgsDoesNotMutateHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"cleared": {Paths: []string{"/tmp/a"}, Agent: "opencode", Agents: map[string]config.AgentSettings{"opencode": {DefaultArgs: []string{}}}},
		"plain":   {Paths: []string{"/tmp/b"}, Agent: "opencode"},
	}}
	if _, err := workspace.Resolve(cfg, "cleared"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	h, _ := agent.Lookup("opencode")
	if !slices.Equal(h.DefaultArgs, []string{"--auto"}) || !slices.Equal(h.RequiredArgs, []string{"--standalone"}) {
		t.Fatalf("built-in harness mutated: default=%q required=%q", h.DefaultArgs, h.RequiredArgs)
	}
	want := []string{"opencode", "--standalone", "--auto"}
	if got := commandOf(t, cfg, "plain", nil); !slices.Equal(got, want) {
		t.Fatalf("command after another resolution = %q, want %q", got, want)
	}
}

func TestResolveDefaultArgsDoesNotAliasConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	src := []string{"--mine", "--other"}
	cfg := &config.Config{Workspaces: map[string]config.Workspace{"w": {
		Paths: []string{"/tmp/a"}, Agent: "opencode",
		Agents: map[string]config.AgentSettings{"opencode": {DefaultArgs: src}},
	}}}
	r, err := workspace.Resolve(cfg, "w")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r.Agent.DefaultArgs[0] = "--changed"
	if src[0] != "--mine" {
		t.Fatalf("resolved harness aliases the configuration: %q", src)
	}
	src[1] = "--changed-too"
	if r.Agent.DefaultArgs[1] != "--other" {
		t.Fatalf("configuration change leaked into resolved harness: %q", r.Agent.DefaultArgs)
	}
}
