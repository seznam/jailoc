package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down [workspace]",
	Short: "Stop a workspace environment",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDown,
}

func runDown(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(args) > 0 {
		workspaceFlag = args[0]
	} else if !workspaceExplicit {
		cwd, err := os.Getwd()
		if err == nil {
			resolved, _, cwdErr := workspace.ResolveFromCWD(cfg, cwd)
			switch {
			case cwdErr == nil:
				workspaceFlag = resolved.Name
			case errors.Is(cwdErr, workspace.ErrNoMatch):
				// no workspace matches CWD — keep default
			default:
				return fmt.Errorf("resolve workspace from current directory: %w", cwdErr)
			}
		}
	}

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	composePath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")

	if _, err := os.Stat(composePath); err != nil {
		if os.IsNotExist(err) {
			_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
			return nil
		}
		return fmt.Errorf("stat compose file: %w", err)
	}

	client := docker.NewClient(composePath, "", ws.Name)
	ctx := cmd.Context()
	running, err := client.IsRunning(ctx)
	if err != nil || !running {
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}

	_, _ = color.New(color.FgCyan).Printf("Stopping workspace %s...\n", ws.Name)
	if err := client.Down(ctx); err != nil {
		return fmt.Errorf("stop workspace: %w", err)
	}

	_, _ = color.New(color.FgGreen).Printf("Workspace %s stopped\n", ws.Name)
	return nil
}

func init() {
	rootCmd.AddCommand(downCmd)
}
