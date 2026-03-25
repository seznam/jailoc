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

var downCmd = &cobra.Command{
	Use:   "down [workspace]",
	Short: "Stop a workspace environment",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDown,
}

func runDown(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	composePath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")

	if _, err := os.Stat(composePath); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Workspace %s is not running\n", ws.Name)
			return nil
		}
		return fmt.Errorf("stat compose file: %w", err)
	}

	client := docker.NewClient(composePath, "", ws.Name)
	running, err := client.IsRunning(context.Background())
	if err != nil || !running {
		fmt.Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}

	fmt.Printf("Stopping workspace %s...\n", ws.Name)
	if err := client.Down(context.Background()); err != nil {
		return fmt.Errorf("stop workspace: %w", err)
	}

	fmt.Printf("Workspace %s stopped\n", ws.Name)
	return nil
}

func init() {
	rootCmd.AddCommand(downCmd)
}
