package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/spf13/cobra"
)

func TestConfigResolvedWorkspaceAndComputedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	projectDir := filepath.Dir(origWD)
	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	content := `[defaults]
agent = "claude"
env = ["DEFAULT_VAR=1", "SHARED=from_defaults"]
cpu = 4.0
memory = "8g"
timezone = "Europe/Prague"
notifications = "off"

[defaults.agents.claude]
profile = "work"

[[defaults.network.rules]]
name = "dev"
allow = ["github.com"]

[workspaces.default]
paths = []

[workspaces.myproject]
paths = [` + `"` + projectDir + `"` + `]
env = ["SHARED=from_workspace", "WS_VAR=2"]
ports = ["8080:80"]
host_ports = [5432]
ssh_auth_sock = true
protect_git = true
notifications = "osc"

[workspaces.myproject.web]
zeta = 9000
app = 3000
`
	configFile := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Cleanup(func() {
		allFlag, workspaceFlag, workspaceExplicit = false, "default", false
	})
	allFlag = false
	workspaceFlag, workspaceExplicit = "default", false
	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	if strings.Contains(out, "Workspace: default") {
		t.Fatalf("expected only auto-selected workspace myproject, got:\n%s", out)
	}
	if strings.Contains(out, "Defaults ") || strings.Contains(out, "Mode:") {
		t.Fatalf("expected no defaults/global header in output, got:\n%s", out)
	}

	assertContains(t, out, "Workspace: myproject\n")
	assertContains(t, out, "  Agent: claude (")
	assertContains(t, out, "  Profile: work\n")
	assertContains(t, out, "  Paths:\n    - "+projectDir+"\n")
	assertContains(t, out, "  Env:\n    - DEFAULT_VAR=1\n    - SHARED=from_workspace\n    - WS_VAR=2\n")
	assertContains(t, out, "  Ports:\n    - 127.0.0.1:8080 -> 80\n")
	assertContains(t, out, "  Host Ports:\n    - 5432 -> 127.0.0.1:5432\n")
	assertContains(t, out, "  Web:\n    - https://app.myproject.jailoc.local -> 3000\n    - https://zeta.myproject.jailoc.local -> 9000\n")
	assertContains(t, out, "  CPU: 4\n")
	assertContains(t, out, "  Memory: 8g\n")
	assertContains(t, out, "  Time Zone: Europe/Prague\n")
	assertContains(t, out, "  SSH Agent: true\n")
	assertContains(t, out, "  Protect Git: true\n")
	assertContains(t, out, "  Notifications: osc\n")
	assertContains(t, out, "  Computed Paths:\n")
	assertContains(t, out, "    Config File: "+configFile+"\n")
	assertContains(t, out, "    Workspace Config Dir: "+filepath.Join(configDir, "workspaces", "myproject")+"\n")
	assertContains(t, out, "    Workspace Cache Dir: "+filepath.Join(home, ".cache", "jailoc", "myproject")+"\n")
	assertContains(t, out, "    VM State Dir: "+filepath.Join(home, ".cache", "jailoc-vm", "workspaces", "myproject")+"\n")
	assertContains(t, out, "    Profile Dir: "+filepath.Join(home, ".local", "share", "jailoc", "profiles", "work")+"\n")
}

func TestConfigComputedProfileDirModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	content := `[workspaces.ws-default]
paths = []

[workspaces.ws-ephemeral]
paths = []
agent = "claude"
[workspaces.ws-ephemeral.agents.claude]
profile = false

[workspaces.ws-claude-mounted]
paths = []
agent = "claude"
mounts = ["/srv/claude:/home/agent/.claude"]
[workspaces.ws-claude-mounted.agents.claude]
profile = false

[workspaces.ws-opencode-partial]
paths = []
agent = "opencode1"
mounts = ["~/custom-data:/home/agent/.local/share/opencode"]
[workspaces.ws-opencode-partial.agents.opencode1]
profile = "work"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Cleanup(func() { allFlag, workspaceFlag, workspaceExplicit = false, "default", false })
	allFlag = false
	workspaceFlag, workspaceExplicit = "default", false

	outDefault := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, []string{"ws-default"}); err != nil {
			t.Fatalf("runConfig(ws-default) = %v", err)
		}
	})
	assertContains(t, outDefault, "  Profile: workspace\n")
	assertContains(t, outDefault, "    Profile Dir: "+filepath.Join(home, ".cache", "jailoc-vm", "workspaces", "ws-default", "guest", "home")+"\n")

	outEphemeral := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, []string{"ws-ephemeral"}); err != nil {
			t.Fatalf("runConfig(ws-ephemeral) = %v", err)
		}
	})
	assertContains(t, outEphemeral, "  Profile: none (ephemeral)\n")
	assertContains(t, outEphemeral, "    Profile Dir: "+filepath.Join(home, ".cache", "jailoc-vm", "workspaces", "ws-ephemeral", "guest", "ephemeral")+" (ephemeral)\n")

	outMounted := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, []string{"ws-claude-mounted"}); err != nil {
			t.Fatalf("runConfig(ws-claude-mounted) = %v", err)
		}
	})
	assertContains(t, outMounted, "    Profile Dir: /srv/claude\n")
	if strings.Contains(outMounted, "Profile Dir: /srv/claude (ephemeral)") {
		t.Fatalf("mounted state must not be labeled ephemeral:\n%s", outMounted)
	}

	outPartial := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, []string{"ws-opencode-partial"}); err != nil {
			t.Fatalf("runConfig(ws-opencode-partial) = %v", err)
		}
	})
	profileDir := filepath.Join(home, ".local", "share", "jailoc", "profiles", "work")
	wantPartial := filepath.Join(home, "custom-data") + ", " + filepath.Join(profileDir, "opencode-state") + ", " + filepath.Join(profileDir, "opencode1-omo")
	assertContains(t, outPartial, "    Profile Dir: "+wantPartial+"\n")
	if _, err := os.Stat(filepath.Join(home, "custom-data")); !os.IsNotExist(err) {
		t.Fatalf("config inspection must not create mount directories on disk: err=%v", err)
	}
}

func TestPrintNetworkRulesShowsLogFalse(t *testing.T) {
	// captureStdout swaps os.Stdout: do not run in parallel.
	off, on := false, true
	out := captureStdout(t, func() {
		printNetworkRules([]config.NetworkRule{
			{Name: "quiet", Allow: []string{"registry.example.com"}, Log: &off},
			{Name: "quiet-http", Deny: []string{"example.com"}, HTTP: &config.HTTPMatch{Methods: []string{"POST"}}, Log: &off},
			{Name: "loud", Allow: []string{"a.example.com"}, Log: &on},
			{Name: "default", Allow: []string{"b.example.com"}},
		}, "")
	})
	want := "- quiet: allow registry.example.com (tcp log=false)\n" +
		"- quiet-http: deny example.com (tcp http methods=POST log=false)\n" +
		"- loud: allow a.example.com (tcp)\n" +
		"- default: allow b.example.com (tcp)\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestPrintConfigWarningsMigrateFooter(t *testing.T) {
	// captureStderr swaps os.Stderr: do not run in parallel.
	const footer = "Run `jailoc migrate` to update deprecated settings in config.toml.\n"
	const rules = "[[defaults.network.rules]]\nallow = [\"*\"]\n"
	tests := []struct {
		name       string
		toml       string
		wantFooter bool
		wantParts  []string
	}{
		{
			name:       "single legacy notice with rules",
			toml:       "[defaults]\nallowed_hosts = [\"example.com\"]\n" + rules,
			wantFooter: true,
			wantParts:  []string{"allowed_hosts is deprecated"},
		},
		{
			name:       "multiple notices",
			toml:       "[defaults]\nallowed_hosts = [\"example.com\"]\nallowed_networks = [\"10.0.0.0/8\"]\n" + rules,
			wantFooter: true,
			wantParts:  []string{"allowed_hosts is deprecated", "allowed_networks is deprecated"},
		},
		{
			name:       "no notices",
			toml:       rules + "\n[workspaces.demo]\npaths = [\"/srv/demo\"]\n",
			wantFooter: false,
		},
		{
			name:       "unknown key only",
			toml:       "[defaults]\nbogus_setting = true\n" + rules,
			wantFooter: false,
			wantParts:  []string{"unknown setting \"defaults.bogus_setting\""},
		},
		{
			name:       "no rules only",
			toml:       "[workspaces.demo]\npaths = [\"/srv/demo\"]\n",
			wantFooter: true,
			wantParts:  []string{"no [[*.network.rules]] configured", "allow = [\"*\"]", "[[defaults.network.rules]]"},
		},
		{
			name:       "legacy and no rules",
			toml:       "[defaults]\nallowed_hosts = [\"example.com\"]\nbogus_setting = true\n\n[workspaces.demo]\npaths = [\"/srv/demo\"]\n",
			wantFooter: true,
			wantParts:  []string{"unknown setting", "allowed_hosts is deprecated", "no [[*.network.rules]] configured"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tt.toml), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := config.LoadFrom(path)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}

			out := captureStderr(t, func() { printConfigWarnings(cfg) })

			last := -1
			for _, part := range tt.wantParts {
				idx := strings.Index(out, part)
				if idx < 0 {
					t.Fatalf("stderr missing %q:\n%s", part, out)
				}
				if idx < last {
					t.Fatalf("stderr part %q out of order:\n%s", part, out)
				}
				last = idx
			}
			if got := strings.Count(out, "jailoc migrate"); tt.wantFooter && got != 1 || !tt.wantFooter && got != 0 {
				t.Fatalf("jailoc migrate mentions = %d, want footer=%v:\n%s", got, tt.wantFooter, out)
			}
			if tt.wantFooter && !strings.HasSuffix(out, footer) {
				t.Fatalf("stderr does not end with footer:\n%s", out)
			}
		})
	}
}

func TestConfigShowsTmpSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	content := `[defaults]
tmp_storage = "disk"
tmp_size = "2g"
tmp_ttl = "15m"

