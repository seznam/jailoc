package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up [workspace]",
	Short: "Start a workspace environment",
	Long:  "Start a workspace VM. If already running, apply reloadable configuration changes.\nWith --all, does this for every configured workspace.",
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

	wss, err := targetWorkspaces(cfg, args)
	if err != nil {
		return err
	}
	return forEachWorkspace(ctx, wss, func(ws *workspace.Resolved) error {
		slog.Debug("starting workspace", "name", ws.Name)
		return runUpVM(ctx, cfg, ws)
	})
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
		start := time.Now()
		final, err := docker.BuildOverlayImage(ctx, chain[len(chain)-1], *ws)
		if err != nil {
			return "", nil, fmt.Errorf("build workspace overlay image: %w", err)
		}
		if final != chain[len(chain)-1] {
			logStep("build overlay image", start, "image", final)
		}
		return final, append(chain, final), nil
	default: // strategyCascade
		_, _ = color.New(color.FgCyan).Printf("Resolving base image...\n")
		start := time.Now()
		base, err := docker.ResolveBaseImage(ctx, cfg, version)
		if err != nil {
			return "", nil, fmt.Errorf("resolve base image: %w", err)
		}
		logStep("resolve base image", start, "image", base)
		chain, err := workspaceLayers(ctx, ws, []string{base})
		if err != nil {
			return "", nil, err
		}

		slog.Debug("building workspace layer", "base", chain[len(chain)-1], "dockerfile", ws.Dockerfile)
		start = time.Now()
		final, err := docker.BuildOverlayImage(ctx, chain[len(chain)-1], *ws)
		if err != nil {
			return "", nil, fmt.Errorf("build workspace overlay image: %w", err)
		}
		if final != chain[len(chain)-1] {
			logStep("build overlay image", start, "image", final)
		}
		return final, append(chain, final), nil
	}
}

// workspaceLayers adds the layers jailoc builds for ws on top of the last
// image of chain: Docker when enable_docker is on, then the agent. It returns
// chain with the images it built appended.
func workspaceLayers(ctx context.Context, ws *workspace.Resolved, chain []string) ([]string, error) {
	if ws.EnableDocker {
		start := time.Now()
		image, err := docker.BuildDockerImage(ctx, chain[len(chain)-1])
		if err != nil {
			return nil, fmt.Errorf("add Docker to the workspace image: %w", err)
		}
		logStep("build Docker layer", start, "image", image)
		chain = append(chain, image)
	}
	if !ws.AgentInstall {
		return chain, nil
	}
	start := time.Now()
	image, err := docker.BuildAgentImage(ctx, chain[len(chain)-1], ws.Agent)
	if err != nil {
		return nil, fmt.Errorf("install %s into the workspace image: %w", ws.Agent.Title, err)
	}
	logStep("build agent layer", start, "image", image)
	return append(chain, image), nil
}

// logStep logs how long the step that began at start took, for finding
// slow phases in jailoc.log.
func logStep(step string, start time.Time, args ...any) {
	slog.Info("step finished", append([]any{"step", step, "duration", time.Since(start).Round(time.Millisecond)}, args...)...)
}

const tuiPluginContainerDir = agent.PluginDir

// writeAgentFiles prepares the host files the agent of ws reads: the
// .gitignore of a read-only config directory mount and the TUI plugin.
func writeAgentFiles(ws *workspace.Resolved, cacheDir string) error {
	h := ws.Agent
	if hostPath, ok := config.ReadOnlyMountCoversPath(ws.Mounts, h.ConfigDir); ok && h.ConfigDir != "" {
		if err := ensureConfigGitignore(hostPath, h.ConfigGitignore); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: could not pre-create .gitignore in %s: %v\n", hostPath, err)
		}
	}
	switch h.TUIPlugin {
	case agent.OpenCode1TUI:
		return writeTUIConfig(cacheDir)
	case agent.OpenCode2TUI:
		return writeTUIPlugin(cacheDir, openCodePluginFiles(embed.TUIPluginOpenCode2JS()))
	case agent.OmOExtension:
		return writeTUIPlugin(cacheDir, map[string][]byte{"omo.js": embed.OmOExtensionJS()})
	case agent.ClaudeStatusLine:
		settings, err := statusLineSettings("claude")
		if err != nil {
			return err
		}
		return writeTUIPlugin(cacheDir, map[string][]byte{
			"statusline.mjs":       embed.StatusLineJS(),
			"claude-settings.json": settings,
		})
	case agent.CopilotStatusLine:
		return writeTUIPlugin(cacheDir, map[string][]byte{"statusline.mjs": embed.StatusLineJS()})
	default:
		return nil
	}
}

