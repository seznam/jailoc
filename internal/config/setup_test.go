package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2/unstable/edit"
)

func TestIsDefaultFile(t *testing.T) {
	t.Parallel()

	t.Run("missing file is considered default", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "nonexistent.toml")
		got, err := IsDefaultFile(path)
		if err != nil {
			t.Fatalf("IsDefaultFile error: %v", err)
		}
		if !got {
			t.Fatal("expected IsDefaultFile to return true for missing file")
		}
	})

	t.Run("created default file is considered default", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := createDefaultAt(path); err != nil {
			t.Fatalf("createDefaultAt: %v", err)
		}
		got, err := IsDefaultFile(path)
		if err != nil {
			t.Fatalf("IsDefaultFile error: %v", err)
		}
		if !got {
			t.Fatal("expected IsDefaultFile to return true for untouched defaultConfigContent")
		}
	})

	t.Run("modified file is not default", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[workspaces.default]\npaths = [\"/data/proj\"]\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		got, err := IsDefaultFile(path)
		if err != nil {
			t.Fatalf("IsDefaultFile error: %v", err)
		}
		if got {
			t.Fatal("expected IsDefaultFile to return false for customized file")
		}
	})
}

func TestSanitizeWorkspaceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"my-project", "my-project"},
		{"My_Project.v2", "my-project-v2"},
		{"  ---Foo___Bar---  ", "foo-bar"},
		{"___", "default"},
		{"", "default"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeWorkspaceName(tc.in); got != tc.want {
				t.Errorf("SanitizeWorkspaceName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderFirstTime(t *testing.T) {
	t.Parallel()

	t.Run("preserves comments and sets defaults and workspace", func(t *testing.T) {
		t.Parallel()
		opts := FirstTimeOptions{
			Agent:         "omo",
			GitConfig:     true,
			SSHAuthSock:   true,
			GPGAgent:      false,
			EnableDocker:  true,
			CPU:           4,
			Memory:        "8g",
			AllowInternet: false,
			ExtraHosts:    []string{"gitlab.example.com", "10.20.0.0/16"},
			WorkspaceName: "default",
			WorkspacePath: "/data/my-app",
		}

		out, err := RenderFirstTime(opts)
		if err != nil {
			t.Fatalf("RenderFirstTime failed: %v", err)
		}

		text := string(out)
		if !strings.Contains(text, "# jailoc configuration") {
			t.Errorf("expected header comments to be preserved, got:\n%s", text)
		}

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		if err := WriteBytes(cfgPath, out); err != nil {
			t.Fatalf("WriteBytes: %v", err)
		}

		cfg, err := LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}

		if cfg.Defaults.Agent != "omo" {
			t.Errorf("Defaults.Agent = %q, want omo", cfg.Defaults.Agent)
		}
		if !cfg.Defaults.SSHAuthSock {
			t.Error("expected Defaults.SSHAuthSock = true")
		}
		if cfg.Defaults.EnableDocker == nil || !*cfg.Defaults.EnableDocker {
			t.Error("expected Defaults.EnableDocker = true")
		}
		if cfg.Defaults.CPU == nil || *cfg.Defaults.CPU != 4 {
			t.Errorf("Defaults.CPU = %v, want 4", cfg.Defaults.CPU)
		}
		if cfg.Defaults.Memory == nil || *cfg.Defaults.Memory != "8g" {
			t.Errorf("Defaults.Memory = %v, want 8g", cfg.Defaults.Memory)
		}
		rules := cfg.Defaults.Network.Get().Rules
		if len(rules) != 2 || rules[1].Name != "custom" {
			t.Fatalf("unexpected network rules: %+v", rules)
		}
		ws := cfg.Workspaces["default"]
		if len(ws.Paths) != 1 || ws.Paths[0] != "/data/my-app" {
			t.Errorf("default workspace paths = %v, want [/data/my-app]", ws.Paths)
		}
	})

	t.Run("internet preset and custom initial workspace name", func(t *testing.T) {
		t.Parallel()
		opts := FirstTimeOptions{
			Agent:         "claude",
			GitConfig:     false,
			AllowInternet: true,
			WorkspaceName: "work",
			WorkspacePath: "/data/work",
		}

		out, err := RenderFirstTime(opts)
		if err != nil {
			t.Fatalf("RenderFirstTime failed: %v", err)
		}

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		if err := WriteBytes(cfgPath, out); err != nil {
			t.Fatalf("WriteBytes: %v", err)
		}

		cfg, err := LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		if cfg.Defaults.GitConfig == nil || *cfg.Defaults.GitConfig {
			t.Errorf("expected Defaults.GitConfig = false, got %v", cfg.Defaults.GitConfig)
		}
		rules := cfg.Defaults.Network.Get().Rules
		if len(rules) != 1 || rules[0].Name != "internet" || len(rules[0].Allow) != 1 || rules[0].Allow[0] != "*" {
			t.Fatalf("unexpected internet rules: %+v", rules)
		}
		ws, ok := cfg.Workspaces["work"]
		if !ok || len(ws.Paths) != 1 || ws.Paths[0] != "/data/work" {
			t.Errorf("expected workspace 'work' with [/data/work], got %+v", ws)
		}
	})
}
func TestRenderFirstTimeCommentsUnsetOptions(t *testing.T) {
	t.Parallel()

	out, err := RenderFirstTime(FirstTimeOptions{GitConfig: true, WorkspaceName: "default"})
	if err != nil {
		t.Fatalf("RenderFirstTime failed: %v", err)
	}
	defaults := defaultsSection(string(out))
	for _, want := range []string{
		`# agent = "opencode"  # opencode (OpenCode 2), opencode1, opencode2, claude, copilot, omo, agy or an agent defined under agents`,
		"# ssh_auth_sock = false  # forward host $SSH_AUTH_SOCK into the VM",
		"# gpg_agent = false  # forward host GPG agent into the VM",
		"# git_config = true  # copy ~/.gitconfig and ~/.ssh/known_hosts read-only",
		"# enable_docker = false  # run an isolated Docker daemon inside the VM",
		"# cpu = 2.0  # virtual CPU cores per workspace VM",
		`# memory = "4g"  # RAM limit per workspace VM`,
	} {
		if !strings.Contains(defaults, want+"\n") {
			t.Errorf("defaults missing commented line %q\n", want)
		}
	}
	if strings.Contains(defaults, "\nagent = ") || strings.Contains(defaults, "\ngit_config = ") {
		t.Errorf("unset defaults must not be active:\n%s", defaults)
	}
}

func TestRenderFirstTimeUncommentsSetOptions(t *testing.T) {
	t.Parallel()

	out, err := RenderFirstTime(FirstTimeOptions{
		Agent:         "omo",
		SSHAuthSock:   true,
		GPGAgent:      true,
		EnableDocker:  true,
		CPU:           4,
		Memory:        "8g",
		WorkspaceName: "default",
	})
	if err != nil {
		t.Fatalf("RenderFirstTime failed: %v", err)
	}
	defaults := defaultsSection(string(out))
	for _, want := range []string{
		`agent = "omo"  # opencode (OpenCode 2), opencode1, opencode2, claude, copilot, omo, agy or an agent defined under agents`,
		"ssh_auth_sock = true  # forward host $SSH_AUTH_SOCK into the VM",
		"gpg_agent = true  # forward host GPG agent into the VM",
		"git_config = false  # copy ~/.gitconfig and ~/.ssh/known_hosts read-only",
		"enable_docker = true  # run an isolated Docker daemon inside the VM",
		"cpu = 4.0  # virtual CPU cores per workspace VM",
		`memory = "8g"  # RAM limit per workspace VM`,
	} {
		if !strings.Contains(defaults, want+"\n") {
			t.Errorf("defaults missing active line %q\n", want)
		}
	}
	for _, key := range []string{"agent", "ssh_auth_sock", "gpg_agent", "git_config", "enable_docker", "cpu", "memory"} {
		if strings.Contains(defaults, "# "+key+" = ") {
			t.Errorf("set option %q still has a commented duplicate in defaults", key)
		}
	}
}

func TestRenderFirstTimeNetworkRules(t *testing.T) {
	t.Parallel()

	t.Run("internet preset replaces dev-deps and keeps examples", func(t *testing.T) {
		t.Parallel()
		out, err := RenderFirstTime(FirstTimeOptions{AllowInternet: true, ExtraHosts: []string{"ignored.example"}, WorkspaceName: "default"})
		if err != nil {
			t.Fatalf("RenderFirstTime failed: %v", err)
		}
		text := string(out)
		if strings.Contains(text, "dev-deps") {
			t.Error("internet preset kept the dev-deps rule")
		}
		if !strings.Contains(text, "# [[defaults.network.rules]]\n# name = \"no-paste\"") {
			t.Error("commented rule examples were not kept")
		}
		cfg := loadRenderedConfig(t, out)
		rules := cfg.Defaults.Network.Get().Rules
		if len(rules) != 1 || rules[0].Name != "internet" || rules[0].Allow[0] != "*" {
			t.Fatalf("unexpected rules: %+v", rules)
		}
	})

	t.Run("extra hosts keep dev-deps and append custom rule", func(t *testing.T) {
		t.Parallel()
		out, err := RenderFirstTime(FirstTimeOptions{ExtraHosts: []string{"gitlab.example.com"}, WorkspaceName: "default"})
		if err != nil {
			t.Fatalf("RenderFirstTime failed: %v", err)
		}
		cfg := loadRenderedConfig(t, out)
		rules := cfg.Defaults.Network.Get().Rules
		if len(rules) != 2 || rules[0].Name != "dev-deps" || rules[1].Name != "custom" || rules[1].Allow[0] != "gitlab.example.com" {
			t.Fatalf("unexpected rules: %+v", rules)
		}
	})
}

func TestRenderFirstTimeNamedWorkspace(t *testing.T) {
	t.Parallel()

	out, err := RenderFirstTime(FirstTimeOptions{WorkspaceName: "work", WorkspacePath: "/data/work"})
	if err != nil {
		t.Fatalf("RenderFirstTime failed: %v", err)
	}
	text := string(out)
	if strings.Contains(text, "[workspaces.default") {
		t.Error("named initial workspace kept a default workspace section")
	}
	for _, want := range []string{"[workspaces.work]\npaths = [\"/data/work\"]\n", "# [workspaces.work.network]\n", "# [workspaces.work.secrets.env.MY_TOKEN]\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q\n", want)
		}
	}
	cfg := loadRenderedConfig(t, out)
	if ws := cfg.Workspaces["work"]; len(ws.Paths) != 1 || ws.Paths[0] != "/data/work" {
		t.Errorf("workspace work paths = %v", ws.Paths)
	}
}

