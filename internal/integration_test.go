//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

var (
	binaryPath string

	testHomesMu sync.Mutex
	testHomes   []string
)

type integrationConfig struct {
	Image struct {
		Repository string `toml:"repository"`
	} `toml:"image"`
	Workspaces map[string]struct {
		Paths []string `toml:"paths"`
	} `toml:"workspaces"`
}

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "jailoc-integration-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}

	binaryPath = filepath.Join(tmpDir, "jailoc")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/jailoc")
	buildCmd.Dir = projectRoot()
	if out, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build jailoc: %v\n%s\n", err, string(out))
		_ = os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	suiteHome, err := os.MkdirTemp("", "jailoc-integration-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create suite home: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", suiteHome); err != nil {
		fmt.Fprintf(os.Stderr, "set HOME: %v\n", err)
		_ = os.RemoveAll(suiteHome)
		_ = os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	code := m.Run()

	cleanupAllHomes()
	if err := os.Setenv("HOME", oldHome); err != nil {
		fmt.Fprintf(os.Stderr, "restore HOME: %v\n", err)
	}
	if err := os.RemoveAll(suiteHome); err != nil {
		fmt.Fprintf(os.Stderr, "remove suite home: %v\n", err)
	}
	if err := os.RemoveAll(tmpDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove temp dir: %v\n", err)
	}

	os.Exit(code)
}

func TestConfigAutoCreation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	home := testHome(t)
	out, err := runJailoc(ctx, home, "config")
	if err != nil {
		t.Fatalf("run jailoc config: %v\noutput:\n%s", err, out)
	}

	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	if _, statErr := os.Stat(configPath); statErr != nil {
		t.Errorf("config file not created at %q: %v", configPath, statErr)
	}
}

func TestUpStatusDownLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	if err := writeMinimalConfig(home, t.TempDir()); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}

	upOut, upErr := runJailoc(ctx, home, "up")
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("run jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	statusOut, statusErr := runJailoc(ctx, home, "status")
	if statusErr != nil {
		t.Fatalf("run jailoc status after up: %v\noutput:\n%s", statusErr, statusOut)
	}
	if !strings.Contains(strings.ToLower(statusOut), "running") {
		t.Errorf("expected running status, got output:\n%s", statusOut)
	}

	downOut, downErr := runJailoc(ctx, home, "down")
	if downErr != nil {
		t.Fatalf("run jailoc down: %v\noutput:\n%s", downErr, downOut)
	}

	statusAfterOut, statusAfterErr := runJailoc(ctx, home, "status")
	if statusAfterErr != nil {
		t.Fatalf("run jailoc status after down: %v\noutput:\n%s", statusAfterErr, statusAfterOut)
	}
	if strings.Contains(strings.ToLower(statusAfterOut), "status:    running") {
		t.Errorf("expected non-running status after down, got output:\n%s", statusAfterOut)
	}
}

func TestAddPathPersists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	home := testHome(t)
	workspaceDir := t.TempDir()
	if err := writeMinimalConfig(home, workspaceDir); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}

	addDir := t.TempDir()
	addOut, addErr := runJailoc(ctx, home, "add", addDir)
	if addErr != nil {
		t.Fatalf("run jailoc add: %v\noutput:\n%s", addErr, addOut)
	}

	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	var cfg integrationConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("decode config %q: %v", configPath, err)
	}

	ws, ok := cfg.Workspaces["default"]
	if !ok {
		t.Errorf("default workspace missing in config")
		return
	}

	found := false
	for _, p := range ws.Paths {
		if p == addDir {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("added path %q not found in config paths: %#v", addDir, ws.Paths)
	}
}

func TestInvalidConfigErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	home := testHome(t)
	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	if err := os.WriteFile(configPath, []byte("[workspaces.default\npaths = [\"/tmp\"]\n"), 0o644); err != nil {
		t.Fatalf("write malformed config %q: %v", configPath, err)
	}

	out, err := runJailoc(ctx, home, "up")
	if err == nil {
		t.Errorf("expected jailoc up to fail with malformed config, output:\n%s", out)
	}
}

func TestUpIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	if err := writeMinimalConfig(home, t.TempDir()); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}

	firstUpOut, firstUpErr := runJailoc(ctx, home, "up")
	if firstUpErr != nil {
		if isImagePullOrAuthFailure(firstUpOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("first jailoc up failed: %v\noutput:\n%s", firstUpErr, firstUpOut)
	}

	secondUpOut, secondUpErr := runJailoc(ctx, home, "up")
	if secondUpErr != nil {
		t.Fatalf("second jailoc up failed: %v\noutput:\n%s", secondUpErr, secondUpOut)
	}
	if !strings.Contains(strings.ToLower(secondUpOut), "already running") {
		t.Errorf("expected idempotent up output to contain 'already running', got:\n%s", secondUpOut)
	}

	downOut, downErr := runJailoc(ctx, home, "down")
	if downErr != nil {
		t.Fatalf("run jailoc down: %v\noutput:\n%s", downErr, downOut)
	}
}

func projectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func testHome(t *testing.T) string {
	t.Helper()

	home, err := os.MkdirTemp("", "jailoc-integration-home-")
	if err != nil {
		t.Fatalf("create test home: %v", err)
	}

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir %q: %v", configDir, err)
	}

	registerHome(home)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = runJailoc(ctx, home, "down")
		_ = os.RemoveAll(home)
	})

	return home
}

func registerHome(home string) {
	testHomesMu.Lock()
	defer testHomesMu.Unlock()
	testHomes = append(testHomes, home)
}

func cleanupAllHomes() {
	testHomesMu.Lock()
	homes := append([]string(nil), testHomes...)
	testHomesMu.Unlock()

	for _, home := range homes {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_, _ = runJailoc(ctx, home, "down")
		cancel()
		_ = os.RemoveAll(home)
	}
}

func runJailoc(ctx context.Context, home string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = projectRoot()
	cmd.Env = append(os.Environ(), "HOME="+home)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("run jailoc %q: %w", strings.Join(args, " "), err)
	}

	return string(out), nil
}

func writeMinimalConfig(home, workspacePath string) error {
	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	content := fmt.Sprintf(`[image]
repository = "registry.example.com/jailoc-base"

[workspaces.default]
paths = [%q]
`, workspacePath)

	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write minimal config to %q: %w", configPath, err)
	}

	return nil
}

func dockerAvailable(ctx context.Context) bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}

	infoCmd := exec.CommandContext(ctx, "docker", "info")
	if err := infoCmd.Run(); err != nil {
		return false
	}

	return true
}

func isImagePullOrAuthFailure(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "pull") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "denied") ||
		strings.Contains(lower, "resolve base image")
}
