package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up [workspace]",
	Short: "Start a workspace environment",
	Long:  "Start a workspace VM. If already running, apply reloadable configuration changes.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUp(cmd.Context(), args)
	},
}

func runUp(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(args) > 0 {
		workspaceFlag = args[0]
	} else if !workspaceExplicit {
		cwd, err := os.Getwd()
		if err == nil {
			resolved, _, cwdErr := workspace.ResolveFromCWD(cfg, cwd)
			switch {
			case cwdErr == nil:
				workspaceFlag = resolved.Name
			case errors.Is(cwdErr, workspace.ErrNoMatch):
				// no workspace matches CWD — keep default
			default:
				return fmt.Errorf("resolve workspace from current directory: %w", cwdErr)
			}
		}
	}

	slog.Debug("resolving workspace", "name", workspaceFlag, "explicit", workspaceExplicit)
	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace %q: %w", workspaceFlag, err)
	}

	return runUpVM(ctx, cfg, ws)
}

// workspaceCacheDir holds the generated files of a workspace that the guest
// reads: the TUI config and plugin.
func workspaceCacheDir(workspace string) string {
	return filepath.Join(jailocCacheDir(), workspace)
}

func jailocCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc")
	}
	return filepath.Join(home, ".cache", "jailoc")
}

type imageStrategy int

const (
	strategyDirectImage imageStrategy = iota
	strategyDefaultsDirect
	strategyDefaultsOverlay // defaults.Image set, ws dockerfile — build overlay using defaults as base
	strategyCascade         // no image/defaults — existing ResolveBaseImage cascade
)

// resolveImageStrategy returns the image resolution strategy and the relevant image string.
// wsImage is the raw workspace image (empty if unset).
// defaultsImage is cfg.Defaults.Image (empty if unset).
// wsDockerfile is the workspace Dockerfile path (empty if unset).
func resolveImageStrategy(wsImage, defaultsImage, wsDockerfile string) (imageStrategy, string) {
	if wsImage != "" {
		return strategyDirectImage, wsImage
	}

	if defaultsImage != "" {
		if wsDockerfile != "" {
			return strategyDefaultsOverlay, defaultsImage
		}
		return strategyDefaultsDirect, defaultsImage
	}

	return strategyCascade, ""
}

// ResolveAndLayerImage resolves and builds the workspace image. It returns
// the final image and the images it is built from, the final one last.
func ResolveAndLayerImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, version string) (string, []string, error) {
	strategy, strategyImage := resolveImageStrategy(ws.Image, cfg.Defaults.Image, ws.Dockerfile)
	slog.Debug("image strategy resolved", "strategy", strategy, "image", strategyImage)
	switch strategy {
	case strategyDirectImage:
		_, _ = color.New(color.FgCyan).Printf("Using workspace image %s\n", strategyImage)
		return strategyImage, []string{strategyImage}, nil
	case strategyDefaultsDirect:
		_, _ = color.New(color.FgCyan).Printf("Using default image %s\n", strategyImage)
		return strategyImage, []string{strategyImage}, nil
	case strategyDefaultsOverlay:
		_, _ = color.New(color.FgCyan).Printf("Building overlay on default image %s...\n", strategyImage)
		chain, err := workspaceLayers(ctx, ws, []string{strategyImage})
		if err != nil {
			return "", nil, err
		}
		final, err := docker.BuildOverlayImage(ctx, chain[len(chain)-1], *ws)
		if err != nil {
			return "", nil, fmt.Errorf("build workspace overlay image: %w", err)
		}
		return final, append(chain, final), nil
	default: // strategyCascade
		_, _ = color.New(color.FgCyan).Printf("Resolving base image...\n")
		base, err := docker.ResolveBaseImage(ctx, cfg, version)
		if err != nil {
			return "", nil, fmt.Errorf("resolve base image: %w", err)
		}
		chain, err := workspaceLayers(ctx, ws, []string{base})
		if err != nil {
			return "", nil, err
		}

		slog.Debug("building workspace layer", "base", chain[len(chain)-1], "dockerfile", ws.Dockerfile)
		final, err := docker.BuildOverlayImage(ctx, chain[len(chain)-1], *ws)
		if err != nil {
			return "", nil, fmt.Errorf("build workspace overlay image: %w", err)
		}
		return final, append(chain, final), nil
	}
}

