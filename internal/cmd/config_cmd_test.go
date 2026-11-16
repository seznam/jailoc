package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	assertContains(t, out, "  CPU: 4\n")
	assertContains(t, out, "  Memory: 8g\n")
	assertContains(t, out, "  Time Zone: Europe/Prague\n")
	assertContains(t, out, "  SSH Agent: true\n")
	assertContains(t, out, "  Protect Git: true\n")
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
