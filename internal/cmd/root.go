package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/logging"
	"github.com/seznam/jailoc/internal/update"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var workspaceFlag string
var workspaceExplicit bool
var appVersion string
var sessionFlag string
var continueFlag bool
var newSessionFlag bool
var attachSessionFlag string

var rootCmd = &cobra.Command{
	Use:          "jailoc [path]",
	Short:        "Manage sandboxed OpenCode virtual machines",
	SilenceUsage: true,
	Args:         cobra.MaximumNArgs(1),
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		_ = logging.Init()
		slog.Info("jailoc started", "version", appVersion)
		if noColor, _ := cmd.Flags().GetBool("no-color"); noColor {
			color.NoColor = true
		}
		workspaceExplicit = cmd.Flags().Changed("workspace")
		return nil
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

		if !workspaceExplicit {
			resolved, _, cwdErr := workspace.ResolveFromCWD(cfg, targetPath)
			switch {
			case cwdErr == nil:
				workspaceFlag = resolved.Name
			case errors.Is(cwdErr, workspace.ErrNoMatch):
				// no workspace matches target — keep default
			default:
				return fmt.Errorf("resolve workspace from target path: %w", cwdErr)
			}
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

		return runSessionVM(ctx, cfg, ws, vmSessionOptions{
			dir: targetPath, session: sessionFlag, cont: continueFlag,
			newAgent: newSessionFlag, attach: attachSessionFlag,
		})
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&workspaceFlag, "workspace", "w", "default", "workspace name")
	rootCmd.PersistentFlags().Bool("no-color", false, "disable colored output")
	rootCmd.Flags().StringVarP(&sessionFlag, "session", "s", "", "session ID to continue")
	rootCmd.Flags().BoolVarP(&continueFlag, "continue", "c", false, "continue the last session")
	rootCmd.Flags().BoolVar(&newSessionFlag, "new", false, "start a new agent session instead of attaching to the running one")
	rootCmd.Flags().StringVar(&attachSessionFlag, "attach", "", "attach to the agent session with this ID (see jailoc sessions)")
	rootCmd.MarkFlagsMutuallyExclusive("session", "continue")
	rootCmd.MarkFlagsMutuallyExclusive("new", "attach")
	rootCmd.MarkFlagsMutuallyExclusive("attach", "session")
	rootCmd.MarkFlagsMutuallyExclusive("attach", "continue")
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

	_, _ = color.New(color.FgYellow).Printf("WARNING: %q is a very broad path — this will share your entire directory tree with the VM.\n", path)
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

// Execute is the entrypoint for the CLI. Version info is passed from main via ldflags.
func Execute(version, commit, date string) error {
	appVersion = version
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rootCmd.SetContext(ctx)

	// Fire non-blocking update check only when stderr is a terminal —
	// skip the HTTP call entirely in non-interactive environments.
	var updateResult <-chan *update.Result
	if term.IsTerminal(int(os.Stderr.Fd())) { //nolint:gosec // Fd() fits int on all supported 64-bit platforms
		updateResult = update.CheckAsync(ctx, version)
	}
	defer func() {
		if updateResult == nil {
			return
		}
		// Non-blocking read: if result not ready (fast command), skip gracefully.
		select {
		case res := <-updateResult:
			if res != nil {
				fmt.Fprint(os.Stderr, update.FormatNotice(res.Current, res.Latest))
			}
		default:
		}
	}()

	return rootCmd.Execute()
}
