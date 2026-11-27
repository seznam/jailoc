package agent

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"opencode", "opencode1", "opencode2", "claude", "copilot", "omo", "agy"} {
		h, ok := Lookup(name)
		if !ok || h.Name != name {
			t.Fatalf("Lookup(%q) = %+v, %v", name, h, ok)
		}
	}
	if _, ok := Lookup("vim"); ok {
		t.Fatal("Lookup(vim) found a harness")
	}
	for name, want := range map[string]string{"opencode": "opencode2", "opencode2": "opencode2", "claude": "claude"} {
		if h, _ := Lookup(name); h.Builtin != want {
			t.Errorf("Lookup(%q).Builtin = %q, want %q", name, h.Builtin, want)
		}
	}
	if _, ok := Lookup(Default); !ok {
		t.Fatalf("default harness %q is not built in", Default)
	}
	alias, _ := Lookup("opencode")
	v2, _ := Lookup("opencode2")
	alias.Name = v2.Name
	if !reflect.DeepEqual(alias, v2) {
		t.Fatalf("Lookup(opencode) = %+v, want opencode2 %+v", alias, v2)
	}
	if !slices.Contains(Names(), "opencode") {
		t.Fatalf("Names() = %v, want the opencode alias", Names())
	}
}

func TestHarnessDefinitions(t *testing.T) {
	t.Parallel()

	stateNames := map[string]string{"cache": "jailoc"}
	for _, h := range All() {
		t.Run(h.Name, func(t *testing.T) {
			if h.Binary == "" || h.Version == "" || h.Continue == "" || h.Resume == "" {
				t.Fatalf("incomplete harness: %+v", h)
			}
			if (h.Package == "") == (h.Release == nil) {
				t.Fatalf("harness %s needs either an npm package or a release", h.Name)
			}
			if r := h.Release; r != nil {
				if r.Repo == "" || r.Binary == "" || !strings.Contains(r.Asset, "{arch}") || r.Version != h.Version {
					t.Fatalf("incomplete release: %+v", r)
				}
				for _, arch := range ReleaseArchs {
					if len(r.SHA256[arch]) != 64 {
						t.Fatalf("release checksum of %s = %q", arch, r.SHA256[arch])
					}
				}
			}
			if strings.HasPrefix(h.Version, "v") {
				t.Fatalf("version %q must be an npm version without v", h.Version)
			}
			if len(h.State) == 0 {
				t.Fatal("harness persists no state")
			}
			for _, s := range h.State {
				if owner, ok := stateNames[s.Name]; ok {
					t.Fatalf("state dir %q of %s already used by %s", s.Name, h.Name, owner)
				}
				stateNames[s.Name] = h.Name
				if !strings.HasPrefix(s.Path, "/home/agent/") {
					t.Fatalf("state dir %q outside the agent home", s.Path)
				}
			}
			for _, kv := range h.Env {
				if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
					t.Fatalf("invalid env entry %q", kv)
				}
			}
			if len(h.Endpoints) == 0 {
				t.Fatal("harness has no endpoints")
			}
		})
	}
}

