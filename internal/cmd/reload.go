package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var reloadCmd = &cobra.Command{
	Use:   "reload [workspace]",
	Short: "Apply configuration changes to a running VM workspace",
	Long: `Apply configuration changes to a running workspace without restarting it
(backend "vm" only): allowed_hosts, allowed_networks, filtered_dns, ports,
ssh_auth_sock, ca_bundle, env and secrets. Running sessions keep running.
Changes that need a new VM are listed; apply them with jailoc restart.
jailoc up on a running VM workspace does the same.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if len(args) > 0 {
			workspaceFlag = args[0]
		} else if !workspaceExplicit {
			if cwd, err := os.Getwd(); err == nil {
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
		if ws.Backend != config.BackendVM {
			return fmt.Errorf("workspace %q: reload is only supported with backend %q; use jailoc restart", ws.Name, config.BackendVM)
		}
		return reloadVM(cmd.Context(), ws)
	},
}

func init() {
	rootCmd.AddCommand(reloadCmd)
}
