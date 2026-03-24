package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

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

	composePath := composeCacheDir(ws.Name) + "docker-compose.yml"
	client := docker.NewClient(composePath, "", ws.Name)

	ctx := cmd.Context()
	running, err := client.IsRunning(ctx)
	if err != nil || !running {
		return fmt.Errorf("workspace %q is not running; run 'jailoc up' first", ws.Name)
	}

	mode := resolveFromFlags(cmd, cfg)
	switch mode {
	case config.ModeExec:
		return attachExec(ctx, client)
	default:
		return attachOnHost(ctx, client, ws)
	}
}

func attachOnHost(ctx context.Context, client *docker.Client, ws *workspace.Resolved) error {
	serverArg := fmt.Sprintf("http://localhost:%d", ws.Port)
	args := []string{"attach", serverArg}

	if password := os.Getenv("OPENCODE_SERVER_PASSWORD"); password != "" {
		args = append(args, "--password", password)
	}

	cmd := exec.CommandContext(ctx, "opencode", args...) //nolint:gosec // binary name is hardcoded, args are controlled
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start opencode attach: %w", err)
	}

	// Watchdog: poll workspace health every 3s, kill process when it stops.
	died := make(chan struct{})
	go watchWorkspace(ctx, client, died)

	// Wait for either process exit or watchdog signal.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case err := <-waitErr:
		return err
	case <-died:
		_ = cmd.Process.Kill()
		<-waitErr // reap the process
		return fmt.Errorf("workspace stopped — detached")
	}
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

	// Cancel context when workspace stops so the exec stream disconnects.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	died := make(chan struct{})
	go watchWorkspace(ctx, client, died)
	go func() {
		<-died
		cancel()
	}()

	serverURL := fmt.Sprintf("http://localhost:%d", workspace.BasePort)
	return client.Exec(ctx, []string{"opencode", "attach", serverURL}, os.Stdin, os.Stdout, os.Stderr)
}

const watchdogInterval = 3 * time.Second

func watchWorkspace(ctx context.Context, client *docker.Client, died chan<- struct{}) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			running, err := client.IsRunning(ctx)
			if err != nil || !running {
				close(died)
				return
			}
		}
	}
}

func init() {
	rootCmd.AddCommand(attachCmd)
}
