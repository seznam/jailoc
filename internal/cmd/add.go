package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/password"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	compPath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")
	if _, err := os.Stat(compPath); err != nil {
		return nil
	}

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

	interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: uintptr→int is safe for file descriptors
	resolver := password.DefaultResolver(interactive, cfg.PasswordMode)
	pw, _, err := resolver.Resolve(ws2.Name)
	if err != nil {
		return err
	}

	os.Setenv("OPENCODE_SERVER_PASSWORD", pw) //nolint:gosec,errcheck // only fails if key is empty

	params := compose.ComposeParams{
		WorkspaceName:   ws2.Name,
		Port:            ws2.Port,
		Image:           "jailoc-base:embedded",
		Paths:           ws2.Paths,
		Mounts:          ws2.Mounts,
		AllowedHosts:    ws2.AllowedHosts,
		AllowedNetworks: ws2.AllowedNetworks,
		Env:              ws2.Env,
		SSHAuthSock:      resolveSSHAuthSock(ws2.SSHAuthSock),
		SSHKnownHosts:    resolveSSHKnownHosts(ws2.SSHAuthSock),
		GitConfig:        resolveGitConfig(ws2.GitConfig),
		CPU:              ws2.CPU,
		Memory:           ws2.Memory,
		UseDataVolume:    !compose.MountsContainTarget(ws2.Mounts, "/home/agent/.local/share/opencode"),
		UseCacheVolume:   !compose.MountsContainTarget(ws2.Mounts, "/home/agent/.cache"),
	}

	if err := config.WriteAllowedFiles(ws2.Name, cfg); err != nil {
		return fmt.Errorf("write allowed files for workspace %q: %w", ws2.Name, err)
	}

	if err := writeEntrypoint(filepath.Dir(compPath)); err != nil {
		return err
	}

	if err := writeDindEntrypoint(filepath.Dir(compPath)); err != nil {
		return err
	}

	if err := compose.WriteComposeFile(params, compPath); err != nil {
		return fmt.Errorf("regenerate compose file: %w", err)
	}

	_, _ = color.New(color.FgCyan).Printf("Restarting workspace %s with updated mounts...\n", ws.Name)
	if err := client.Up(ctx); err != nil {
		return fmt.Errorf("restart workspace: %w", err)
	}

	_, _ = color.New(color.FgGreen).Printf("Workspace %q restarted with updated mounts\n", ws.Name)
	return nil
}

func init() {
	rootCmd.AddCommand(addCmd)
}