func TestRenderFirstTimeEncodesControlCharacters(t *testing.T) {
	t.Parallel()

	wantPath := "/data/control-\a-path"
	out, err := RenderFirstTime(FirstTimeOptions{WorkspaceName: "work", WorkspacePath: wantPath})
	if err != nil {
		t.Fatalf("RenderFirstTime failed: %v", err)
	}
	if _, err := edit.Parse(out); err != nil {
		t.Fatalf("edit.Parse: %v\n%s", err, out)
	}
	cfg := loadRenderedConfig(t, out)
	if got := cfg.Workspaces["work"].Paths; len(got) != 1 || got[0] != wantPath {
		t.Errorf("workspace paths = %q, want [%q]", got, wantPath)
	}
}

func TestRenderWorkspaceNewSection(t *testing.T) {
	t.Parallel()

	t.Run("commented options when nothing is set", func(t *testing.T) {
		t.Parallel()
		out, err := RenderWorkspace([]byte("[defaults]\nagent = \"opencode\"\n"), WorkspaceOptions{Name: "api", Paths: []string{"/data/api"}})
		if err != nil {
			t.Fatalf("RenderWorkspace failed: %v", err)
		}
		text := string(out)
		for _, want := range []string{
			"[workspaces.api]\npaths = [\"/data/api\"]\n",
			"# agent = \"opencode\"  # opencode (OpenCode 2)",
			"# enable_docker = false  # run an isolated Docker daemon inside the VM",
			"# ca_bundle = true  # omitted: inherit defaults; true/false/string: override",
			"# timezone = \"UTC\"  # time zone in the VM; omitted: the host's zone",
			"# [[workspaces.api.network.rules]]\n# name = \"internet\"",
			"# [workspaces.api.secrets.file.MY_FILE]",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("output missing %q\n", want)
			}
		}
		cfg := loadRenderedConfig(t, out)
		if ws := cfg.Workspaces["api"]; ws.Agent != "" || ws.EnableDocker != nil || len(ws.Paths) != 1 {
			t.Errorf("new workspace has active overrides: %+v", ws)
		}
	})

	t.Run("active agent, docker and internet rule", func(t *testing.T) {
		t.Parallel()
		enableDocker := true
		out, err := RenderWorkspace([]byte("[defaults]\nagent = \"opencode\"\n"), WorkspaceOptions{
			Name:          "api",
			Paths:         []string{"/data/api"},
			Agent:         "claude",
			EnableDocker:  &enableDocker,
			AllowInternet: true,
		})
		if err != nil {
			t.Fatalf("RenderWorkspace failed: %v", err)
		}
		text := string(out)
		for _, want := range []string{
			"agent = \"claude\"  # opencode (OpenCode 2)",
			"enable_docker = true  # run an isolated Docker daemon inside the VM",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("output missing %q\n", want)
			}
		}
		if strings.Contains(text, "# [[workspaces.api.network.rules]]") {
			t.Error("internet preset kept the commented internet example")
		}
		cfg := loadRenderedConfig(t, out)
		ws := cfg.Workspaces["api"]
		if ws.Agent != "claude" || ws.EnableDocker == nil || !*ws.EnableDocker {
			t.Errorf("workspace overrides = agent %q enable_docker %v", ws.Agent, ws.EnableDocker)
		}
		rules := ws.Network.Get().Rules
		if len(rules) != 1 || rules[0].Name != "internet" {
			t.Errorf("workspace rules = %+v", rules)
		}
	})
}

