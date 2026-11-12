package cmd

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
)

func TestRunSetupFirstTimeAndWorkspace(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	t.Run("first-time setup with --yes writes valid customized config", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		projDir := cwd

		var out bytes.Buffer
		w := &wizardPrompter{
			in:  bufio.NewReader(strings.NewReader("")),
			out: &out,
			yes: true,
		}

		wsName, startNow, err := runSetup(context.Background(), cfgPath, projDir, w, false)
		if err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		if wsName != "default" {
			t.Errorf("wsName = %q, want default", wsName)
		}
		if startNow {
			t.Error("expected startNow = false when --yes is set")
		}

		isDef, err := config.IsDefaultFile(cfgPath)
		if err != nil {
			t.Fatalf("IsDefaultFile: %v", err)
		}
		if isDef {
			t.Error("expected config file to no longer be considered untouched default")
		}

		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		ws := cfg.Workspaces["default"]
		if len(ws.Paths) != 1 || ws.Paths[0] != projDir {
			t.Errorf("default workspace paths = %v, want [%s]", ws.Paths, projDir)
		}
	})

	t.Run("second run on customized config enters workspace setup mode", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(cfgPath, []byte("[defaults]\nagent = \"omo\"\n\n[workspaces.default]\npaths = [\"/data/default\"]\n"), 0o600); err != nil {
			t.Fatalf("write initial config: %v", err)
		}

		projDir := cwd

		input := strings.Join([]string{
			"agrologo",
			projDir,
			"claude",
			"yes",
			"custom",
			"gitlab.seznam.net, 10.10.0.0/16",
			"yes",
			"no",
		}, "\n") + "\n"

		var out bytes.Buffer
		w := &wizardPrompter{
			in:  bufio.NewReader(strings.NewReader(input)),
			out: &out,
			yes: false,
		}

		wsName, startNow, err := runSetup(context.Background(), cfgPath, projDir, w, false)
		if err != nil {
			t.Fatalf("runSetup workspace mode failed: %v", err)
		}
		if wsName != "agrologo" {
			t.Errorf("wsName = %q, want agrologo", wsName)
		}
		if startNow {
			t.Error("expected startNow = false")
		}

		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		ws, ok := cfg.Workspaces["agrologo"]
		if !ok {
			t.Fatal("expected workspace 'agrologo' to be created")
		}
		if ws.Agent != "claude" {
			t.Errorf("ws.Agent = %q, want claude", ws.Agent)
		}
		if ws.EnableDocker == nil || !*ws.EnableDocker {
			t.Errorf("expected ws.EnableDocker = true, got %v", ws.EnableDocker)
		}
		if len(ws.Paths) != 1 || ws.Paths[0] != projDir {
			t.Errorf("ws.Paths = %v, want [%s]", ws.Paths, projDir)
		}
		rules := ws.Network.Get().Rules
		if len(rules) != 1 || len(rules[0].Allow) != 2 {
			t.Errorf("unexpected network rules: %+v", rules)
		}
	})

	t.Run("dry-run prints config without writing file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		projDir := cwd

		var out bytes.Buffer
		w := &wizardPrompter{
			in:  bufio.NewReader(strings.NewReader("")),
			out: &out,
			yes: true,
		}

		_, _, err := runSetup(context.Background(), cfgPath, projDir, w, true)
		if err != nil {
			t.Fatalf("runSetup dry-run failed: %v", err)
		}
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected %s not to exist after dry-run", cfgPath)
		}
		assertContains(t, out.String(), "dry run, nothing written")
	})

	t.Run("gh and glab prompts run only when binary is accessible on host", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		projDir := cwd

		cfgNone := filepath.Join(dir, "none.toml")
		var outNone bytes.Buffer
		wNone := &wizardPrompter{
			in:       bufio.NewReader(strings.NewReader("")),
			out:      &outNone,
			yes:      true,
			lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		}
		if _, _, err := runSetup(context.Background(), cfgNone, projDir, wNone, false); err != nil {
			t.Fatalf("runSetup (none) failed: %v", err)
		}
		if strings.Contains(outNone.String(), "GH_TOKEN") || strings.Contains(outNone.String(), "GITLAB_TOKEN") {
			t.Errorf("expected no GH_TOKEN/GITLAB_TOKEN prompt when binaries are absent, got:\n%s", outNone.String())
		}

		cfgBoth := filepath.Join(dir, "both.toml")
		inputBoth := strings.Join([]string{
			"opencode",
			"yes",
			"no",
			"no",
			"dev-deps",
			"",
			"no",
			"2",
			"4g",
			"yes",
			"yes",
			"default",
			projDir,
			"yes",
			"no",
		}, "\n") + "\n"
		var outBoth bytes.Buffer
		wBoth := &wizardPrompter{
			in:       bufio.NewReader(strings.NewReader(inputBoth)),
			out:      &outBoth,
			yes:      false,
			lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		}
		if _, _, err := runSetup(context.Background(), cfgBoth, projDir, wBoth, false); err != nil {
			t.Fatalf("runSetup (both) failed: %v", err)
		}
		assertContains(t, outBoth.String(), "GH_TOKEN")
		assertContains(t, outBoth.String(), "GITLAB_TOKEN")

		loaded, err := config.LoadFrom(cfgBoth)
		if err != nil {
			t.Fatalf("LoadFrom(both): %v", err)
		}
		if loaded.Defaults.Secrets.Env["GH_TOKEN"].FromCommand == "" {
			t.Error("expected GH_TOKEN secret to be configured")
		}
		if loaded.Defaults.Secrets.Env["GITLAB_TOKEN"].FromCommand == "" {
			t.Error("expected GITLAB_TOKEN secret to be configured")
		}
	})
}
