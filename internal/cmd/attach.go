package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/password"
	"github.com/seznam/jailoc/internal/workspace"
)

func attachHostArgs(serverURL, password, dir string) []string {
	args := []string{"attach", serverURL}
	if password != "" {
		args = append(args, "--password", password)
	}
	if dir != "" {
		args = append(args, "--dir", dir)
	}
	return args
}

func attachExecArgs(serverURL, dir string) []string {
	args := []string{"opencode", "attach", serverURL}
	if dir != "" {
		args = append(args, "--dir", dir)
	}
	return args
}

// hostTUIConfigEnv returns OPENCODE_TUI_CONFIG env var entries for host attach
// when jailoc's generated tui.json should be used as a fallback. It does not
// override an explicit env var, and it does not point OpenCode at a missing
// generated config file.
func hostTUIConfigEnv(configPath string) []string {
	if os.Getenv("OPENCODE_TUI_CONFIG") != "" {
		return nil
	}
	if _, err := os.Stat(configPath); err != nil {
		return nil
	}

	ocDir, err := openCodeConfigDir()
	if err != nil {
		return nil
	}
	userTUI := filepath.Join(ocDir, "tui.json")
	if _, err := os.Stat(userTUI); err == nil {
		return nil // user has their own tui.json — don't override
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil // unreadable or other filesystem error — don't override
	}
	return []string{"OPENCODE_TUI_CONFIG=" + configPath}
}

// openCodeConfigDir returns the directory OpenCode uses for its config:
// $XDG_CONFIG_HOME/opencode if set, otherwise $HOME/.config/opencode.
// This matches OpenCode's own resolution regardless of platform (os.UserConfigDir
// returns ~/Library/Application Support on macOS, which OpenCode does not use).
func openCodeConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func execTUIConfigEnv(configPath string) []string {
	return []string{"OPENCODE_TUI_CONFIG=" + configPath}
}

func envWithOverrides(base []string, overrides ...string) []string {
	if len(overrides) == 0 {
		return append([]string{}, base...)
	}

	overrideKeys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		if key, _, ok := strings.Cut(entry, "="); ok && key != "" {
			overrideKeys[key] = struct{}{}
		}
	}

	filtered := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		if key, _, ok := strings.Cut(entry, "="); ok {
			if _, exists := overrideKeys[key]; exists {
				continue
			}
		}
		filtered = append(filtered, entry)
	}

	return append(filtered, overrides...)
}

func attachOnHost(ctx context.Context, ws *workspace.Resolved, dir string, passwordMode string) error {
	binary, err := config.ResolveBinary()
	if err != nil {
		return fmt.Errorf("resolve opencode binary: %w", err)
	}

	serverArg := fmt.Sprintf("http://localhost:%d", ws.Port)
	interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: uintptr→int is safe for file descriptors
	resolver := password.DefaultResolver(interactive, passwordMode)
	pw, _, err := resolver.Resolve(ws.Name)
	if err != nil {
		return err
	}
	args := attachHostArgs(serverArg, pw, dir)
	cmd := exec.Command(binary, args...) //nolint:gosec // binary name is from ResolveBinary, args are controlled
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	tuiPath := filepath.Join(jailocCacheDir(), "tui.json")
	cmd.Env = envWithOverrides(os.Environ(),
		"JAILOC=1",
		"JAILOC_WORKSPACE="+ws.Name,
	)
	if env := hostTUIConfigEnv(tuiPath); len(env) > 0 {
		cmd.Env = envWithOverrides(cmd.Env, env...)
	}

	err = runCommandWithContext(ctx, cmd, func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}, attachWaitDelay)
	return attachResult(ctx, err)
}

func attachExec(ctx context.Context, client *docker.Client, dir string) error {
	fd := int(os.Stdin.Fd()) //nolint:gosec // Fd() fits in int on all supported platforms
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set raw terminal: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			// Terminal resize is forwarded by the exec stream automatically.
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
	}()

	serverURL := fmt.Sprintf("http://localhost:%d", workspace.BasePort)
	err = client.Exec(ctx, attachExecArgs(serverURL, dir), execTUIConfigEnv("/etc/jailoc-tui.json"), os.Stdin, os.Stdout, os.Stderr)
	return attachResult(ctx, err)
}

const (
	attachPollInterval = 500 * time.Millisecond
	attachWaitDelay    = 2 * time.Second
)

var errUnhealthy = errors.New("opencode process unhealthy inside container")

func startAttachWatch(parent context.Context, client *docker.Client, workspaceName string) (context.Context, func(), error) {
	containerID, err := client.CurrentContainerID(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve opencode container: %w", err)
	}
	if containerID == "" {
		return nil, nil, fmt.Errorf("workspace %q is not running; run 'jailoc up' first", workspaceName)
	}

	attachCtx, cancel := context.WithCancelCause(parent)
	go monitorAttach(attachCtx, cancel, client.CurrentContainerID, client.HealthStatus, containerID, attachPollInterval)

	return attachCtx, func() { cancel(nil) }, nil
}

func attachResult(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}

	return err
}

func monitorAttach(ctx context.Context, cancel context.CancelCauseFunc, currentContainerID func(context.Context) (string, error), healthStatus func(context.Context) (string, error), expectedContainerID string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			containerID, err := currentContainerID(ctx)
			if err != nil {
				cancel(fmt.Errorf("monitor opencode container: %w", err))
				return
			}
			if containerID == "" {
				cancel(fmt.Errorf("opencode container stopped during attach"))
				return
			}
			if containerID != expectedContainerID {
				cancel(fmt.Errorf("opencode container restarted during attach"))
				return
			}
			health, err := healthStatus(ctx)
			if err != nil {
				continue // transient health check failure — ignore
			}
			if health == "unhealthy" {
				cancel(errUnhealthy)
				return
			}
		}
	}
}

func runCommandWithContext(ctx context.Context, cmd *exec.Cmd, terminate func() error, waitDelay time.Duration) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command %q: %w", cmd.Path, err)
	}

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- cmd.Wait()
	}()

	select {
	case err := <-resultCh:
		return err
	case <-ctx.Done():
		if terminate != nil {
			if err := terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return fmt.Errorf("cancel command %q: %w", cmd.Path, err)
			}
		}

		if waitDelay <= 0 {
			return <-resultCh
		}

		select {
		case err := <-resultCh:
			return err
		case <-time.After(waitDelay):
			if cmd.Process != nil {
				if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					return fmt.Errorf("kill command %q: %w", cmd.Path, err)
				}
			}
			return <-resultCh
		}
	}
}
