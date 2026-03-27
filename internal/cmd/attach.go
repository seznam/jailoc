package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

var attachCmd = &cobra.Command{
	Use:   "attach [workspace]",
	Short: "Attach to a running workspace (host opencode attach by default)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAttach,
}

func runAttach(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	composePath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")
	client := docker.NewClient(composePath, "", ws.Name)

	ctx := cmd.Context()
	running, err := client.IsRunning(ctx)
	if err != nil || !running {
		return fmt.Errorf("workspace %q is not running; run 'jailoc up' first", ws.Name)
	}

	attachCtx, stop, err := startAttachWatch(ctx, client, ws.Name)
	if err != nil {
		return err
	}
	defer stop()

	mode := resolveFromFlags(cmd, cfg)
	var attachErr error
	switch mode {
	case config.ModeExec:
		_, _ = color.New(color.FgCyan).Printf("Attaching to workspace %s (exec mode)...\n", ws.Name)
		attachErr = attachExec(attachCtx, client)
	default:
		_, _ = color.New(color.FgCyan).Printf("Attaching to workspace %s (remote mode)...\n", ws.Name)
		attachErr = attachOnHost(attachCtx, ws)
	}

	if errors.Is(attachErr, errUnhealthy) {
		_, _ = color.New(color.FgCyan).Fprintf(os.Stderr, "\n--- last %d log lines ---\n", tailLogLines)
		logCtx, logCancel := context.WithTimeout(ctx, tailLogTimeout)
		defer logCancel()
		if logErr := client.TailLogs(logCtx, tailLogLines, os.Stderr); logErr != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "failed to retrieve logs: %v\n", logErr)
		}
		_, _ = color.New(color.FgRed).Fprintf(os.Stderr, "--- container reported unhealthy; see logs above ---\n")
	}

	return attachErr
}

func attachOnHost(ctx context.Context, ws *workspace.Resolved) error {
	binary, err := config.ResolveBinary()
	if err != nil {
		return fmt.Errorf("resolve opencode binary: %w", err)
	}

	serverArg := fmt.Sprintf("http://localhost:%d", ws.Port)
	args := []string{"attach", serverArg}

	if password := os.Getenv("OPENCODE_SERVER_PASSWORD"); password != "" {
		args = append(args, "--password", password)
	}

	cmd := exec.Command(binary, args...) //nolint:gosec // binary name is from ResolveBinary, args are controlled
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = runCommandWithContext(ctx, cmd, func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}, attachWaitDelay)
	return attachResult(ctx, err)
}

func attachExec(ctx context.Context, client *docker.Client) error {
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
	err = client.Exec(ctx, []string{"opencode", "attach", serverURL}, os.Stdin, os.Stdout, os.Stderr)
	return attachResult(ctx, err)
}

const (
	attachPollInterval = 500 * time.Millisecond
	attachWaitDelay    = 2 * time.Second
	tailLogLines       = 50
	tailLogTimeout     = 10 * time.Second
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

func init() {
	rootCmd.AddCommand(attachCmd)
}
