package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			SecretsEnv: map[string]Secret{
				"GH_TOKEN": {FromCommand: "gh auth token"},
			},
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
		if got := cfg.Defaults.Secrets.Env["GH_TOKEN"].FromCommand; got != "gh auth token" {
			t.Errorf("GH_TOKEN FromCommand = %q, want 'gh auth token'", got)
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
