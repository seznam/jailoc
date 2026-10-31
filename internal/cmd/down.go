package cmd

import (
	"context"
	"fmt"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down [workspace]",
	Short: "Stop a workspace environment",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDownCtx(cmd.Context(), args)
	},
}

func runDownCtx(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	wss, err := targetWorkspaces(cfg, args)
	if err != nil {
		return err
	}
	return forEachWorkspace(ctx, wss, func(ws *workspace.Resolved) error {
		return downVM(ws, downPurge)
	})
}

var downPurge bool

func init() {
	downCmd.Flags().BoolVar(&downPurge, "purge", false, "also delete VM state, persistent home directories and the isolated builder")
	addAllFlag(downCmd, "stop all configured workspaces")
	rootCmd.AddCommand(downCmd)
}
