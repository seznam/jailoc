package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var workspaceFlag string
var appVersion string
var remoteFlag, execFlag bool

var rootCmd = &cobra.Command{
	Use:          "jailoc [path]",
	Short:        "Manage sandboxed OpenCode Docker environments",
	SilenceUsage: true,
	Args:         cobra.MaximumNArgs(1),
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if noColor, _ := cmd.Flags().GetBool("no-color"); noColor {
			color.NoColor = true
		}
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		targetPath, err := resolveTargetPath(args)
		if err != nil {
			return fmt.Errorf("resolve target path: %w", err)
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		ws, err := workspace.Resolve(cfg, workspaceFlag)
		if err != nil {
			return fmt.Errorf("resolve workspace %q: %w", workspaceFlag, err)
		}

		if !workspace.MatchesCWD(ws, targetPath) {
			_, _ = color.New(color.FgYellow, color.Bold).Printf("Path %s is not in workspace %s. Add it? [y/N]: ", targetPath, ws.Name)
			answer, err := readLineCtx(ctx)
			if err != nil {
				return fmt.Errorf("read add-to-workspace prompt response: %w", err)
			}

			trimmed := strings.ToLower(strings.TrimSpace(answer))
			if trimmed != "y" && trimmed != "yes" {
				return nil
			}

			ok, err := confirmBroadPath(ctx, targetPath)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}

			if err := config.AddPath(workspaceFlag, targetPath); err != nil {
				return fmt.Errorf("add path %q to workspace %q: %w", targetPath, workspaceFlag, err)
			}
			_, _ = color.New(color.FgGreen).Printf("Added %s to workspace %s\n", targetPath, workspaceFlag)

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
			_, _ = color.New(color.FgCyan).Printf("Starting workspace %s...\n", ws.Name)
			if err := runUp(ctx); err != nil {
				return fmt.Errorf("start workspace %q: %w", ws.Name, err)
			}
			_, _ = color.New(color.FgCyan).Printf("Waiting for OpenCode to be ready on port %d...\n", ws.Port)
			if err := waitForReady(ctx, ws.Port, client); err != nil {
				return fmt.Errorf("wait for workspace %q readiness: %w", ws.Name, err)
			}
		}

		mode := resolveFromFlags(cmd, cfg)
		attachCtx, stop, err := startAttachWatch(ctx, client, ws.Name)
		if err != nil {
			return err
		}
		defer stop()

		var attachErr error
		switch mode {
		case config.ModeExec:
			_, _ = color.New(color.FgCyan).Printf("Attaching to workspace %s (exec mode)...\n", ws.Name)
			attachErr = attachExec(attachCtx, client)
		default:
			_, _ = color.New(color.FgCyan).Printf("Attaching to workspace %s (remote mode)...\n", ws.Name)
			attachErr = attachOnHost(attachCtx, ws)
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
	rootCmd.PersistentFlags().Bool("no-color", false, "disable colored output")
	rootCmd.MarkFlagsMutuallyExclusive("remote", "exec")
}

// resolveTargetPath returns the absolute path from a positional argument.
// Falls back to the current working directory when no argument is given.
func resolveTargetPath(args []string) (string, error) {
	if len(args) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
		return cwd, nil
	}

	expanded, err := config.ExpandPath(args[0])
	if err != nil {
		return "", fmt.Errorf("expand path %q: %w", args[0], err)
	}

	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path %q: %w", expanded, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path %q does not exist", abs)
		}
		return "", fmt.Errorf("stat path %q: %w", abs, err)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", abs)
	}

	return abs, nil
}

func isBroadPath(path string) bool {
	if path == "/" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return path == home
}

func confirmBroadPath(ctx context.Context, path string) (bool, error) {
	if !isBroadPath(path) {
		return true, nil
	}

	_, _ = color.New(color.FgYellow).Printf("WARNING: %q is a very broad path — this will mount your entire directory tree into the container.\n", path)
	_, _ = color.New(color.FgYellow, color.Bold).Print("Are you sure? [y/N]: ")

	answer, err := readLineCtx(ctx)
	if err != nil {
		return false, fmt.Errorf("read broad-path confirmation: %w", err)
	}

	trimmed := strings.ToLower(strings.TrimSpace(answer))
	return trimmed == "y" || trimmed == "yes", nil
}

// readLineCtx reads a line from stdin, returning ctx.Err() on cancellation.
// Uses SetReadDeadline to interrupt the blocked read — no goroutine leak.
// Pattern from encoredev/encore (cli/internal/login/deviceauth.go).
func readLineCtx(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case <-ctx.Done():
		if err := os.Stdin.SetReadDeadline(time.Now()); err == nil {
			<-ch
			_ = os.Stdin.SetReadDeadline(time.Time{})
		}
		return "", ctx.Err()
	case r := <-ch:
		return r.line, r.err
	}
}

// resolveFromFlags returns the effective access mode based on CLI flags and config.
// Priority: --remote/--exec flag → config mode → auto-detect via LookPath.
func resolveFromFlags(_ *cobra.Command, cfg *config.Config) string {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rootCmd.SetContext(ctx)

	return rootCmd.Execute()
}

const (
	readyPollInterval = 200 * time.Millisecond
	readyPollTimeout  = 60 * time.Second
)

func waitForReady(ctx context.Context, port int, dc *docker.Client) error {
	url := fmt.Sprintf("http://localhost:%d", port)

	ctx, cancel := context.WithTimeout(ctx, readyPollTimeout)
	defer cancel()

	httpClient := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()

	for {
		state, exitCode, err := dc.ContainerState(ctx)
		if err == nil && state == "exited" {
			return fmt.Errorf("container exited with code %d before becoming ready", exitCode)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for %s", readyPollTimeout, url)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return fmt.Errorf("create readiness request: %w", err)
			}
			resp, err := httpClient.Do(req) //nolint:gosec // URL is localhost with controlled port
			if err != nil {
				continue
			}
			_ = resp.Body.Close()
			return nil
		}
	}
}
