package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

// allFlag is --all of up, down, restart, reload and status.
var allFlag bool

func addAllFlag(cmd *cobra.Command, usage string) {
	cmd.Flags().BoolVarP(&allFlag, "all", "A", false, usage)
}

// targetWorkspaces returns the workspaces a command acts on: every
// configured workspace with --all, else the one named by args, --workspace
// or the current directory.
func targetWorkspaces(cfg *config.Config, args []string) ([]*workspace.Resolved, error) {
	if allFlag {
		if len(args) > 0 || workspaceExplicit {
			return nil, errors.New("--all cannot be combined with a workspace name")
		}
		names := slices.Sorted(maps.Keys(cfg.Workspaces))
		resolved := make([]*workspace.Resolved, 0, len(names))
		for _, name := range names {
			ws, err := workspace.Resolve(cfg, name)
			if err != nil {
				return nil, fmt.Errorf("resolve workspace %q: %w", name, err)
			}
			resolved = append(resolved, ws)
		}
		return resolved, nil
	}

	name := workspaceFlag
	if len(args) > 0 {
		name = args[0]
	} else if !workspaceExplicit {
		if cwd, err := os.Getwd(); err == nil {
			resolved, _, cwdErr := workspace.ResolveFromCWD(cfg, cwd)
			switch {
			case cwdErr == nil:
				name = resolved.Name
			case errors.Is(cwdErr, workspace.ErrNoMatch):
				// no workspace matches CWD — keep default
			default:
				return nil, fmt.Errorf("resolve workspace from current directory: %w", cwdErr)
			}
		}
	}
	ws, err := workspace.Resolve(cfg, name)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %q: %w", name, err)
	}
	return []*workspace.Resolved{ws}, nil
}

// forEachWorkspace runs fn for each workspace in order. A failure does not
// stop the remaining workspaces; the failures are returned joined. It stops
// when ctx is cancelled.
func forEachWorkspace(ctx context.Context, wss []*workspace.Resolved, fn func(*workspace.Resolved) error) error {
	if len(wss) == 1 {
		return fn(wss[0])
	}
	var errs []error
	for _, ws := range wss {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := fn(ws); err != nil {
			errs = append(errs, fmt.Errorf("workspace %q: %w", ws.Name, err))
		}
	}
	return errors.Join(errs...)
}