func TestRenderWorkspaceEncodesControlCharacters(t *testing.T) {
	t.Parallel()

	wantPath := "/data/control-\x1b-\a-\v-path"
	out, err := RenderWorkspace(nil, WorkspaceOptions{Name: "work", Paths: []string{wantPath}})
	if err != nil {
		t.Fatalf("RenderWorkspace failed: %v", err)
	}
	doc, err := edit.Parse(out)
	if err != nil {
		t.Fatalf("edit.Parse: %v\n%s", err, out)
	}
	value, ok := doc.Get([]string{"workspaces", "work", "paths"})
	if !ok {
		t.Fatal("edit.Parse output has no workspaces.work.paths")
	}
	paths, ok := value.([]any)
	if !ok || len(paths) != 1 || paths[0] != wantPath {
		t.Errorf("edit.Parse paths = %#v, want [%q]", value, wantPath)
	}
	cfg := loadRenderedConfig(t, out)
	if got := cfg.Workspaces["work"].Paths; len(got) != 1 || got[0] != wantPath {
		t.Errorf("workspace paths = %q, want [%q]", got, wantPath)
	}
}

func TestRenderWorkspaceRootWorkspacesTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		raw              string
		wantExistingPath string
		wantComment      string
		wantRootTable    bool
	}{
		{
			name:             "nonempty inline table",
			raw:              "# top comment\nworkspaces = { existing = { paths = [\"/data/existing\"], agent = \"claude\" } } # keep inline comment\n",
			wantExistingPath: "/data/existing",
			wantComment:      "# keep inline comment",
		},
		{
			name:        "empty inline table",
			raw:         "# top comment\nworkspaces = {} # keep empty inline comment\n",
			wantComment: "# keep empty inline comment",
		},
		{
			name:          "normal table",
			raw:           "# top comment\n[workspaces] # keep root table\n",
			wantComment:   "# keep root table",
			wantRootTable: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := RenderWorkspace([]byte(tc.raw), WorkspaceOptions{Name: "api", Paths: []string{"/data/api"}})
			if err != nil {
				t.Fatalf("RenderWorkspace failed: %v", err)
			}
			if _, err := edit.Parse(out); err != nil {
				t.Fatalf("edit.Parse: %v\n%s", err, out)
			}
			text := string(out)
			if !strings.Contains(text, "# top comment") || !strings.Contains(text, tc.wantComment) {
				t.Errorf("comments were not retained:\n%s", text)
			}
			if !strings.Contains(text, "[workspaces.api]\npaths = [\"/data/api\"]\n") || !strings.Contains(text, "# agent = \"opencode\"") {
				t.Errorf("new workspace section is not documented:\n%s", text)
			}
			if tc.wantRootTable && !strings.Contains(text, "[workspaces] # keep root table") {
				t.Errorf("normal workspaces table was rewritten:\n%s", text)
			}

			cfg := loadRenderedConfig(t, out)
			if got := cfg.Workspaces["api"].Paths; len(got) != 1 || got[0] != "/data/api" {
				t.Errorf("api paths = %v, want [/data/api]", got)
			}
			if tc.wantExistingPath != "" {
				existing := cfg.Workspaces["existing"]
				if got := existing.Paths; len(got) != 1 || got[0] != tc.wantExistingPath {
					t.Errorf("existing paths = %v, want [%s]", got, tc.wantExistingPath)
				}
				if existing.Agent != "claude" {
					t.Errorf("existing agent = %q, want claude", existing.Agent)
				}
			}
		})
	}
}

