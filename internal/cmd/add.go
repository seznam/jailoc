package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add [path]",
	Short: "Add current directory (or path) to a workspace",
	Long:  "Add a path to a workspace's path list and restart the environment.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAdd,
}

func runAdd(cmd *cobra.Command, args []string) error {
	targetDir, err := resolveTargetPath(args)
	if err != nil {
		return fmt.Errorf("resolve target directory: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Auto-detect workspace from target path if not explicitly set
	if !workspaceExplicit {
		resolved, _, cwdErr := workspace.ResolveFromCWD(cfg, targetDir)
		switch {
		case cwdErr == nil:
			workspaceFlag = resolved.Name
		case errors.Is(cwdErr, workspace.ErrNoMatch):
			// no workspace matches target — keep default
		default:
			return fmt.Errorf("resolve workspace from target directory: %w", cwdErr)
		}
	}

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace %q: %w", workspaceFlag, err)
	}

	// Conflict detection: explicit -w + path not under workspace → error
	if workspaceExplicit && len(ws.Paths) > 0 && !workspace.MatchesCWD(ws, targetDir) {
		return fmt.Errorf("path %q is not under workspace %q; use a different --workspace or omit the flag for auto-detection", targetDir, workspaceFlag)
	}

	if isDuplicate(ws.Paths, targetDir) {
		_, _ = color.New(color.FgYellow).Printf("Path %q is already in workspace %q\n", targetDir, ws.Name)
		return nil
	}

	ok, err := confirmBroadPath(cmd.Context(), targetDir)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if err := config.AddPath(workspaceFlag, targetDir); err != nil {
		return fmt.Errorf("add path to config: %w", err)
	}

	_, _ = color.New(color.FgGreen).Printf("Added %q to workspace %q\n", targetDir, workspaceFlag)

	return maybeRestartWorkspace(cmd.Context(), ws)
}

func isDuplicate(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

func maybeRestartWorkspace(ctx context.Context, ws *workspace.Resolved) error {
	return restartVMIfRunning(ctx, ws)
}

func init() {
	rootCmd.AddCommand(addCmd)
}
