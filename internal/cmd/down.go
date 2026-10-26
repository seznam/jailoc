package cmd

import (
	"context"
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

var downPurge bool

var downCmd = &cobra.Command{
	Use:   "down [workspace]",
	Short: "Stop a workspace environment",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDownCtx(cmd.Context(), args)
	},
}

func runDownCtx(ctx context.Context, args []string) error {
	return runDownCtxForce(ctx, args, false)
}

func runDownCtxForce(ctx context.Context, args []string, force bool) error {
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

	if ws.Backend == config.BackendVM {
		return downVM(ctx, ws, composePath, downPurge)
	}

	if _, err := os.Stat(composePath); err != nil {
		if os.IsNotExist(err) {
			_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
			return nil
		}
		return fmt.Errorf("stat compose file: %w", err)
	}

	client := docker.NewClient(composePath, "", ws.Name)

	if !force {
		running, err := client.IsRunning(ctx)
		if err != nil || !running {
			_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
			return nil
		}
	}

	_, _ = color.New(color.FgCyan).Printf("Stopping workspace %s...\n", ws.Name)
	if err := client.Down(ctx); err != nil {
		return fmt.Errorf("stop workspace: %w", err)
	}

	_, _ = color.New(color.FgGreen).Printf("Workspace %s stopped\n", ws.Name)
	return nil
}

// downVM stops the workspace containers inside the VM, then the VM and its
// network broker. With purge it also deletes the VM state and data disk.
func downVM(ctx context.Context, ws *workspace.Resolved, composePath string, purge bool) error {
	connected, err := connectBackend(ws)
	if err != nil {
		return fmt.Errorf("connect to workspace %q vm: %w", ws.Name, err)
	}
	if !connected {
		if purge {
			if err := stopVM(ws.Name, true); err != nil {
				return err
			}
			_, _ = color.New(color.FgGreen).Printf("Workspace %s VM state deleted\n", ws.Name)
			return nil
		}
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}

	_, _ = color.New(color.FgCyan).Printf("Stopping workspace %s...\n", ws.Name)
	if _, err := os.Stat(composePath); err == nil {
		if err := docker.NewClient(composePath, "", ws.Name).Down(ctx); err != nil {
			return fmt.Errorf("stop workspace: %w", err)
		}
	}
	if err := stopVM(ws.Name, purge); err != nil {
		return err
	}
	_, _ = color.New(color.FgGreen).Printf("Workspace %s stopped\n", ws.Name)
	return nil
}

func init() {
	downCmd.Flags().BoolVar(&downPurge, "purge", false, `also delete the VM and its Docker data disk (backend "vm" only)`)
	rootCmd.AddCommand(downCmd)
}
