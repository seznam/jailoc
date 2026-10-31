package cmd

import (
	"fmt"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var reloadCmd = &cobra.Command{
	Use:   "reload [workspace]",
	Short: "Apply configuration changes to a running VM workspace",
	Long: `Apply configuration changes to a running workspace without restarting it
allowed_hosts, allowed_networks, filtered_dns, ports,
ssh_auth_sock, ca_bundle, env and secrets. Running sessions keep running.
Changes that need a new VM are listed; apply them with jailoc restart.
jailoc up on a running VM workspace does the same.
With --all, reloads every running workspace.`,
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
				if running, err := workspaceRunning(ws); err != nil || !running {
					return err
				}
			}
			return reloadVM(ctx, ws)
		})
	},
}

func init() {
	addAllFlag(reloadCmd, "reload all running workspaces")
	rootCmd.AddCommand(reloadCmd)
}
