package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
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

	composePath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")

	if _, err := os.Stat(composePath); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Workspace %s is not running\n", ws.Name)
			return nil
		}
		return fmt.Errorf("stat compose file: %w", err)
	}

	client := docker.NewClient(composePath, "", ws.Name)
	ctx := cmd.Context()

	state, exitCode, err := client.ContainerState(ctx)
	if err != nil {
		return fmt.Errorf("check container state: %w", err)
	}

	if state == "" {
		fmt.Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}

	fmt.Printf("Workspace: %s\n", ws.Name)
	fmt.Printf("Port:      %d\n", ws.Port)

	switch state {
	case "running":
		health, err := client.HealthStatus(ctx)
		if err != nil {
			return fmt.Errorf("check health status: %w", err)
		}

		switch health {
		case "unhealthy":
			fmt.Printf("Status:    running (unhealthy)\n")
		case "starting":
			fmt.Printf("Status:    running (starting)\n")
		default:
			fmt.Printf("Status:    running\n")
		}
	case "exited":
		fmt.Printf("Status:    exited (code %d)\n", exitCode)
	default:
		fmt.Printf("Status:    %s\n", state)
	}

	return nil
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
