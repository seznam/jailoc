package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/password"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var statusCmd = &cobra.Command{
	Use:   "status [workspace]",
	Short: "Show status of workspace environments",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
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

	state, exitCode, err := client.ContainerState(ctx)
	if err != nil {
		return fmt.Errorf("check container state: %w", err)
	}

	if state == "" {
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}

	_, _ = color.New(color.FgCyan).Printf("Workspace: %s\n", ws.Name)
	_, _ = color.New(color.FgCyan).Printf("Port:      %d\n", ws.Port)

	// Peek at password source without generating or persisting (status is read-only).
	interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: uintptr→int is safe for file descriptors
	pwResolver := password.DefaultResolver(interactive, cfg.PasswordMode)
	pwSource, pwErr := pwResolver.Peek(ws.Name)

	_, _ = color.New(color.FgCyan).Printf("Password:  ")
	if pwErr != nil {
		_, _ = color.New(color.FgYellow).Printf("unknown\n")
	} else {
		switch pwSource {
		case password.SourceEnv:
			_, _ = color.New(color.FgYellow).Printf("%s\n", pwSource)
		default:
			_, _ = color.New(color.FgGreen).Printf("%s\n", pwSource)
		}
	}

	switch state {
	case "running":
		health, err := client.HealthStatus(ctx)
		if err != nil {
			return fmt.Errorf("check health status: %w", err)
		}

		switch health {
		case "unhealthy":
			_, _ = color.New(color.FgCyan).Printf("Status:    ")
			_, _ = color.New(color.FgRed).Printf("running (unhealthy)\n")
		case "starting":
			_, _ = color.New(color.FgCyan).Printf("Status:    ")
			_, _ = color.New(color.FgYellow).Printf("running (starting)\n")
		default:
			_, _ = color.New(color.FgCyan).Printf("Status:    ")
			_, _ = color.New(color.FgGreen).Printf("running\n")
		}
	case "exited":
		_, _ = color.New(color.FgCyan).Printf("Status:    ")
		_, _ = color.New(color.FgRed).Printf("exited (code %d)\n", exitCode)
	default:
		_, _ = color.New(color.FgCyan).Printf("Status:    ")
		_, _ = color.New(color.FgYellow).Printf("%s\n", state)
	}

	return nil
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