// defaultsSection returns the text before the first workspace section.
func defaultsSection(text string) string {
	if i := strings.Index(text, "\n[workspaces."); i >= 0 {
		return text[:i+1]
	}
	return text
}

// loadRenderedConfig writes rendered output to a temporary file and loads it.
func loadRenderedConfig(t *testing.T, data []byte) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteBytes(path, data); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v\n%s", err, data)
	}
	return cfg
}

func TestRenderWorkspace(t *testing.T) {
	t.Parallel()

	raw := []byte(`# custom top comment
[defaults]
agent = "opencode"

[workspaces.default]
paths = ["/data/default"]
`)

	enableDocker := true
	opts := WorkspaceOptions{
		Name:         "szn",
		Paths:        []string{"/data/szn"},
		Agent:        "omo",
		EnableDocker: &enableDocker,
		ExtraHosts:   []string{"gitlab.seznam.net"},
	}

	out, err := RenderWorkspace(raw, opts)
	if err != nil {
		t.Fatalf("RenderWorkspace failed: %v", err)
	}
	if !strings.Contains(string(out), "# custom top comment") {
		t.Errorf("expected comment to be preserved, got:\n%s", string(out))
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := WriteBytes(cfgPath, out); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}

	cfg, err := LoadFrom(cfgPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	ws, ok := cfg.Workspaces["szn"]
	if !ok {
		t.Fatal("expected workspace 'szn' to exist")
	}
	if ws.Agent != "omo" {
		t.Errorf("ws.Agent = %q, want omo", ws.Agent)
	}
	if ws.EnableDocker == nil || !*ws.EnableDocker {
		t.Errorf("expected ws.EnableDocker = true, got %v", ws.EnableDocker)
	}
	if len(ws.Paths) != 1 || ws.Paths[0] != "/data/szn" {
		t.Errorf("ws.Paths = %v, want [/data/szn]", ws.Paths)
	}
	rules := ws.Network.Get().Rules
	if len(rules) != 1 || len(rules[0].Allow) != 1 || rules[0].Allow[0] != "gitlab.seznam.net" {
		t.Errorf("unexpected workspace network rules: %+v", rules)
	}
}

func TestRenderWorkspaceInheritClearsOverrides(t *testing.T) {
	t.Parallel()

	raw := []byte(`[workspaces.szn]
paths = ["/data/old"]
agent = "claude"
enable_docker = true

[workspaces.szn.network]
dns = "9.9.9.9"

[[workspaces.szn.network.rules]]
name = "internet"
allow = ["*"]
`)

	out, err := RenderWorkspace(raw, WorkspaceOptions{Name: "szn", Paths: []string{"/data/szn"}})
	if err != nil {
		t.Fatalf("RenderWorkspace failed: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteBytes(cfgPath, out); err != nil {
		t.Fatalf("WriteBytes: %v", err)
	}
	cfg, err := LoadFrom(cfgPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v\n%s", err, out)
	}
	ws := cfg.Workspaces["szn"]
	if ws.Agent != "" || ws.EnableDocker != nil {
		t.Errorf("inherited agent/enable_docker kept overrides: agent=%q enable_docker=%v", ws.Agent, ws.EnableDocker)
	}
	if rules := ws.Network.Get().Rules; len(rules) != 0 {
		t.Errorf("inherited network kept rules: %+v", rules)
	}
	if got := ws.Network.Get().DNS; got != "9.9.9.9" {
		t.Errorf("network.dns = %q, want unrelated setting kept", got)
	}
	if len(ws.Paths) != 1 || ws.Paths[0] != "/data/szn" {
		t.Errorf("paths = %v, want [/data/szn]", ws.Paths)
	}
}
