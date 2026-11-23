package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
)

type signalWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	needle string
	hit    chan struct{}
	once   sync.Once
}

func (w *signalWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(b)
	if strings.Contains(w.buf.String(), w.needle) {
		w.once.Do(func() { close(w.hit) })
	}
	return n, err
}

func writeCustomConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[defaults]\nagent = \"omo\"\n\n[workspaces.default]\npaths = [\"/data/default\"]\n"), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	return cfgPath
}

// driveFormViews steps through every non-hidden group of form by sending Enter
// on each field and collects all rendered views.
func driveFormViews(t *testing.T, form *huh.Form) string {
	t.Helper()
	form.WithTheme(compactTheme())
	if cmd := form.Init(); cmd != nil {
		flushCmd(form, cmd)
	}
	_, cmd := form.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	flushCmd(form, cmd)

	var views strings.Builder
	views.WriteString(form.View())
	views.WriteString("\n")

	for steps := 0; steps < 50 && form.State == huh.StateNormal; steps++ {
		_, cmd := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
		flushCmd(form, cmd)
		views.WriteString(form.View())
		views.WriteString("\n")
	}
	if form.State != huh.StateCompleted {
		t.Fatalf("form did not complete after stepping through fields (state=%v, errors=%v)", form.State, form.Errors())
	}
	return views.String()
}

func flushCmd(form *huh.Form, cmd tea.Cmd) {
	for depth := 0; cmd != nil && depth < 20; depth++ {
		ch := make(chan tea.Msg, 1)
		go func(c tea.Cmd) {
			ch <- c()
		}(cmd)
		var msg tea.Msg
		select {
		case msg = <-ch:
		case <-time.After(5 * time.Millisecond):
			return
		}
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				flushCmd(form, sub)
			}
			return
		}
		_, next := form.Update(msg)
		cmd = next
	}
}