func TestCommand(t *testing.T) {
	t.Parallel()

	claude, _ := Lookup("claude")
	omo, _ := Lookup("omo")
	copilot, _ := Lookup("copilot")
	agy, _ := Lookup("agy")
	opencode, _ := Lookup("opencode")
	opencode1, _ := Lookup("opencode1")
	tests := []struct {
		name string
		h    Harness
		args []string
		id   string
		cont bool
		want []string
	}{
		{"opencode plain", opencode, nil, "", false, []string{"opencode", "--standalone", "--auto"}},
		{"opencode session", opencode, nil, "ses_1", false, []string{"opencode", "--standalone", "--auto", "--session", "ses_1"}},
		{"opencode continue", opencode, nil, "", true, []string{"opencode", "--standalone", "--auto", "--continue"}},
		{"opencode1 plain", opencode1, nil, "", false, []string{"opencode"}},
		{"opencode1 session", opencode1, nil, "ses_1", false, []string{"opencode", "--session", "ses_1"}},
		{"claude plain", claude, nil, "", false, []string{"claude", "--dangerously-skip-permissions", "--settings", PluginDir + "/claude-settings.json"}},
		{"claude resume with args", claude, []string{"--model", "opus"}, "abc", false, []string{"claude", "--dangerously-skip-permissions", "--settings", PluginDir + "/claude-settings.json", "--resume", "abc", "--model", "opus"}},
		{"claude continue", claude, nil, "", true, []string{"claude", "--dangerously-skip-permissions", "--settings", PluginDir + "/claude-settings.json", "--continue"}},
		{"omo plain", omo, nil, "", false, []string{"omo", "--permission-preset", "full-access", "--extension", PluginDir + "/omo.js"}},
		{"copilot plain", copilot, nil, "", false, []string{"copilot", "--allow-all"}},
		{"agy conversation", agy, nil, "c1", false, []string{"agy", "--dangerously-skip-permissions", "--conversation", "c1"}},
		{"agy continue", agy, nil, "", true, []string{"agy", "--dangerously-skip-permissions", "--continue"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.h.Command(tt.args, tt.id, tt.cont); !slices.Equal(got, tt.want) {
				t.Fatalf("Command() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultMounts(t *testing.T) {
	t.Parallel()

	opencode, _ := Lookup("opencode")
	claude, _ := Lookup("claude")
	if got := claude.DefaultMounts(); !slices.Equal(got, []string{"~/.agents:/home/agent/.agents:ro"}) {
		t.Fatalf("claude mounts = %q", got)
	}
	got := opencode.DefaultMounts()
	if !slices.Equal(got, []string{"~/.agents:/home/agent/.agents:ro"}) {
		t.Fatalf("opencode mounts = %q", got)
	}
	got[0] = "changed"
	if opencode.DefaultMounts()[0] == "changed" {
		t.Fatal("DefaultMounts shares its backing array")
	}
}

func TestSettingsFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		plugin  TUIPlugin
		file    string
		harness string
		ok      bool
	}{
		{CopilotStatusLine, "settings.json", "copilot", true},
		{AgyStatusLine, "antigravity-cli/settings.json", "agy", true},
		{ClaudeStatusLine, "", "", false},
		{NoTUIPlugin, "", "", false},
	}
	for _, tt := range tests {
		file, harness, ok := tt.plugin.SettingsFile()
		if file != tt.file || harness != tt.harness || ok != tt.ok {
			t.Fatalf("SettingsFile(%d) = %q, %q, %v; want %q, %q, %v", tt.plugin, file, harness, ok, tt.file, tt.harness, tt.ok)
		}
	}
}

func TestCommandDefaultArgs(t *testing.T) {
	t.Parallel()

	opencode, _ := Lookup("opencode")
	omo, _ := Lookup("omo")
	claude, _ := Lookup("claude")

	cleared := opencode
	cleared.DefaultArgs = nil
	replaced := opencode
	replaced.DefaultArgs = []string{"--mine"}
	omoCleared := omo
	omoCleared.DefaultArgs = []string{}
	claudeReplaced := claude
	claudeReplaced.DefaultArgs = []string{"--permission-mode", "plan"}

	if !slices.Equal(opencode.RequiredArgs, []string{"--standalone"}) || !slices.Equal(opencode.DefaultArgs, []string{"--auto"}) {
		t.Fatalf("opencode required=%q default=%q, want [--standalone] [--auto]", opencode.RequiredArgs, opencode.DefaultArgs)
	}

	tests := []struct {
		name string
		h    Harness
		args []string
		id   string
		cont bool
		want []string
	}{
		{"cleared keeps required", cleared, nil, "", false, []string{"opencode", "--standalone"}},
		{"cleared keeps resume and args", cleared, []string{"-v"}, "ses_1", false, []string{"opencode", "--standalone", "--session", "ses_1", "-v"}},
		{"cleared keeps continue", cleared, nil, "", true, []string{"opencode", "--standalone", "--continue"}},
		{"replaced keeps required", replaced, []string{"-v"}, "", true, []string{"opencode", "--standalone", "--mine", "--continue", "-v"}},
		{"cleared keeps plugin args", omoCleared, []string{"-v"}, "", false, []string{"omo", "--extension", PluginDir + "/omo.js", "-v"}},
		{"replaced keeps plugin args", claudeReplaced, nil, "abc", false, []string{"claude", "--permission-mode", "plan", "--settings", PluginDir + "/claude-settings.json", "--resume", "abc"}},
		{"user args may repeat a default", opencode, []string{"--auto"}, "", false, []string{"opencode", "--standalone", "--auto", "--auto"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.h.Command(tt.args, tt.id, tt.cont); !slices.Equal(got, tt.want) {
				t.Fatalf("Command() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderEndpoints(t *testing.T) {
	t.Parallel()

	h, ok := Lookup("opencode")
	if !ok {
		t.Fatal("opencode harness not found")
	}
	tests := []struct {
		name    string
		answer  string
		want    []string
		wantErr string
	}{
		{name: "empty selects all", answer: "", want: h.Endpoints},
		{name: "one provider keeps shared endpoints", answer: "anthropic", want: []string{"api.anthropic.com", "models.dev"}},
		{name: "several providers in endpoint order", answer: "OpenAI, anthropic anthropic", want: []string{"api.anthropic.com", "api.openai.com", "auth.openai.com", "chatgpt.com", "models.dev"}},
		{name: "unknown provider", answer: "anthropic,mistral", wantErr: `unknown provider "mistral"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			names, err := h.ParseProviders(tc.answer)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseProviders(%q) error = %v, want %q", tc.answer, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProviders(%q): %v", tc.answer, err)
			}
			got, err := h.ProviderEndpoints(names)
			if err != nil {
				t.Fatalf("ProviderEndpoints(%v): %v", names, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ProviderEndpoints(%v) = %v, want %v", names, got, tc.want)
			}
		})
	}

	if _, err := h.ProviderEndpoints([]string{"mistral"}); err == nil {
		t.Error("ProviderEndpoints with an unknown provider: want error")
	}
}

func TestTmpfsDirs(t *testing.T) {
	t.Parallel()

	omo, ok := Lookup("omo")
	if !ok {
		t.Fatal("omo harness not found")
	}
	if want := []string{"/home/agent/.omo/agent/rpc"}; !slices.Equal(omo.TmpfsDirs, want) {
		t.Fatalf("omo.TmpfsDirs = %q, want %q", omo.TmpfsDirs, want)
	}
	for _, h := range All() {
		if h.Name == "omo" {
			continue
		}
		if len(h.TmpfsDirs) != 0 {
			t.Errorf("%s.TmpfsDirs = %q, want empty", h.Name, h.TmpfsDirs)
		}
	}
}

func TestSandboxEnv(t *testing.T) {
	t.Parallel()

	claudeEnv := []string{
		"CLAUDE_CONFIG_DIR=/home/agent/.claude",
		"CLAUDE_CODE_SANDBOXED=1",
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"IS_SANDBOX=1",
	}
	for _, h := range All() {
		t.Run(h.Name, func(t *testing.T) {
			t.Parallel()

			if h.Name != "claude" {
				for _, kv := range h.Env {
					if strings.HasPrefix(kv, "IS_SANDBOX=") {
						t.Errorf("%s.Env contains %q, want no IS_SANDBOX", h.Name, kv)
					}
				}
				return
			}
			for _, want := range claudeEnv {
				if !slices.Contains(h.Env, want) {
					t.Errorf("claude.Env = %q, want %q", h.Env, want)
				}
			}
		})
	}
}
