package cmd

import (
	"fmt"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart [workspace]",
	Short: "Restart a workspace environment",
	Long: "Stop and restart a workspace VM with the current configuration. If the workspace is not running, start it.\n" +
		"With --all, restarts every running workspace.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		wss, err := targetWorkspaces(cfg, args)
		if err != nil {
			return err
		}
		return forEachWorkspace(ctx, wss, func(ws *workspace.Resolved) error {
			if allFlag {
				return restartVMIfRunning(ctx, ws)
			}
			if err := downVM(ws, false); err != nil {
				return err
			}
			return runUpVM(ctx, cfg, ws)
		})
	},
}

func init() {
	addAllFlag(restartCmd, "restart all running workspaces")
	rootCmd.AddCommand(restartCmd)
}
