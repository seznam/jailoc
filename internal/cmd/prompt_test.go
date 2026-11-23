package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/seznam/jailoc/internal/config"
)

// firstTimeAccessibleAnswers answers every visible first-time question with
// Claude as the agent; Claude has fixed endpoints, so no provider question.
const firstTimeAccessibleAnswers = "3\n" + // agent: claude
	"\n\n\n" + // git_config, ssh_auth_sock, gpg_agent
	"1\n" + // network: dev-deps
	"\n" + // extra hosts
	"\n\n\n" + // docker, CPUs, memory
	"\n\n" // workspace name, directory

func TestAccessibleFirstTimeSetup(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	t.Run("hidden steps are skipped and answers are saved", func(t *testing.T) {
		t.Parallel()
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		p := &promptIO{in: strings.NewReader(firstTimeAccessibleAnswers + "y\n"), out: &out, accessible: true}

		if _, err := runSetup(context.Background(), p, setupOptions{configPath: cfgPath, targetDir: cwd}); err != nil {
			t.Fatalf("runSetup: %v\noutput:\n%s", err, out.String())
		}
		if strings.Contains(out.String(), "Model providers") {
			t.Fatalf("provider question asked for Claude:\n%s", out.String())
		}
		assertContains(t, out.String(), "Review configuration")
		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Defaults.Agent != "claude" {
			t.Fatalf("agent = %q, want claude", cfg.Defaults.Agent)
		}
	})

	t.Run("input ending before the final confirm writes nothing", func(t *testing.T) {
		t.Parallel()
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		p := &promptIO{in: strings.NewReader(firstTimeAccessibleAnswers), out: &out, accessible: true}

		_, err := runSetup(context.Background(), p, setupOptions{configPath: cfgPath, targetDir: cwd})
		if err == nil || !strings.Contains(err.Error(), "input ended") {
			t.Fatalf("runSetup error = %v, want input ended", err)
		}
		if _, err := os.Stat(cfgPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("config written after truncated input: %v", err)
		}
	})

	t.Run("cancel while waiting for an answer stops without writing", func(t *testing.T) {
		t.Parallel()
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		pr, pw := io.Pipe()
		t.Cleanup(func() { _ = pw.Close() })
		out := &signalWriter{needle: "Default coding agent", hit: make(chan struct{})}
		p := &promptIO{in: pr, out: out, accessible: true}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := runSetup(ctx, p, setupOptions{configPath: cfgPath, targetDir: cwd})
			done <- err
		}()

		select {
		case <-out.hit:
		case <-time.After(10 * time.Second):
			t.Fatal("agent question not printed")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("runSetup error = %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("runSetup did not return after cancel")
		}
		if _, err := os.Stat(cfgPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("config written after cancel: %v", err)
		}
	})
}

func TestSetupReviewFollowsEdits(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	opts := setupOptions{configPath: filepath.Join(t.TempDir(), "config.toml"), targetDir: cwd}
	ans := defaultFirstTimeAnswers(opts)
	ans.agent = "claude"
	form := buildFirstTimeForm(&ans, opts).form()
	form.WithTheme(compactTheme())
	flushCmd(form, form.Init())
	_, cmd := form.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	flushCmd(form, cmd)

	for i := 0; i < 30 && !strings.Contains(form.View(), "Review configuration"); i++ {
		_, cmd := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
		flushCmd(form, cmd)
	}
	assertContains(t, form.View(), "2 CPUs")

	// An answer changed on an earlier step must show up in the summary.
	ans.cpu = "8"
	_, cmd = form.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	flushCmd(form, cmd)
	assertContains(t, form.View(), "8 CPUs")
}