// workspaceLayers adds the layers jailoc builds for ws on top of the last
// image of chain: Docker when enable_docker is on, then the agent. It returns
// chain with the images it built appended.
func workspaceLayers(ctx context.Context, ws *workspace.Resolved, chain []string) ([]string, error) {
	if ws.EnableDocker {
		image, err := docker.BuildDockerImage(ctx, chain[len(chain)-1])
		if err != nil {
			return nil, fmt.Errorf("add Docker to the workspace image: %w", err)
		}
		chain = append(chain, image)
	}
	if !ws.AgentInstall {
		return chain, nil
	}
	image, err := docker.BuildAgentImage(ctx, chain[len(chain)-1], ws.Agent)
	if err != nil {
		return nil, fmt.Errorf("install %s into the workspace image: %w", ws.Agent.Title, err)
	}
	return append(chain, image), nil
}

const tuiPluginContainerDir = "/etc/jailoc-tui-plugin"

// writeAgentFiles prepares the host files the agent of ws reads: the
// .gitignore of a read-only config directory mount and the TUI plugin.
func writeAgentFiles(ws *workspace.Resolved, cacheDir string) error {
	h := ws.Agent
	if hostPath, ok := config.ReadOnlyMountCoversPath(ws.Mounts, h.ConfigDir); ok && h.ConfigDir != "" {
		if err := ensureConfigGitignore(hostPath, h.ConfigGitignore); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: could not pre-create .gitignore in %s: %v\n", hostPath, err)
		}
	}
	if h.TUIConfigEnv == "" {
		return nil
	}
	return writeTUIConfig(cacheDir)
}

func writeTUIConfig(baseDir string) error {
	if err := writeTUIPlugin(baseDir); err != nil {
		return err
	}

	if err := writeTUIJSON(filepath.Join(baseDir, "tui-container.json"), "file://"+tuiPluginContainerDir); err != nil {
		return err
	}

	return nil
}

func writeTUIJSON(path, specifier string) error {
	config := map[string][]string{
		"plugin": {specifier},
	}

	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("write tui config: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tui-*.json")
	if err != nil {
		return fmt.Errorf("write tui config: %w", err)
	}
	tmpName := filepath.Clean(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName) //nolint:gosec // tmpName is from os.CreateTemp in a controlled directory
		return fmt.Errorf("write tui config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName) //nolint:gosec // tmpName is from os.CreateTemp in a controlled directory
		return fmt.Errorf("write tui config: %w", err)
	}

	if err := os.Chmod(tmpName, 0o644); err != nil { //nolint:gosec // 0o644 is appropriate for non-executable JSON config file
		_ = os.Remove(tmpName) //nolint:gosec // tmpName is from os.CreateTemp in a controlled directory
		return fmt.Errorf("write tui config: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil { //nolint:gosec // both paths are controlled: tmpName from CreateTemp, path from caller
		_ = os.Remove(tmpName) //nolint:gosec // tmpName is from os.CreateTemp in a controlled directory
		return fmt.Errorf("write tui config: %w", err)
	}

	return nil
}

func writeTUIPlugin(baseDir string) error {
	dir := filepath.Join(baseDir, "tui-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // 0o755 required: bind-mount preserves host perms, dir must be readable in container
		return fmt.Errorf("create tui plugin dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), embed.TUIPluginJSON(), 0o644); err != nil { //nolint:gosec // 0o644 is appropriate for non-executable JSON
		return fmt.Errorf("write tui plugin package.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tui.js"), embed.TUIPluginJS(), 0o644); err != nil { //nolint:gosec // 0o644 is appropriate for non-executable JS
		return fmt.Errorf("write tui plugin tui.js: %w", err)
	}
	return nil
}

// ensureConfigGitignore creates hostDir/.gitignore with content unless it
// exists: some agents write it into their config directory on startup and
// fail when the directory is mounted read-only.
func ensureConfigGitignore(hostDir, content string) error {
	gitignorePath := filepath.Join(hostDir, ".gitignore")
	_, err := os.Stat(gitignorePath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", gitignorePath, err)
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil { //nolint:gosec // 0o755: directory must be readable when bind-mounted
		return fmt.Errorf("create directory %q: %w", hostDir, err)
	}
	if err := os.WriteFile(gitignorePath, []byte(content), 0o644); err != nil { //nolint:gosec // 0o644: standard file perms
		return fmt.Errorf("write %q: %w", gitignorePath, err)
	}
	return nil
}

var osStat = os.Stat

func resolveGitConfig(enabled bool) string {
	if !enabled {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".gitconfig")
	if _, err := osStat(path); err == nil {
		return path
	}
	path = filepath.Join(home, ".config", "git", "config")
	if _, err := osStat(path); err == nil {
		return path
	}
	return ""
}

func init() {
	rootCmd.AddCommand(upCmd)
}
