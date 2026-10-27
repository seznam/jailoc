package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

// vmSessionOptions select the agent session a VM session attaches to.
type vmSessionOptions struct {
	dir      string // working directory of the agent
	session  string // OpenCode session ID to continue (new agent session)
	cont     bool   // continue the last OpenCode session (new agent session)
	newAgent bool   // always start a new agent session
	attach   string // attach to this agent session ID
}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List agent sessions running in a workspace VM",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ws, err := resolveVMWorkspace()
		if err != nil {
			return err
		}
		return listSessionsVM(cmd.Context(), ws)
	},
}

var sessionsStopCmd = &cobra.Command{
	Use:   "stop ID",
	Short: "Stop an agent session in a workspace VM",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := resolveVMWorkspace()
		if err != nil {
			return err
		}
		return stopSessionVM(cmd.Context(), ws, args[0])
	},
}

// resolveVMWorkspace resolves --workspace or the workspace of the current
// directory.
func resolveVMWorkspace() (*workspace.Resolved, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	name := workspaceFlag
	if !workspaceExplicit {
		if cwd, err := os.Getwd(); err == nil {
			resolved, _, cwdErr := workspace.ResolveFromCWD(cfg, cwd)
			switch {
			case cwdErr == nil:
				name = resolved.Name
			case errors.Is(cwdErr, workspace.ErrNoMatch):
			default:
				return nil, fmt.Errorf("resolve workspace from current directory: %w", cwdErr)
			}
		}
	}
	ws, err := workspace.Resolve(cfg, name)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	return ws, nil
}

func init() {
	sessionsCmd.AddCommand(sessionsStopCmd)
	rootCmd.AddCommand(sessionsCmd)
}
