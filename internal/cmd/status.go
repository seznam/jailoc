package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status [workspace]",
	Short: "Show status of a workspace VM",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStatus,
}

func runStatus(_ *cobra.Command, args []string) error {
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
			default:
				return fmt.Errorf("resolve workspace from current directory: %w", cwdErr)
			}
		}
	}
	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	return statusVM(ws)
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
