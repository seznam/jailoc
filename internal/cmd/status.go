package cmd

import (
	"fmt"

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

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	wss, err := targetWorkspaces(cfg, args)
	if err != nil {
		return err
	}
	first := true
	return forEachWorkspace(cmd.Context(), wss, func(ws *workspace.Resolved) error {
		if !first {
			fmt.Println()
		}
		first = false
		return statusVM(cmd.Context(), ws)
	})
}

func init() {
	addAllFlag(statusCmd, "show the status of all configured workspaces")
	rootCmd.AddCommand(statusCmd)
}
