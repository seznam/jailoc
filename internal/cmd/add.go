package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
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

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace %q: %w", workspaceFlag, err)
	}

	if isDuplicate(ws.Paths, targetDir) {
		fmt.Printf("Path %q is already in workspace %q\n", targetDir, ws.Name)
		return nil
	}

	ok, err := confirmBroadPath(targetDir)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if err := config.AddPath(workspaceFlag, targetDir); err != nil {
		return fmt.Errorf("add path to config: %w", err)
	}

	fmt.Printf("Added %q to workspace %q\n", targetDir, workspaceFlag)

	return maybeRestartWorkspace(ws)
}

func isDuplicate(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

func maybeRestartWorkspace(ws *workspace.Resolved) error {
	compPath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")
	if _, err := os.Stat(compPath); err != nil {
		return nil
	}

	ctx := context.Background()
	client := docker.NewClient(compPath, "", ws.Name)

	running, err := client.IsRunning(ctx)
	if err != nil {
		return nil
	}

	if !running {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload config for restart: %w", err)
	}

	ws2, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("re-resolve workspace for restart: %w", err)
	}

	password := os.Getenv("OPENCODE_SERVER_PASSWORD")
	params := compose.ComposeParams{
		WorkspaceName:    ws2.Name,
		Port:             ws2.Port,
		Image:            "jailoc-base:embedded",
		Paths:            ws2.Paths,
		AllowedHosts:     ws2.AllowedHosts,
		AllowedNetworks:  ws2.AllowedNetworks,
		OpenCodePassword: password,
		Env:              ws2.Env,
	}

	if err := config.WriteAllowedFiles(ws2.Name, cfg); err != nil {
		return fmt.Errorf("write allowed files for workspace %q: %w", ws2.Name, err)
	}

	if err := compose.WriteComposeFile(params, compPath); err != nil {
		return fmt.Errorf("regenerate compose file: %w", err)
	}

	fmt.Printf("Restarting workspace %s with updated mounts...\n", ws.Name)
	if err := client.Up(ctx); err != nil {
		return fmt.Errorf("restart workspace: %w", err)
	}

	fmt.Printf("Workspace %q restarted with updated mounts\n", ws.Name)
	return nil
}

func init() {
	rootCmd.AddCommand(addCmd)
}
