package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
)

func answers(lines ...string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

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
		w := &wizardPrompter{in: answers(), out: &out, yes: true}

		res, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd, askStart: true})
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
		assertContains(t, out.String(), "Default VM memory [4g]: 4g")
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

	t.Run("interactive first-time setup explains each question", func(t *testing.T) {
		t.Parallel()

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		w := &wizardPrompter{in: answers("", "", "", "", "", "", "", "", "", "", "", ""), out: &out}

		if _, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd}); err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		output := out.String()
		for _, want := range []string{
			"jailoc installs this agent in the VM and starts it in each session.",
			"A read-only copy, so commits in the VM use your name and email.",
			"Lets SSH and Git in the VM use your keys and ~/.ssh/known_hosts.",
			"processes in the VM can sign and decrypt with them.",
			"Private networks stay blocked unless you allow them explicitly.",
			"such as gitlab.corp.example or 10.20.0.0/16.",
			"the host Docker socket is not shared.",
			"Virtual CPUs per VM, rounded up to a whole number.",
			"Used in commands such as \"jailoc up <name>\".",
			"The agent can modify any file in it.",
		} {
			assertContains(t, output, want)
		}

		cpuAt := strings.Index(output, "Default VM CPUs")
		limitAt := strings.Index(output, "RAM limit per VM, not memory reserved on the host.")
		freedAt := strings.Index(output, "Allocated as needed; freed pages and unused file cache return to the host.")
		memoryAt := strings.Index(output, "Default VM memory")
		if cpuAt < 0 || limitAt < cpuAt || freedAt < limitAt || memoryAt < freedAt {
			t.Errorf("expected both RAM lines, in order, between the CPU and memory prompts, got:\n%s", output)
		}
	})

	t.Run("workspace setup with --yes prints no explanations", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeCustomConfig(t)
		var out bytes.Buffer
		w := &wizardPrompter{in: answers(), out: &out, yes: true}

		if _, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd}); err != nil {
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
		w := &wizardPrompter{in: answers(), out: &out, yes: true}

		res, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd, defaultName: "project"})
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
		var out bytes.Buffer
		w := &wizardPrompter{
			in:  answers("agrologo", cwd, "claude", "yes", "custom", "gitlab.seznam.net, 10.10.0.0/16", "yes", "no"),
			out: &out,
		}

		res, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd, askStart: true})
		if err != nil {
			t.Fatalf("runSetup workspace mode failed: %v", err)
		}
		if res.name != "agrologo" || res.startNow {
			t.Errorf("result = %+v, want name agrologo and startNow false", res)
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
		if len(ws.Paths) != 1 || ws.Paths[0] != cwd {
			t.Errorf("ws.Paths = %v, want [%s]", ws.Paths, cwd)
		}
		for _, want := range []string{
			"\"inherit\" uses the [defaults] agent.",
			"\"internet\" adds all public hosts to them, \"custom\" the hosts you list.",
			"such as *.corp.example or 10.20.0.0/16.",
		} {
			assertContains(t, out.String(), want)
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
		var out bytes.Buffer
		w := &wizardPrompter{
			in:  answers("other", chosen, "inherit", "inherit", "inherit", "yes", "yes"),
			out: &out,
		}

		res, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd, askStart: true})
		if err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		if res.dir != chosen || !res.startNow {
			t.Errorf("result = %+v, want dir %s and startNow true", res, chosen)
		}
	})

	t.Run("start question is skipped when the caller starts the workspace", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeCustomConfig(t)
		var out bytes.Buffer
		w := &wizardPrompter{
			in:  answers("embedded", cwd, "inherit", "inherit", "inherit", "yes"),
			out: &out,
		}

		res, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd})
		if err != nil {
			t.Fatalf("runSetup failed: %v", err)
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
		w := &wizardPrompter{in: answers(), out: &out, yes: true}

		_, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: "/"})
		if err == nil {
			t.Fatal("expected an error for a broad workspace path with --yes")
		}
		assertContains(t, err.Error(), "very broad path")
		if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
			t.Errorf("expected %s not to be written", cfgPath)
		}
	})

	t.Run("declined broad path asks for another directory", func(t *testing.T) {
		t.Parallel()

		cfgPath := writeCustomConfig(t)
		var out bytes.Buffer
		w := &wizardPrompter{
			in:  answers("narrow", "/", "no", cwd, "inherit", "inherit", "inherit", "yes"),
			out: &out,
		}

		res, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: "/"})
		if err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		assertContains(t, out.String(), "very broad path")
		if res.dir != cwd {
			t.Errorf("res.dir = %q, want %q", res.dir, cwd)
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
		w := &wizardPrompter{in: bufio.NewReader(pr), out: out}
		errc := make(chan error, 1)
		go func() {
			_, err := runSetup(ctx, w, setupOptions{configPath: cfgPath, targetDir: cwd})
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
		w := &wizardPrompter{in: answers(), out: &out, yes: true}

		if _, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: cwd, dryRun: true}); err != nil {
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
		var out bytes.Buffer
		w := &wizardPrompter{
			in:  answers("linked", link, "yes", "inherit", "inherit", "inherit", "yes"),
			out: &out,
		}

		res, err := runSetup(context.Background(), w, setupOptions{configPath: cfgPath, targetDir: link})
		if err != nil {
			t.Fatalf("runSetup failed: %v", err)
		}
		assertContains(t, out.String(), "very broad path")
		if res.dir != link {
			t.Errorf("res.dir = %q, want original symlink %q", res.dir, link)
		}
		cfg, err := config.LoadFrom(cfgPath)
		if err != nil {
			t.Fatalf("LoadFrom: %v", err)
		}
		if got := cfg.Workspaces["linked"].Paths; len(got) != 1 || got[0] != link {
			t.Errorf("workspace paths = %v, want [%s]", got, link)
		}
	})

	t.Run("ambiguous single-letter shortcut is rejected while unique shortcut succeeds", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		w := &wizardPrompter{
			ctx: context.Background(),
			in:  answers("c", "copilot"),
			out: &out,
		}
		idx, err := w.choose("Agent?", []string{"opencode", "claude", "copilot"}, 2)
		if err != nil {
			t.Fatalf("choose failed: %v", err)
		}
		if idx != 2 {
			t.Errorf("idx = %d, want 2 (copilot)", idx)
		}
		assertContains(t, out.String(), "answer one of: opencode, claude, copilot")

		out.Reset()
		w.in = answers("o")
		idx, err = w.choose("Agent?", []string{"opencode", "claude", "copilot"}, 2)
		if err != nil {
			t.Fatalf("choose unique shortcut failed: %v", err)
		}
		if idx != 0 {
			t.Errorf("idx = %d, want 0 (opencode)", idx)
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