[workspaces.inherits]
paths = []

[workspaces.overrides]
paths = []
tmp_storage = "ram"
tmp_size = "512m"
tmp_ttl = "0"

[workspaces.bare]
paths = []
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Cleanup(func() { allFlag, workspaceFlag, workspaceExplicit = false, "default", false })
	allFlag = false
	workspaceFlag, workspaceExplicit = "default", false

	tests := []struct {
		workspace string
		want      string
	}{
		{"inherits", "  Tmp: disk, 2 GiB, cleanup after 15m\n"},
		{"overrides", "  Tmp: ram, 512 MiB, no cleanup\n"},
	}
	for _, tt := range tests {
		out := captureStdout(t, func() {
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			if err := runConfig(cmd, []string{tt.workspace}); err != nil {
				t.Fatalf("runConfig(%s) = %v", tt.workspace, err)
			}
		})
		assertContains(t, out, tt.want)
	}

	// Without tmp settings anywhere the host defaults apply.
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[workspaces.bare]\npaths = []\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, []string{"bare"}); err != nil {
			t.Fatalf("runConfig(bare) = %v", err)
		}
	})
	assertContains(t, out, "  Tmp: ram, 1 GiB, cleanup after 8h\n")
}

func TestConfigRejectsInvalidTmpSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[workspaces.small]\npaths = []\ntmp_storage = \"disk\"\ntmp_size = \"1m\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Cleanup(func() { allFlag, workspaceFlag, workspaceExplicit = false, "default", false })
	allFlag = false
	workspaceFlag, workspaceExplicit = "default", false

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runConfig(cmd, []string{"small"})
	if err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("runConfig error = %v, want the 16 MiB minimum", err)
	}
}
