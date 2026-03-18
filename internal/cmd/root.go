package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

var workspaceFlag string
var appVersion string
var remoteFlag, execFlag bool

var rootCmd = &cobra.Command{
	Use:   "jailoc",
	Short: "Manage sandboxed OpenCode Docker environments",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		ws, err := workspace.Resolve(cfg, workspaceFlag)
		if err != nil {
			return fmt.Errorf("resolve workspace %q: %w", workspaceFlag, err)
		}

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current directory: %w", err)
		}

		if !workspace.MatchesCWD(ws, cwd) {
			fmt.Printf("Current directory %s is not in workspace %s. Add it? [y/N]: ", cwd, ws.Name)
			reader := bufio.NewReader(os.Stdin)
			answer, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read add-to-workspace prompt response: %w", err)
			}

			trimmed := strings.ToLower(strings.TrimSpace(answer))
			if trimmed != "y" && trimmed != "yes" {
				return nil
			}

			if err := config.AddPath(workspaceFlag, cwd); err != nil {
				return fmt.Errorf("add path %q to workspace %q: %w", cwd, workspaceFlag, err)
			}
			fmt.Printf("Added %s to workspace %s\n", cwd, workspaceFlag)

			cfg, err = config.Load()
			if err != nil {
				return fmt.Errorf("reload config: %w", err)
			}
			ws, err = workspace.Resolve(cfg, workspaceFlag)
			if err != nil {
				return fmt.Errorf("re-resolve workspace %q: %w", workspaceFlag, err)
			}
		}

		composePath := filepath.Join(ComposeCacheDir(ws.Name), "docker-compose.yml")
		client := docker.NewClient(composePath, "", ws.Name)
		running, err := client.IsRunning(ctx)
		if err != nil {
			if !isComposeFileMissing(err) {
				return fmt.Errorf("check workspace %q running status: %w", ws.Name, err)
			}
			running = false
		}

		if !running {
			fmt.Printf("Starting workspace %s...\n", ws.Name)
			if err := runUp(ctx); err != nil {
				return fmt.Errorf("start workspace %q: %w", ws.Name, err)
			}
		}

		mode := resolveFromFlags(cmd, cfg)
		var attachErr error
		switch mode {
		case config.ModeExec:
			attachErr = attachExec(ctx, client)
		default:
			attachErr = attachOnHost(ws)
		}
		if attachErr != nil {
			return fmt.Errorf("attach to workspace %q: %w", ws.Name, attachErr)
		}

		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&workspaceFlag, "workspace", "w", "default", "workspace name")
	rootCmd.PersistentFlags().BoolVar(&remoteFlag, "remote", false, "Use remote mode (host-side opencode attach)")
	rootCmd.PersistentFlags().BoolVar(&execFlag, "exec", false, "Use exec mode (docker exec opencode inside container)")
	rootCmd.MarkFlagsMutuallyExclusive("remote", "exec")
}

// resolveFromFlags returns the effective access mode based on CLI flags and config.
// Priority: --remote/--exec flag → config mode → auto-detect via LookPath.
func resolveFromFlags(cmd *cobra.Command, cfg *config.Config) string {
	if remoteFlag {
		return config.ModeRemote
	}
	if execFlag {
		return config.ModeExec
	}
	return config.ResolveMode(cfg.Mode)
}

// Execute is the entrypoint for the CLI. Version info is passed from main via ldflags.
func Execute(version, commit, date string) error {
	appVersion = version
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
	return rootCmd.Execute()
}