// statusLineCommand is the status line command of harness (claude or
// copilot) that runs the jailoc status line script.
func statusLineCommand(harness string) string {
	return "node " + tuiPluginContainerDir + "/statusline.mjs " + harness
}

// statusLine is the statusLine setting of harness (claude or copilot).
func statusLine(harness string) map[string]string {
	return map[string]string{"type": "command", "command": statusLineCommand(harness)}
}

// statusLineSettings returns the settings file of harness that sets the
// jailoc status line.
func statusLineSettings(harness string) ([]byte, error) {
	data, err := json.Marshal(map[string]any{"statusLine": statusLine(harness)})
	if err != nil {
		return nil, fmt.Errorf("encode %s settings: %w", harness, err)
	}
	return data, nil
}

func openCodePluginFiles(js []byte) map[string][]byte {
	return map[string][]byte{"package.json": embed.TUIPluginJSON(), "tui.js": js}
}

func writeTUIConfig(baseDir string) error {
	if err := writeTUIPlugin(baseDir, openCodePluginFiles(embed.TUIPluginOpenCode1JS())); err != nil {
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

// writeTUIPlugin writes files into the plugin directory under baseDir that
// the guest sees at tuiPluginContainerDir.
func writeTUIPlugin(baseDir string, files map[string][]byte) error {
	dir := filepath.Join(baseDir, "tui-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // 0o755 required: bind-mount preserves host perms, dir must be readable in container
		return fmt.Errorf("create tui plugin dir: %w", err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil { //nolint:gosec // 0o644 is appropriate for non-executable plugin files
			return fmt.Errorf("write tui plugin %s: %w", name, err)
		}
	}
	return nil
}

// cliConfigContent returns the OpenCode 2 cli.json content that loads the
// jailoc TUI plugin. The content's plugins replace those of the cli.json in
// the agent's config directory, so they are that file's plugins, or the
// tui.json plugins OpenCode 2 migrates when there is no cli.json, followed
// by the jailoc plugin. Relative plugin paths are made absolute against the
// config directory.
func cliConfigContent(ws *workspace.Resolved) (string, error) {
	h := ws.Agent
	var plugins []json.RawMessage
	if hostDir, ok := config.MountHostPath(ws.Mounts, h.ConfigDir); ok {
		var err error
		plugins, err = configuredTUIPlugins(hostDir, h.ConfigDir)
		if err != nil {
			return "", err
		}
	}
	jailoc, err := json.Marshal("file://" + tuiPluginContainerDir)
	if err != nil {
		return "", fmt.Errorf("encode tui plugin: %w", err)
	}
	if !slices.ContainsFunc(plugins, func(p json.RawMessage) bool { return bytes.Equal(p, jailoc) }) {
		plugins = append(plugins, jailoc)
	}
	data, err := json.Marshal(map[string][]json.RawMessage{"plugins": plugins})
	if err != nil {
		return "", fmt.Errorf("encode cli config: %w", err)
	}
	return string(data), nil
}

// configuredTUIPlugins returns the TUI plugins configured in hostDir, the
// host directory of the guest config directory guestDir.
func configuredTUIPlugins(hostDir, guestDir string) ([]json.RawMessage, error) {
	for _, f := range []struct{ name, key string }{{"cli.json", "plugins"}, {"tui.json", "plugin"}} {
		data, err := os.ReadFile(filepath.Join(hostDir, f.name)) //nolint:gosec // the agent's config directory mounted into the workspace
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.name, err)
		}
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Join(hostDir, f.name), err)
		}
		var plugins []json.RawMessage
		if raw, ok := cfg[f.key]; ok {
			if err := json.Unmarshal(raw, &plugins); err != nil {
				return nil, fmt.Errorf("parse %s %s: %w", filepath.Join(hostDir, f.name), f.key, err)
			}
		}
		for i, p := range plugins {
			var spec string
			if json.Unmarshal(p, &spec) != nil || !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
				continue
			}
			if plugins[i], err = json.Marshal(path.Join(guestDir, spec)); err != nil {
				return nil, fmt.Errorf("encode plugin %q: %w", spec, err)
			}
		}
		return plugins, nil
	}
	return nil, nil
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
	addAllFlag(upCmd, "start all configured workspaces")
	rootCmd.AddCommand(upCmd)
}
