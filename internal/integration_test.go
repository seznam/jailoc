//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

const integrationPassword = "jailoc-integration-password"

type integrationConfig struct {
	Base struct {
	} `toml:"base"`
	Workspaces map[string]struct {
		Paths   []string `toml:"paths"`
		Env     []string `toml:"env"`
		EnvFile []string `toml:"env_file"`
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
	_ = os.RemoveAll(filepath.Join(projectRoot(), ".integration-tmp"))
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	if err := writeMinimalConfig(home, testWorkspaceDir(t)); err != nil {
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
	workspaceDir := testWorkspaceDir(t)
	if err := writeMinimalConfig(home, workspaceDir); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}

	addDir := testWorkspaceDir(t)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	if err := writeMinimalConfig(home, testWorkspaceDir(t)); err != nil {
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

func TestRestartLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	if err := writeMinimalConfig(home, testWorkspaceDir(t)); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}

	upOut, upErr := runJailoc(ctx, home, "up")
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("run jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	restartOut, restartErr := runJailoc(ctx, home, "restart")
	if restartErr != nil {
		t.Fatalf("run jailoc restart: %v\noutput:\n%s", restartErr, restartOut)
	}

	statusOut, statusErr := runJailoc(ctx, home, "status")
	if statusErr != nil {
		t.Fatalf("run jailoc status after restart: %v\noutput:\n%s", statusErr, statusOut)
	}
	statusLower := strings.ToLower(statusOut)
	if strings.Contains(statusLower, "not running") || !strings.Contains(statusLower, "running") {
		t.Errorf("expected running status after restart, got output:\n%s", statusOut)
	}

	downOut, downErr := runJailoc(ctx, home, "down")
	if downErr != nil {
		t.Fatalf("run jailoc down: %v\noutput:\n%s", downErr, downOut)
	}

	// Restart from stopped state — covers "if not running, starts it" behavior.
	restartOut, restartErr = runJailoc(ctx, home, "restart")
	if restartErr != nil {
		if isImagePullOrAuthFailure(restartOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("run jailoc restart from stopped state: %v\noutput:\n%s", restartErr, restartOut)
	}

	statusOut, statusErr = runJailoc(ctx, home, "status")
	if statusErr != nil {
		t.Fatalf("run jailoc status after restart from stopped state: %v\noutput:\n%s", statusErr, statusOut)
	}
	statusLower = strings.ToLower(statusOut)
	if strings.Contains(statusLower, "not running") || !strings.Contains(statusLower, "running") {
		t.Errorf("expected running status after restart from stopped state, got output:\n%s", statusOut)
	}

	downOut, downErr = runJailoc(ctx, home, "down")
	if downErr != nil {
		t.Fatalf("run jailoc down after cold restart: %v\noutput:\n%s", downErr, downOut)
	}
}

func TestEnvVarsReachContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)

	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	content := fmt.Sprintf(`[base]

[workspaces.default]
paths = [%q]
env = ["TEST_CUSTOM_VAR=hello_from_jailoc"]
`, workspaceDir)
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	upOut, upErr := runJailoc(ctx, home, "up")
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	containerName := "jailoc-default-opencode-1"
	execOut, execErr := exec.CommandContext(ctx, "docker", "exec", containerName, "sh", "-c", "echo $TEST_CUSTOM_VAR").CombinedOutput()
	if execErr != nil {
		t.Fatalf("docker exec: %v\noutput:\n%s", execErr, string(execOut))
	}
	got := strings.TrimSpace(string(execOut))
	if got != "hello_from_jailoc" {
		t.Errorf("TEST_CUSTOM_VAR: got %q, want %q", got, "hello_from_jailoc")
	}

	downOut, downErr := runJailoc(ctx, home, "down")
	if downErr != nil {
		t.Fatalf("jailoc down: %v\noutput:\n%s", downErr, downOut)
	}
}

func TestEnvFileVarsReachContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)

	envFile, err := os.CreateTemp("", "jailoc-integration-*.env")
	if err != nil {
		t.Fatalf("create temp env file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(envFile.Name()) })
	if err := os.WriteFile(envFile.Name(), []byte("FILE_VAR=from_file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	content := fmt.Sprintf(`[base]

[workspaces.default]
paths = [%q]
env_file = [%q]
`, workspaceDir, envFile.Name())
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	upOut, upErr := runJailoc(ctx, home, "up")
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	containerName := "jailoc-default-opencode-1"
	execOut, execErr := exec.CommandContext(ctx, "docker", "exec", containerName, "sh", "-c", "echo $FILE_VAR").CombinedOutput()
	if execErr != nil {
		t.Fatalf("docker exec: %v\noutput:\n%s", execErr, string(execOut))
	}
	got := strings.TrimSpace(string(execOut))
	if got != "from_file" {
		t.Errorf("FILE_VAR: got %q, want %q", got, "from_file")
	}

	downOut, downErr := runJailoc(ctx, home, "down")
	if downErr != nil {
		t.Fatalf("jailoc down: %v\noutput:\n%s", downErr, downOut)
	}
}

// TestSecretsReachContainer exercises all four destination × source secret
// combinations end to end. Together with
// TestSecretEnvManifestMissingSecretAbortsStartup it is the only coverage for
// the entrypoint.sh secret path: manifest parsing, the read of
// /run/secrets/<name> as root, and the setpriv handover to the agent.
//
// The agent's own environment is deliberately not asserted. entrypoint.sh
// injects env-destination secrets into the agent process only, so a fresh
// `docker exec` never inherits them, and /proc/1/environ is unreadable once
// setpriv's UID change clears the process dumpable flag (reading it would need
// CAP_SYS_PTRACE, which the compose file drops). What is observable is that
// PID 1 reached the agent at all: entrypoint.sh aborts before setpriv if any
// manifest entry cannot be resolved, so an agent PID 1 proves the export loop
// ran over every name in the manifest.
func TestSecretsReachContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	const envSourceValue = "value-from-host-env"
	t.Setenv("JAILOC_IT_ENV_SOURCE", envSourceValue)

	secretDir := t.TempDir()

	// 0600 on purpose: an env-destination secret is read by entrypoint.sh as
	// root before setpriv, so it must not require world-readable permissions.
	envFromFilePath := filepath.Join(secretDir, "env-source")
	if err := os.WriteFile(envFromFilePath, []byte("value-from-host-file\n"), 0o600); err != nil {
		t.Fatalf("write env-destination secret file: %v", err)
	}

	// 0644: a file-destination secret is bind-mounted straight to UID 1000.
	fileFromFilePath := filepath.Join(secretDir, "file-source")
	if err := os.WriteFile(fileFromFilePath, []byte("value-mounted-from-host-file"), 0o644); err != nil {
		t.Fatalf("write file-destination secret file: %v", err)
	}

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)

	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	content := fmt.Sprintf(`[base]

[workspaces.default]
paths = [%q]

[workspaces.default.secrets.env.IT_ENV_FROM_ENV]
from_env = "JAILOC_IT_ENV_SOURCE"

[workspaces.default.secrets.env.IT_ENV_FROM_FILE]
from_file = %q

[workspaces.default.secrets.file.it_file_from_env]
from_env = "JAILOC_IT_ENV_SOURCE"

[workspaces.default.secrets.file.it_file_from_file]
from_file = %q
`, workspaceDir, envFromFilePath, fileFromFilePath)
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	upOut, upErr := runJailoc(ctx, home, "up")
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	const containerName = "jailoc-default-opencode-1"

	// PID 1 becomes the agent only after entrypoint.sh resolved every manifest
	// entry and reached its final exec: the export loop aborts the container
	// otherwise. jailoc up returns while the entrypoint is still root, setting up
	// iptables and waiting for dind, so wait for the handover here.
	waitForAgentPID1(ctx, t, containerName)

	// Environment destinations: entrypoint.sh reads these as root, so the source
	// file needs no world-readable bit and the value must land in /run/secrets.
	for name, want := range map[string]string{
		"IT_ENV_FROM_ENV":  envSourceValue,
		"IT_ENV_FROM_FILE": "value-from-host-file",
	} {
		if got := dockerExec(ctx, t, containerName, "0", "cat /run/secrets/"+name); strings.TrimSpace(got) != want {
			t.Errorf("/run/secrets/%s: got %q, want %q", name, strings.TrimSpace(got), want)
		}
	}

	// File destinations must be readable by the unprivileged agent itself.
	for name, want := range map[string]string{
		"it_file_from_env":  envSourceValue,
		"it_file_from_file": "value-mounted-from-host-file",
	} {
		got := dockerExec(ctx, t, containerName, "1000", "cat /run/secrets/"+name)
		if strings.TrimSpace(got) != want {
			t.Errorf("/run/secrets/%s: got %q, want %q", name, strings.TrimSpace(got), want)
		}
	}

	// The manifest is also mounted into the privileged dind sidecar, so it must
	// list env-destination secret names only — never a value or a host source.
	manifestOut := dockerExec(ctx, t, containerName, "0", "cat /etc/jailoc/secret-env")
	if manifestOut != "IT_ENV_FROM_ENV\nIT_ENV_FROM_FILE\n" {
		t.Errorf("secret-env manifest = %q, want the two env secret names only", manifestOut)
	}

	downOut, downErr := runJailoc(ctx, home, "down")
	if downErr != nil {
		t.Fatalf("jailoc down: %v\noutput:\n%s", downErr, downOut)
	}
}

// TestSecretEnvManifestMissingSecretAbortsStartup covers the fail-closed branch
// of the entrypoint.sh export loop. jailoc's own up-time validation makes this
// state unreachable through the CLI, so the manifest is corrupted on the host
// (it is bind-mounted into the container) and the container restarted directly.
func TestSecretEnvManifestMissingSecretAbortsStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	t.Setenv("JAILOC_IT_ENV_SOURCE", "value-from-host-env")

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)

	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	content := fmt.Sprintf(`[base]

[workspaces.default]
paths = [%q]

[workspaces.default.secrets.env.IT_ENV_FROM_ENV]
from_env = "JAILOC_IT_ENV_SOURCE"
`, workspaceDir)
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	upOut, upErr := runJailoc(ctx, home, "up")
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	const containerName = "jailoc-default-opencode-1"

	manifestPath := filepath.Join(home, ".config", "jailoc", "workspaces", "default", "secret-env")
	if err := os.WriteFile(manifestPath, []byte("IT_ENV_FROM_ENV\nIT_NEVER_PROVISIONED\n"), 0o600); err != nil {
		t.Fatalf("corrupt secret-env manifest: %v", err)
	}

	if out, err := exec.CommandContext(ctx, "docker", "restart", containerName).CombinedOutput(); err != nil {
		t.Fatalf("docker restart: %v\noutput:\n%s", err, string(out))
	}

	exitCode, logs := waitForContainerExit(ctx, t, containerName)
	if exitCode == 0 {
		t.Fatalf("container survived a manifest entry with no matching secret; logs:\n%s", logs)
	}
	for _, want := range []string{"FATAL", "/run/secrets/IT_NEVER_PROVISIONED"} {
		if !strings.Contains(logs, want) {
			t.Errorf("container logs are missing %q:\n%s", want, logs)
		}
	}
}

// waitForAgentPID1 blocks until the container's PID 1 runs as the unprivileged
// agent, which is the observable signal that entrypoint.sh finished its root
// phase — including the secret export loop — and exec'd through setpriv.
func waitForAgentPID1(ctx context.Context, t *testing.T, container string) {
	t.Helper()

	var last string
	for range 90 {
		out, err := exec.CommandContext(ctx, "docker", "exec", "-u", "0", container, "ps", "-o", "user=", "-p", "1").CombinedOutput()
		if err == nil {
			if last = strings.TrimSpace(string(out)); last == "agent" {
				return
			}
		}
		time.Sleep(time.Second)
	}

	logs, _ := exec.CommandContext(ctx, "docker", "logs", container).CombinedOutput()
	t.Fatalf("PID 1 still runs as %q after 90s, want \"agent\": entrypoint.sh did not complete the setpriv handover; logs:\n%s", last, string(logs))
}

// waitForContainerExit polls until the container stops, then returns its exit
// code together with its logs.
func waitForContainerExit(ctx context.Context, t *testing.T, container string) (int, string) {
	t.Helper()

	for range 60 {
		out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}} {{.State.ExitCode}}", container).Output()
		if err != nil {
			t.Fatalf("docker inspect %s: %v", container, err)
		}
		running, code, found := strings.Cut(strings.TrimSpace(string(out)), " ")
		if !found {
			t.Fatalf("unexpected docker inspect output %q", string(out))
		}
		if running == "false" {
			exitCode, err := strconv.Atoi(code)
			if err != nil {
				t.Fatalf("parse exit code %q: %v", code, err)
			}
			logs, _ := exec.CommandContext(ctx, "docker", "logs", container).CombinedOutput()
			return exitCode, string(logs)
		}
		time.Sleep(time.Second)
	}

	logs, _ := exec.CommandContext(ctx, "docker", "logs", container).CombinedOutput()
	t.Fatalf("container %s still running after 60s; logs:\n%s", container, string(logs))
	return 0, ""
}

// TestSecretsMissingHostSourceFailsFast asserts that jailoc up rejects an unset
// host source before starting anything, instead of letting entrypoint.sh abort
// on a missing /run/secrets/<name>.
func TestSecretsMissingHostSourceFailsFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)

	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	content := fmt.Sprintf(`[base]

[workspaces.default]
paths = [%q]

[workspaces.default.secrets.env.IT_MISSING]
from_env = "JAILOC_IT_DEFINITELY_UNSET"
`, workspaceDir)
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	upOut, upErr := runJailoc(ctx, home, "up")
	if upErr == nil {
		t.Fatalf("jailoc up succeeded with an unset host source:\n%s", upOut)
	}
	for _, want := range []string{"IT_MISSING", "JAILOC_IT_DEFINITELY_UNSET", "is not set"} {
		if !strings.Contains(upOut, want) {
			t.Errorf("jailoc up output is missing %q:\n%s", want, upOut)
		}
	}

	manifestPath := filepath.Join(home, ".config", "jailoc", "workspaces", "default", "secret-env")
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("secret-env manifest was written for a rejected config, stat error = %v", err)
	}
}

func TestCABundleForwardingLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)
	workspaceName := uniqueWorkspaceName(t, "ca-bundle")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		_, _ = runJailoc(cleanupCtx, home, "down", workspaceName)
	})
	bundle, serverCertificate := integrationCertificateBundle(t, "jailoc-ca-forwarding")
	bundlePath := filepath.Join(home, "integration-ca.pem")
	if err := os.WriteFile(bundlePath, bundle, 0o600); err != nil {
		t.Fatalf("write CA bundle: %v", err)
	}
	writeCAIntegrationConfig(t, home, workspaceName, workspaceDir, "true")

	upOut, upErr := runJailocWithEnv(ctx, home, map[string]string{"SSL_CERT_FILE": bundlePath}, "up", workspaceName)
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	materializedPath := filepath.Join(home, ".config", "jailoc", "workspaces", workspaceName, "ca-bundle.pem")
	materialized, err := os.ReadFile(materializedPath)
	if err != nil {
		t.Fatalf("read materialized CA bundle: %v", err)
	}
	if string(materialized) != string(bundle) {
		t.Fatal("materialized CA bundle differs from selected source")
	}
	marker := strings.Split(string(bundle), "\n")[1]
	opencodeContainer := "jailoc-" + workspaceName + "-opencode-1"
	waitForAgentPID1(ctx, t, opencodeContainer)
	opencodeInstalled := dockerExec(ctx, t, opencodeContainer, "0", "grep -F '"+marker+"' /etc/ssl/certs/ca-certificates.crt")
	if !strings.Contains(opencodeInstalled, marker) {
		t.Fatalf("opencode system CA bundle does not contain certificate marker %q", marker)
	}
	nodeTrust := dockerExec(ctx, t, opencodeContainer, "1000", "tr '\\0' '\\n' < /proc/1/environ | grep '^NODE_USE_SYSTEM_CA=1$'")
	if !strings.Contains(nodeTrust, "NODE_USE_SYSTEM_CA=1") {
		t.Fatal("opencode process does not enable Node system CA trust")
	}
	dindContainer := "jailoc-" + workspaceName + "-dind-1"
	installed := dockerExec(ctx, t, dindContainer, "0", "grep -F '"+marker+"' /etc/ssl/certs/ca-certificates.crt")
	if !strings.Contains(installed, marker) {
		t.Fatalf("DinD system CA bundle does not contain certificate marker %q", marker)
	}
	verifyForwardedCATLS(ctx, t, serverCertificate, opencodeContainer, dindContainer)
	oldDindID, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Id}}", dindContainer).Output()
	if err != nil {
		t.Fatalf("inspect original DinD container: %v", err)
	}
	replacement, replacementCertificate := integrationCertificateBundle(t, "jailoc-ca-replacement")
	if err := os.WriteFile(bundlePath, replacement, 0o600); err != nil {
		t.Fatalf("write replacement CA bundle: %v", err)
	}
	additionalPath := filepath.Join(workspaceDir, "additional")
	if err := os.Mkdir(additionalPath, 0o755); err != nil {
		t.Fatalf("create additional workspace path: %v", err)
	}
	addOut, addErr := runJailocWithEnv(ctx, home, map[string]string{"SSL_CERT_FILE": bundlePath}, "add", "--workspace", workspaceName, additionalPath)
	if addErr != nil {
		t.Fatalf("jailoc add after CA rotation: %v\noutput:\n%s", addErr, addOut)
	}
	newDindID, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Id}}", dindContainer).Output()
	if err != nil {
		t.Fatalf("inspect updated DinD container: %v", err)
	}
	if string(newDindID) == string(oldDindID) {
		t.Fatal("jailoc add kept the original DinD container after CA rotation")
	}
	replacementMarker := strings.Split(string(replacement), "\n")[1]
	for _, container := range []string{opencodeContainer, dindContainer} {
		if container == opencodeContainer {
			waitForAgentPID1(ctx, t, container)
		}
		dockerExec(ctx, t, container, "0", "grep -F '"+replacementMarker+"' /etc/ssl/certs/ca-certificates.crt")
		dockerExec(ctx, t, container, "0", "if grep -Fq '"+marker+"' /etc/ssl/certs/ca-certificates.crt; then exit 1; fi")
	}
	verifyForwardedCATLS(ctx, t, replacementCertificate, opencodeContainer, dindContainer)
	marker = replacementMarker
	for _, container := range []string{opencodeContainer, dindContainer} {
		if out, err := exec.CommandContext(ctx, "docker", "restart", container).CombinedOutput(); err != nil {
			t.Fatalf("restart %s with valid CA: %v\noutput:\n%s", container, err, out)
		}
		if container == opencodeContainer {
			waitForAgentPID1(ctx, t, container)
		}
		count := strings.TrimSpace(dockerExec(ctx, t, container, "0", "grep -Fc '"+marker+"' /etc/ssl/certs/ca-certificates.crt"))
		if count != "1" {
			t.Fatalf("%s CA certificate copies after restart = %s, want 1", container, count)
		}
	}
	if err := os.Remove(materializedPath); err != nil {
		t.Fatalf("remove materialized CA bundle for invalid-path probe: %v", err)
	}
	for _, container := range []string{opencodeContainer, dindContainer} {
		if out, err := exec.CommandContext(ctx, "docker", "restart", container).CombinedOutput(); err != nil {
			t.Fatalf("restart %s without CA: %v\noutput:\n%s", container, err, out)
		}
		if container == opencodeContainer {
			waitForAgentPID1(ctx, t, container)
		}
		dockerExec(ctx, t, container, "0", "if grep -Fq '"+marker+"' /etc/ssl/certs/ca-certificates.crt; then exit 1; fi")
	}
	if err := os.Mkdir(materializedPath, 0o700); err != nil {
		t.Fatalf("replace materialized CA bundle with directory: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "restart", dindContainer).CombinedOutput(); err != nil {
		t.Fatalf("restart DinD with invalid CA path: %v\noutput:\n%s", err, out)
	}
	exitCode, logs := waitForContainerExit(ctx, t, dindContainer)
	if exitCode == 0 || !strings.Contains(logs, "jailoc-dind: FATAL:") {
		t.Fatalf("DinD invalid CA path exit=%d, want non-zero with fatal log; logs:\n%s", exitCode, logs)
	}

	writeCAIntegrationConfig(t, home, workspaceName, workspaceDir, "false")
	restartOut, restartErr := runJailocWithEnv(ctx, home, nil, "restart", workspaceName)
	if restartErr != nil {
		t.Fatalf("jailoc restart with forwarding disabled: %v\noutput:\n%s", restartErr, restartOut)
	}
	if _, err := os.Stat(materializedPath); !os.IsNotExist(err) {
		t.Fatalf("materialized CA bundle after false: stat error = %v, want not exist", err)
	}

	if err := os.WriteFile(materializedPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale CA bundle: %v", err)
	}
	writeCAIntegrationConfig(t, home, workspaceName, workspaceDir, "true")
	restartOut, restartErr = runJailocWithEnv(ctx, home, nil, "restart", workspaceName)
	if restartErr != nil {
		t.Fatalf("jailoc restart without automatic source: %v\noutput:\n%s", restartErr, restartOut)
	}
	if _, err := os.Stat(materializedPath); !os.IsNotExist(err) {
		t.Fatalf("materialized CA bundle without automatic source: stat error = %v, want not exist", err)
	}
}

func TestCABundleForwardingWithoutDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)
	workspaceName := uniqueWorkspaceName(t, "ca-bundle-no-docker")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		_, _ = runJailoc(cleanupCtx, home, "down", workspaceName)
	})
	bundle, serverCertificate := integrationCertificateBundle(t, "jailoc-ca-forwarding-no-docker")
	bundlePath := filepath.Join(home, "integration-ca.pem")
	if err := os.WriteFile(bundlePath, bundle, 0o600); err != nil {
		t.Fatalf("write CA bundle: %v", err)
	}
	writeCAIntegrationConfigWithDocker(t, home, workspaceName, workspaceDir, strconv.Quote(bundlePath), false)

	upOut, upErr := runJailocWithEnv(ctx, home, nil, "up", workspaceName)
	if upErr != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("jailoc up: %v\noutput:\n%s", upErr, upOut)
	}

	materializedPath := filepath.Join(home, ".config", "jailoc", "workspaces", workspaceName, "ca-bundle.pem")
	materialized, err := os.ReadFile(materializedPath)
	if err != nil {
		t.Fatalf("read materialized CA bundle: %v", err)
	}
	if string(materialized) != string(bundle) {
		t.Fatal("materialized CA bundle differs from selected source")
	}
	marker := strings.Split(string(bundle), "\n")[1]
	opencodeContainer := "jailoc-" + workspaceName + "-opencode-1"
	waitForAgentPID1(ctx, t, opencodeContainer)
	installed := dockerExec(ctx, t, opencodeContainer, "0", "grep -F '"+marker+"' /etc/ssl/certs/ca-certificates.crt")
	if !strings.Contains(installed, marker) {
		t.Fatalf("opencode system CA bundle does not contain certificate marker %q", marker)
	}
	verifyForwardedCATLS(ctx, t, serverCertificate, opencodeContainer, "")
	nodeTrust := dockerExec(ctx, t, opencodeContainer, "1000", "tr '\\0' '\\n' < /proc/1/environ | grep '^NODE_USE_SYSTEM_CA=1$'")
	if !strings.Contains(nodeTrust, "NODE_USE_SYSTEM_CA=1") {
		t.Fatal("opencode process does not enable Node system CA trust")
	}
	dindContainer := "jailoc-" + workspaceName + "-dind-1"
	if out, err := exec.CommandContext(ctx, "docker", "inspect", dindContainer).CombinedOutput(); err == nil {
		t.Fatalf("DinD container exists with enable_docker=false:\n%s", out)
	}
}

// dockerExec runs a shell command inside the container as the given UID. Use
// "1000" to assert what the unprivileged agent can reach and "0" to read files
// the agent is not meant to open itself.
func dockerExec(ctx context.Context, t *testing.T, container, uid, script string) string {
	t.Helper()

	out, err := exec.CommandContext(ctx, "docker", "exec", "-u", uid, container, "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec as uid %s %q: %v\noutput:\n%s", uid, script, err, string(out))
	}
	return string(out)
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

// testWorkspaceDir creates a temp directory under the project root instead of
// os.TempDir(). Workspace paths under /tmp conflict with forbiddenMountPrefixes
// validation, which blocks /tmp as a container-internal directory.
func testWorkspaceDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join(projectRoot(), ".integration-tmp")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create workspace base dir %q: %v", base, err)
	}
	dir, err := os.MkdirTemp(base, "ws-")
	if err != nil {
		t.Fatalf("create test workspace dir: %v", err)
	}
	return dir
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
	return runJailocWithEnv(ctx, home, nil, args...)
}

func TestJailocEnvironmentBypassesSystemKeyring(t *testing.T) {
	t.Setenv("OPENCODE_SERVER_PASSWORD", "host-password")

	env := jailocTestEnv("/test/home", map[string]string{"SSL_CERT_FILE": "/test/ca.pem"})

	assertEnvironmentValue(t, env, "HOME", "/test/home")
	assertEnvironmentValue(t, env, "OPENCODE_SERVER_PASSWORD", integrationPassword)
	assertEnvironmentValue(t, env, "SSL_CERT_FILE", "/test/ca.pem")
}

func runJailocWithEnv(ctx context.Context, home string, overrides map[string]string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = projectRoot()
	cmd.Env = jailocTestEnv(home, overrides)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("run jailoc %q: %w", strings.Join(args, " "), err)
	}

	return string(out), nil
}

func jailocTestEnv(home string, overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides)+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "HOME" && key != "OPENCODE_SERVER_PASSWORD" && key != "SSL_CERT_FILE" && key != "NIX_SSL_CERT_FILE" {
			env = append(env, entry)
		}
	}
	env = append(env, "HOME="+home)
	for key, value := range overrides {
		if key == "OPENCODE_SERVER_PASSWORD" {
			continue
		}
		env = append(env, key+"="+value)
	}
	return append(env, "OPENCODE_SERVER_PASSWORD="+integrationPassword)
}

func assertEnvironmentValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	prefix := key + "="
	matches := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			matches++
			if entry != prefix+want {
				t.Fatalf("%s environment value = %q, want %q", key, strings.TrimPrefix(entry, prefix), want)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("%s environment entries = %d, want 1", key, matches)
	}
}

func writeCAIntegrationConfig(t *testing.T, home, workspaceName, workspacePath, value string) {
	t.Helper()
	writeCAIntegrationConfigWithDocker(t, home, workspaceName, workspacePath, value, true)
}

func writeCAIntegrationConfigWithDocker(t *testing.T, home, workspaceName, workspacePath, value string, enableDocker bool) {
	t.Helper()
	content := fmt.Sprintf("[base]\n\n[workspaces.%s]\npaths = [%q]\nexpose_port = false\nenable_docker = %t\nca_bundle = %s\n", workspaceName, workspacePath, enableDocker, value)
	if err := os.WriteFile(filepath.Join(home, ".config", "jailoc", "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write CA integration config: %v", err)
	}
}

func integrationCertificateBundle(t *testing.T, commonName string) ([]byte, tls.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA private key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate TLS server key: %v", err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "host.docker.internal"},
		DNSNames:     []string{"host.docker.internal"},
		NotBefore:    template.NotBefore,
		NotAfter:     template.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, template, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatalf("create TLS server certificate: %v", err)
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return bundle, tls.Certificate{Certificate: [][]byte{leafDER, der}, PrivateKey: leafKey}
}

func verifyForwardedCATLS(ctx context.Context, t *testing.T, certificate tls.Certificate, opencodeContainer, dindContainer string) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen for TLS fixture: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("trusted"))
	}), TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}}}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	url := fmt.Sprintf("https://host.docker.internal:%d", listener.Addr().(*net.TCPAddr).Port)
	for _, container := range []string{opencodeContainer, dindContainer} {
		if container == "" {
			continue
		}
		client := "curl --fail --silent --show-error --max-time 10 " + url
		if container == dindContainer {
			client = "wget -qO- " + url
		}
		response := dockerExec(ctx, t, container, "0", "HTTPS_PROXY= HTTP_PROXY= ALL_PROXY= NO_PROXY=host.docker.internal "+client)
		if response != "trusted" {
			t.Fatalf("%s trusted TLS response = %q, want trusted", container, response)
		}
	}
	response := dockerExec(ctx, t, opencodeContainer, "1000", "NODE_USE_SYSTEM_CA=1 HTTPS_PROXY= HTTP_PROXY= ALL_PROXY= NO_PROXY=host.docker.internal node -e 'require(\"node:https\").get(process.argv[1], r => { if (r.statusCode !== 200) process.exitCode = 1; r.pipe(process.stdout) }).on(\"error\", e => { console.error(e); process.exitCode = 1 })' "+url)
	if response != "trusted" {
		t.Fatalf("Node trusted TLS response = %q, want trusted", response)
	}
}

func uniqueWorkspaceName(t *testing.T, prefix string) string {
	t.Helper()
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate workspace name suffix: %v", err)
	}
	return fmt.Sprintf("%s-%x", prefix, suffix)
}

func writeMinimalConfig(home, workspacePath string) error {
	configPath := filepath.Join(home, ".config", "jailoc", "config.toml")
	content := fmt.Sprintf(`[base]

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

func TestHealthEndpointAccessible(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}

	home := testHome(t)
	workspaceDir := testWorkspaceDir(t)

	content := fmt.Sprintf(`[base]

[workspaces.default]
paths = [%q]
`, workspaceDir)
	if err := os.WriteFile(filepath.Join(home, ".config", "jailoc", "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	upOut, err := runJailoc(ctx, home, "up")
	if err != nil {
		if isImagePullOrAuthFailure(upOut) {
			t.Skip("requires accessible image registry")
		}
		t.Fatalf("jailoc up: %v\noutput: %s", err, upOut)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := "http://localhost:4096/global/health"
	var lastStatus int
	var lastErr error
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if reqErr != nil {
			t.Fatalf("create request: %v", reqErr)
		}
		req.SetBasicAuth("opencode", integrationPassword)
		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			t.Logf("health check error (retrying): %v", doErr)
			time.Sleep(5 * time.Second)
			continue
		}
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode
		if resp.StatusCode == http.StatusOK {
			if dOut, dErr := runJailoc(ctx, home, "down"); dErr != nil {
				t.Fatalf("jailoc down: %v\noutput:\n%s", dErr, dOut)
			}
			return
		}
		t.Logf("health check status %d (retrying)", resp.StatusCode)
		time.Sleep(5 * time.Second)
	}
	if dOut, dErr := runJailoc(ctx, home, "down"); dErr != nil {
		t.Fatalf("jailoc down: %v\noutput:\n%s", dErr, dOut)
	}
	t.Fatalf("health endpoint not accessible after 90s: last status %d, last error: %v", lastStatus, lastErr)
}
