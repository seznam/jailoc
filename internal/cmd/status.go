package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

var statusCmd = &cobra.Command{
	Use:   "status [workspace]",
	Short: "Show status of workspace environments",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	composePath := composeCacheDir(ws.Name) + "docker-compose.yml"

	if _, err := os.Stat(composePath); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Workspace %s is not running\n", ws.Name)
			return nil
		}
		return fmt.Errorf("stat compose file: %w", err)
	}

	client := docker.NewClient(composePath, "", ws.Name)
	running, err := client.IsRunning(context.Background())
	if err != nil {
		return fmt.Errorf("check running status: %w", err)
	}

	if !running {
		fmt.Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}

	fmt.Printf("Workspace: %s\n", ws.Name)
	fmt.Printf("Port:      %d\n", ws.Port)
	fmt.Printf("Status:    running\n")
	return nil
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
