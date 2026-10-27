package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"os"
)

var downCmd = &cobra.Command{
	Use:   "down [workspace]",
	Short: "Stop a workspace environment",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDownCtx(cmd.Context(), args)
	},
}

func runDownCtx(_ context.Context, args []string) error {
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

	return downVM(ws, downPurge)
}

var downPurge bool

func init() {
	downCmd.Flags().BoolVar(&downPurge, "purge", false, "also delete VM state, persistent home directories and the isolated builder")
	rootCmd.AddCommand(downCmd)
}
