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

}