func TestRunSetupFirstTimeAndWorkspace(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	t.Run("first-time setup with --yes writes valid customized config", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}

		res, err := runSetup(context.Background(), p, setupOptions{configPath: cfgPath, targetDir: cwd, askStart: true})
		if err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		if res.name != "default" || res.dir != cwd {
			t.Errorf("result = %+v, want name default and dir %s", res, cwd)
		}
		if res.startNow {
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
		if len(ws.Paths) != 1 || ws.Paths[0] != cwd {
			t.Errorf("default workspace paths = %v, want [%s]", ws.Paths, cwd)
		}
		for _, hint := range []string{
			"A read-only copy, so commits in the VM use your name and email.",
			"RAM limit per VM, not memory reserved on the host.",
			"Allocated as needed; freed pages and unused file cache return to the host.",
		} {
			if strings.Contains(out.String(), hint) {
				t.Errorf("--yes output must not contain explanation %q, got:\n%s", hint, out.String())
			}
		}
	})

	t.Run("first-time setup form renders explanations and review step", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		opts := setupOptions{configPath: cfgPath, targetDir: cwd}
		ans := defaultFirstTimeAnswers(opts)
		ans.agent = "opencode"

		form := buildFirstTimeForm(&ans, opts)
		output := driveFormViews(t, form)

		for _, want := range []string{
			"jailoc installs this agent in the VM and starts it in each session.",
			"The agent reaches only the model and auth endpoints of the providers you select",
			"A read-only copy, so commits in the VM use your name and email.",
			"Lets SSH and Git in the VM use your keys and ~/.ssh/known_hosts.",
			"processes in the VM can sign and decrypt with them.",
			"Private networks stay blocked unless you allow them explicitly.",
			"such as gitlab.corp.example or 10.20.0.0/16.",
			"the host Docker socket is not shared.",
			"Virtual CPUs per VM, rounded up to a whole number.",
			"RAM limit per VM, not memory reserved on the host.",
			"Allocated as needed; freed pages and unused file cache return to the host.",
			"Used in commands such as \"jailoc up <name>\".",
			"The agent can modify any file in it.",
			"Review configuration",
		} {
			assertContains(t, output, want)
		}

		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}
		if _, err := applyFirstTimeSetup(context.Background(), p, opts, ans); err != nil {
			t.Fatalf("applyFirstTimeSetup failed: %v", err)
		}
		if _, err := config.LoadFrom(cfgPath); err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
	})

	t.Run("first-time setup with custom answers and provider selection", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		opts := setupOptions{configPath: cfgPath, targetDir: cwd}
		ans := defaultFirstTimeAnswers(opts)
		ans.agent = "opencode"
		ans.providers = []string{"anthropic", "openai"}
		ans.extraHosts = "gitlab.corp.example, 10.20.0.0/16"
		ans.enableDocker = true
		ans.cpu = "4"
		ans.memory = "8g"

		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}
		if _, err := applyFirstTimeSetup(context.Background(), p, opts, ans); err != nil {
			t.Fatalf("applyFirstTimeSetup: %v", err)
		}

		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		wantEndpoints := []string{"api.anthropic.com", "api.openai.com", "auth.openai.com", "chatgpt.com", "models.dev"}
		if got := cfg.Defaults.Network.Get().AgentEndpoints; !slices.Equal(got, wantEndpoints) {
			t.Errorf("agent_endpoints = %v, want %v", got, wantEndpoints)
		}
		if cfg.Defaults.EnableDocker == nil || !*cfg.Defaults.EnableDocker || cfg.Defaults.CPU == nil || *cfg.Defaults.CPU != 4 || cfg.Defaults.Memory == nil || *cfg.Defaults.Memory != "8g" {
			t.Errorf("defaults = %+v", cfg.Defaults)
		}
	})

	t.Run("workspace setup with --yes prints no explanations", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeCustomConfig(t)
		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}

		if _, err := runSetup(context.Background(), p, setupOptions{configPath: cfgPath, targetDir: cwd}); err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		if strings.Contains(out.String(), "Used in commands such as") {
			t.Errorf("--yes output must not contain explanations, got:\n%s", out.String())
		}
	})

	t.Run("first-time setup seeds the workspace name from the caller", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}

		res, err := runSetup(context.Background(), p, setupOptions{configPath: cfgPath, targetDir: cwd, defaultName: "project"})
		if err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		if res.name != "project" {
			t.Errorf("res.name = %q, want project", res.name)
		}
		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		if ws, ok := cfg.Workspaces["project"]; !ok || len(ws.Paths) != 1 || ws.Paths[0] != cwd {
			t.Errorf("workspace project = %+v (exists %v), want paths [%s]", ws, ok, cwd)
		}
	})

	t.Run("second run on customized config enters workspace setup mode", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeCustomConfig(t)
		raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: test file
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		opts := setupOptions{configPath: cfgPath, targetDir: cwd, askStart: true}
		ans := defaultWorkspaceSetupAnswers(opts)
		ans.name = "agrologo"
		ans.dir = cwd
		ans.agent = "claude"
		ans.docker = "yes"
		ans.networkMode = "custom"
		ans.extraHosts = "gitlab.seznam.net, 10.10.0.0/16"
		ans.save = true

		form := buildWorkspaceSetupForm(&ans, opts, cfg, raw)
		output := driveFormViews(t, form)
		for _, want := range []string{
			"\"inherit\" uses the [defaults] agent.",
			"\"internet\" adds all public hosts to them, \"custom\" the hosts you list.",
			"such as *.corp.example or 10.20.0.0/16.",
			"Review workspace",
		} {
			assertContains(t, output, want)
		}

		var out bytes.Buffer
		// Feed "n\n" for the post-save "Start workspace now?" accessible confirm.
		p := &promptIO{in: strings.NewReader("n\n"), out: &out, accessible: true}
		res, err := applyWorkspaceSetup(context.Background(), p, opts, raw, ans)
		if err != nil {
			t.Fatalf("applyWorkspaceSetup failed: %v", err)
		}
		if res.name != "agrologo" || res.startNow {
			t.Errorf("result = %+v, want name agrologo and startNow false", res)
		}

		cfg, err = config.LoadFrom(cfgPath)
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
		if len(ws.Paths) != 1 || ws.Paths[0] != cwd {
			t.Errorf("ws.Paths = %v, want [%s]", ws.Paths, cwd)
		}
		rules := ws.Network.Get().Rules
		if len(rules) != 1 || len(rules[0].Allow) != 2 {
			t.Errorf("unexpected network rules: %+v", rules)
		}
	})

	t.Run("result carries the directory chosen in the wizard", func(t *testing.T) {
		t.Parallel()

		chosen := filepath.Dir(cwd)
		cfgPath := writeCustomConfig(t)
		raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: test file
		if err != nil {
			t.Fatal(err)
		}

		opts := setupOptions{configPath: cfgPath, targetDir: cwd, askStart: true}
		ans := defaultWorkspaceSetupAnswers(opts)
		ans.name = "other"
		ans.dir = chosen

		var out bytes.Buffer
		p := &promptIO{in: strings.NewReader("y\n"), out: &out, accessible: true}
		res, err := applyWorkspaceSetup(context.Background(), p, opts, raw, ans)
		if err != nil {
			t.Fatalf("applyWorkspaceSetup failed: %v", err)
		}
		if res.dir != chosen || !res.startNow {
			t.Errorf("result = %+v, want dir %s and startNow true", res, chosen)
		}
	})

	t.Run("start question is skipped when the caller starts the workspace", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeCustomConfig(t)
		raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: test file
		if err != nil {
			t.Fatal(err)
		}

		opts := setupOptions{configPath: cfgPath, targetDir: cwd}
		ans := defaultWorkspaceSetupAnswers(opts)
		ans.name = "embedded"
		ans.dir = cwd

		var out bytes.Buffer
		p := &promptIO{out: &out, accessible: true}
		res, err := applyWorkspaceSetup(context.Background(), p, opts, raw, ans)
		if err != nil {
			t.Fatalf("applyWorkspaceSetup failed: %v", err)
		}
		if res.name != "embedded" || res.startNow {
			t.Errorf("result = %+v, want name embedded and startNow false", res)
		}
		if strings.Contains(out.String(), "Start workspace") {
			t.Errorf("expected no start question, got:\n%s", out.String())
		}
	})

	t.Run("broad path is refused with --yes", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}

		_, err := runSetup(context.Background(), p, setupOptions{configPath: cfgPath, targetDir: "/"})
		if err == nil {
			t.Fatal("expected an error for a broad workspace path with --yes")
		}
		assertContains(t, err.Error(), "very broad path")
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected %s not to be written", cfgPath)
		}
	})

	t.Run("broad path group is shown for root and rejects declined confirmation", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeCustomConfig(t)
		raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: test file
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		opts := setupOptions{configPath: cfgPath, targetDir: "/"}
		ans := defaultWorkspaceSetupAnswers(opts)
		ans.name = "narrow"
		ans.dir = "/"
		ans.confirmBroad = false

		form := buildWorkspaceSetupForm(&ans, opts, cfg, raw)
		form.WithTheme(compactTheme())
		if cmd := form.Init(); cmd != nil {
			flushCmd(form, cmd)
		}
		// Advance past name group and dir group to reach broad path confirm.
		for range 2 {
			_, cmd := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
			flushCmd(form, cmd)
		}
		view := form.View()
		assertContains(t, view, "very broad path")

		// Submitting with confirmBroad = false triggers validation error.
		_, cmd := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
		flushCmd(form, cmd)
		if len(form.Errors()) == 0 {
			t.Fatal("expected validation error when declining broad path")
		}
	})

	t.Run("cancel while waiting for an answer stops without writing", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		pr, pw := io.Pipe()
		t.Cleanup(func() { _ = pw.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		out := &signalWriter{needle: "Default coding agent", hit: make(chan struct{})}
		p := &promptIO{in: pr, out: out}
		errc := make(chan error, 1)
		go func() {
			_, err := runSetup(ctx, p, setupOptions{configPath: cfgPath, targetDir: cwd})
			errc <- err
		}()

		select {
		case <-out.hit:
		case <-time.After(10 * time.Second):
			t.Fatal("wizard never asked the first question")
		}
		cancel()
		select {
		case err := <-errc:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("runSetup error = %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("wizard did not stop after cancellation")
		}
		if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
			t.Errorf("expected %s not to be written", cfgPath)
		}
	})

	t.Run("dry-run prints config without writing file", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}

		if _, err := runSetup(context.Background(), p, setupOptions{configPath: cfgPath, targetDir: cwd, dryRun: true}); err != nil {
			t.Fatalf("runSetup dry-run failed: %v", err)
		}
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected %s not to exist after dry-run", cfgPath)
		}
		assertContains(t, out.String(), "dry run, nothing written")
	})

	t.Run("symlink to home directory is treated as broad while retaining symlink path", func(t *testing.T) {
		t.Parallel()

		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot get home dir: %v", err)
		}
		tmpDir, err := os.MkdirTemp(cwd, ".test-symlink-*")
		if err != nil {
			t.Fatalf("mkdir temp in cwd: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
		link := filepath.Join(tmpDir, "home-link")
		if err := os.Symlink(home, link); err != nil {
			t.Fatalf("create symlink: %v", err)
		}

		cfgPath := writeCustomConfig(t)
		raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: test file
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatal(err)
		}

		opts := setupOptions{configPath: cfgPath, targetDir: link}
		ans := defaultWorkspaceSetupAnswers(opts)
		ans.name = "linked"
		ans.dir = link
		ans.confirmBroad = true

		form := buildWorkspaceSetupForm(&ans, opts, cfg, raw)
		output := driveFormViews(t, form)
		assertContains(t, output, "very broad path")

		var out bytes.Buffer
		p := &promptIO{out: &out, yes: true}
		res, err := applyWorkspaceSetup(context.Background(), p, opts, raw, ans)
		if err != nil {
			t.Fatalf("applyWorkspaceSetup failed: %v", err)
		}
		if res.dir != link {
			t.Errorf("res.dir = %q, want original symlink %q", res.dir, link)
		}
		cfg, err = config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		if got := cfg.Workspaces["linked"].Paths; len(got) != 1 || got[0] != link {
			t.Errorf("workspace paths = %v, want [%s]", got, link)
		}
	})

	t.Run("normalizeProviders returns nil for all and preserves order for subsets", func(t *testing.T) {
		t.Parallel()

		h, _ := agent.Lookup("opencode")
		if got := normalizeProviders(h, h.ProviderNames()); got != nil {
			t.Errorf("normalizeProviders(all) = %v, want nil", got)
		}
		if got := normalizeProviders(h, []string{"openai", "anthropic"}); !slices.Equal(got, []string{"anthropic", "openai"}) {
			t.Errorf("normalizeProviders(subset) = %v, want [anthropic openai]", got)
		}
		if got := normalizeProviders(h, []string{}); got == nil || len(got) != 0 {
			t.Errorf("normalizeProviders(empty) = %v, want non-nil empty slice", got)
		}
	})

	t.Run("non-finite CPU values are rejected at prompt", func(t *testing.T) {
		t.Parallel()

		for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "0", "-2"} {
			if err := validatePositiveFloat(bad); err == nil {
				t.Errorf("validatePositiveFloat(%q) = nil, want error", bad)
			}
		}
		for _, good := range []string{"1", "2.5", "4"} {
			if err := validatePositiveFloat(good); err != nil {
				t.Errorf("validatePositiveFloat(%q) = %v, want nil", good, err)
			}
		}
	})
}
