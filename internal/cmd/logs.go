package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

var followLogs bool

var logsCmd = &cobra.Command{
	Use:   "logs [workspace]",
	Short: "Show logs for a workspace environment",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runLogs,
}

func runLogs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Determine workspace name
	name := workspaceFlag
	if len(args) > 0 {
		name = args[0]
	}

	ws, err := workspace.Resolve(cfg, name)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	composePath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")

	// Check if compose file exists (workspace running)
	if _, err := os.Stat(composePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("workspace %q is not running; start it with 'jailoc up'", ws.Name)
		}
		return fmt.Errorf("check compose file %q: %w", composePath, err)
	}

	client := docker.NewClient(composePath, "", ws.Name)

	// Check if workspace is actually running
	running, err := client.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("check if workspace is running: %w", err)
	}

	if !running {
		return fmt.Errorf("workspace %q is not running; start it with 'jailoc up'", ws.Name)
	}

	// Stream logs
	if err := client.Logs(ctx, followLogs, os.Stdout); err != nil {
		return fmt.Errorf("stream logs: %w", err)
	}

	return nil
}

func init() {
	logsCmd.Flags().BoolVarP(&followLogs, "follow", "f", false, "Follow log output")
	rootCmd.AddCommand(logsCmd)
}
