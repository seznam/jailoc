package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

var upCmd = &cobra.Command{
	Use:   "up [workspace]",
	Short: "Start a workspace environment",
	Long:  "Start the Docker Compose environment for a workspace. If already running, no-op.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUp(cmd.Context())
	},
}

func runUp(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace %q: %w", workspaceFlag, err)
	}

	if err := preflightDocker(ctx, ws.Name); err != nil {
		return fmt.Errorf("docker is not running or not accessible: %w", err)
	}

	composePath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")
	runningClient := docker.NewClient(composePath, "", ws.Name)
	running, err := runningClient.IsRunning(ctx)
	if err != nil {
		if !isComposeFileMissing(err) {
			return fmt.Errorf("check workspace %q running status: %w", ws.Name, err)
		}
		running = false
	}
	if running {
		fmt.Printf("Workspace %s is already running on port %d\n", ws.Name, ws.Port)
		return nil
	}

	finalImage, err := ResolveAndLayerImage(ctx, cfg, ws, appVersion)
	if err != nil {
		return err
	}

	cacheDir := ComposeCacheDir(ws.Name)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create compose cache directory %q: %w", cacheDir, err)
	}

	params := compose.ComposeParams{
		WorkspaceName:    ws.Name,
		Port:             ws.Port,
		Image:            finalImage,
		Paths:            ws.Paths,
		AllowedHosts:     ws.AllowedHosts,
		AllowedNetworks:  ws.AllowedNetworks,
		OpenCodePassword: os.Getenv("OPENCODE_SERVER_PASSWORD"),
	}

	if err := compose.WriteComposeFile(params, composePath); err != nil {
		return fmt.Errorf("write compose file for workspace %q: %w", ws.Name, err)
	}

	startClient := docker.NewClient(composePath, "", ws.Name)
	if err := startClient.Up(ctx); err != nil {
		return fmt.Errorf("start workspace %q: %w", ws.Name, err)
	}

	fmt.Printf("Workspace %s started on port %d\n", ws.Name, ws.Port)
	return nil
}

func preflightDocker(ctx context.Context, workspaceName string) error {
	tmpDir, err := os.MkdirTemp("", "jailoc-up-preflight-")
	if err != nil {
		return fmt.Errorf("create preflight temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpComposePath := filepath.Join(tmpDir, "docker-compose.yml")
	content := []byte("services:\n  opencode:\n    image: busybox\n")
	if err := os.WriteFile(tmpComposePath, content, 0o644); err != nil {
		return fmt.Errorf("write preflight compose file: %w", err)
	}

	checkClient := docker.NewClient(tmpComposePath, "", workspaceName)
	if _, err := checkClient.IsRunning(ctx); err != nil {
		return err
	}

	return nil
}

func ComposeCacheDir(workspace string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc", workspace) + string(filepath.Separator)
	}
	return filepath.Join(home, ".cache", "jailoc", workspace) + string(filepath.Separator)
}

func ResolveAndLayerImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, version string) (string, error) {
	base, err := docker.ResolveImage(ctx, cfg, version)
	if err != nil {
		return "", fmt.Errorf("resolve base image: %w", err)
	}
	final, err := docker.ApplyWorkspaceLayer(ctx, base, ws.Name)
	if err != nil {
		return "", fmt.Errorf("apply workspace image layer: %w", err)
	}
	return final, nil
}

func isComposeFileMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "open ") && strings.Contains(msg, "docker-compose.yml")
}

func init() {
	rootCmd.AddCommand(upCmd)
}
