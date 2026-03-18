package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

var inDocker bool

var attachCmd = &cobra.Command{
	Use:   "attach [workspace]",
	Short: "Attach to a running workspace (host opencode attach by default)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAttach(cmd, args)
	},
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

	if inDocker {
		return attachInDocker(ctx, client)
	}

	return attachOnHost(ws)
}

func attachOnHost(ws *workspace.Resolved) error {
	serverArg := fmt.Sprintf("localhost:%d", ws.Port)
	args := []string{"attach", "--server", serverArg}

	if password := os.Getenv("OPENCODE_SERVER_PASSWORD"); password != "" {
		args = append(args, "--password", password)
	}

	cmd := exec.Command("opencode", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func attachInDocker(ctx context.Context, client *docker.Client) error {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set raw terminal: %w", err)
	}
	defer term.Restore(fd, oldState)

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

	return client.Exec(ctx, []string{"/bin/bash"}, os.Stdin, os.Stdout, os.Stderr)
}

func init() {
	attachCmd.Flags().BoolVar(&inDocker, "in-docker", false, "Run attach inside the container via exec instead of host opencode attach")
	rootCmd.AddCommand(attachCmd)
}
