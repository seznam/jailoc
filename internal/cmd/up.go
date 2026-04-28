package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/password"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const ocConfigContainerPath = "/home/agent/.config/opencode"

var upCmd = &cobra.Command{
	Use:   "up [workspace]",
	Short: "Start a workspace environment",
	Long:  "Start the Docker Compose environment for a workspace. If already running, no-op.",
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

	ws, err := workspace.Resolve(cfg, workspaceFlag)
	if err != nil {
		return fmt.Errorf("resolve workspace %q: %w", workspaceFlag, err)
	}

	_, _ = color.New(color.FgCyan).Printf("Checking Docker availability...\n")
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

	interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: uintptr→int is safe for file descriptors
	resolver := password.DefaultResolver(interactive, cfg.PasswordMode)
	pwSource, err := resolver.Peek(ws.Name)
	if err != nil {
		return fmt.Errorf("check password for workspace %q: %w", ws.Name, err)
	}
	hasPassword := pwSource != ""

	if needsMigration(running, hasPassword) {
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is running without a password.\n", ws.Name)
		if !interactive {
			return fmt.Errorf("password migration required for workspace %q but stdin is not a terminal; restart with: jailoc down %s && jailoc up %s", ws.Name, ws.Name, ws.Name)
		}
		fmt.Printf("Applying a password requires restarting the workspace containers.\n")
		fmt.Printf("Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read migration prompt for workspace %q: %w", ws.Name, err)
		}
		answer = strings.TrimSpace(answer)
		if answer != "y" && answer != "Y" {
			return fmt.Errorf("password migration declined for workspace %q", ws.Name)
		}
	} else if running {
		if ws.ExposePort {
			_, _ = color.New(color.FgYellow).Printf("Workspace %s is already running on port %d\n", ws.Name, ws.Port)
		} else {
			_, _ = color.New(color.FgYellow).Printf("Workspace %s is already running (exec-only, port not exposed)\n", ws.Name)
		}
		return nil
	}

	if ws.ExposePort {
		runningPorts, err := docker.RunningWorkspacePorts(ctx)
		if err != nil {
			return fmt.Errorf("check running workspace ports: %w", err)
		}
		if err := checkPortConflict(runningPorts, ws.Name, ws.Port); err != nil {
			return err
		}
	}

	if hostPath, ok := compose.ReadOnlyMountCoversPath(ws.Mounts, ocConfigContainerPath); ok {
		if err := ensureOCConfigGitignore(hostPath); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr,
				"WARNING: could not pre-create .gitignore in %s: %v\n"+
					"OpenCode (shipped since jailoc 1.13) may fail to start — see https://github.com/anomalyco/opencode/issues/23040\n",
				hostPath, err)
		}
	}

	_, _ = color.New(color.FgCyan).Printf("Resolving image for workspace %s...\n", ws.Name)
	finalImage, err := ResolveAndLayerImage(ctx, cfg, ws, appVersion)
	if err != nil {
		return fmt.Errorf("resolve image for workspace %q: %w", ws.Name, err)
	}

	cacheDir := ComposeCacheDir(ws.Name)
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return fmt.Errorf("create compose cache directory %q: %w", cacheDir, err)
	}

	if err := config.WriteAllowedFiles(ws.Name, cfg); err != nil {
		return fmt.Errorf("write allowed files for workspace %q: %w", ws.Name, err)
	}

	if err := writeEntrypoint(cacheDir); err != nil {
		return err
	}

	if ws.EnableDocker {
		if err := writeDindEntrypoint(cacheDir); err != nil {
			return err
		}
	}

	if err := writeTUIConfig(jailocCacheDir()); err != nil {
		return err
	}

	pw, _, err := resolver.Resolve(ws.Name)
	if err != nil {
		return err
	}

	os.Setenv("OPENCODE_SERVER_PASSWORD", pw) //nolint:gosec,errcheck // only fails if key is empty

	params := compose.ComposeParams{
		WorkspaceName:   ws.Name,
		Port:            ws.Port,
		Image:           finalImage,
		Paths:           ws.Paths,
		Mounts:          ws.Mounts,
		AllowedHosts:    ws.AllowedHosts,
		AllowedNetworks: ws.AllowedNetworks,
		Env:              ws.Env,
		SSHAuthSock:      resolveSSHAuthSock(ws.SSHAuthSock),
		SSHKnownHosts:    resolveSSHKnownHosts(ws.SSHAuthSock),
		GitConfig:        resolveGitConfig(ws.GitConfig),
		CPU:              ws.CPU,
		Memory:           ws.Memory,
		UseDataVolume:    !compose.MountsContainTarget(ws.Mounts, "/home/agent/.local/share/opencode"),
		UseCacheVolume:   !compose.MountsContainTarget(ws.Mounts, "/home/agent/.cache"),
		ExposePort:       ws.ExposePort,
		EnableDocker:     ws.EnableDocker,
	}

	_, _ = color.New(color.FgCyan).Printf("Generating compose configuration...\n")
	if err := compose.WriteComposeFile(params, composePath); err != nil {
		return fmt.Errorf("write compose file for workspace %q: %w", ws.Name, err)
	}

	_, _ = color.New(color.FgCyan).Printf("Starting workspace %s...\n", ws.Name)
	startClient := docker.NewClient(composePath, "", ws.Name)
	if err := startClient.Up(ctx); err != nil {
		return fmt.Errorf("start workspace %q: %w", ws.Name, err)
	}

	if ws.ExposePort {
		_, _ = color.New(color.FgGreen).Printf("Workspace %s started on port %d\n", ws.Name, ws.Port)
	} else {
		_, _ = color.New(color.FgGreen).Printf("Workspace %s started (exec-only, port not exposed)\n", ws.Name)
	}
	return nil
}

func preflightDocker(ctx context.Context, workspaceName string) error {
	tmpDir, err := os.MkdirTemp("", "jailoc-up-preflight-")
	if err != nil {
		return fmt.Errorf("create preflight temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tmpComposePath := filepath.Join(tmpDir, "docker-compose.yml")
	content := []byte("services:\n  opencode:\n    image: busybox\n")
	if err := os.WriteFile(tmpComposePath, content, 0o600); err != nil {
		return fmt.Errorf("write preflight compose file: %w", err)
	}

	checkClient := docker.NewClient(tmpComposePath, "", workspaceName)
	if _, err := checkClient.IsRunning(ctx); err != nil {
		return fmt.Errorf("docker preflight check: %w", err)
	}

	return nil
}

func ComposeCacheDir(workspace string) string {
	return filepath.Join(jailocCacheDir(), workspace) + string(filepath.Separator)
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
	strategyDirectImage     imageStrategy = iota // ws.Image set — compose pulls natively
	strategyDefaultsDirect                       // defaults.Image set, no ws dockerfile — compose pulls natively
	strategyDefaultsOverlay                      // defaults.Image set, ws dockerfile — build overlay using defaults as base
	strategyCascade                              // no image/defaults — existing ResolveBaseImage cascade
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

func ResolveAndLayerImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, version string) (string, error) {
	strategy, strategyImage := resolveImageStrategy(ws.Image, cfg.Defaults.Image, ws.Dockerfile)
	switch strategy {
	case strategyDirectImage:
		_, _ = color.New(color.FgCyan).Printf("Using workspace image %s\n", strategyImage)
		return strategyImage, nil
	case strategyDefaultsDirect:
		_, _ = color.New(color.FgCyan).Printf("Using default image %s\n", strategyImage)
		return strategyImage, nil
	case strategyDefaultsOverlay:
		_, _ = color.New(color.FgCyan).Printf("Building overlay on default image %s...\n", strategyImage)
		final, err := docker.BuildOverlayImage(ctx, strategyImage, *ws)
		if err != nil {
			return "", fmt.Errorf("build workspace overlay image: %w", err)
		}
		return final, nil
	default: // strategyCascade
		_, _ = color.New(color.FgCyan).Printf("Resolving base image...\n")
		base, err := docker.ResolveBaseImage(ctx, cfg, version)
		if err != nil {
			return "", fmt.Errorf("resolve base image: %w", err)
		}

		final, err := docker.BuildOverlayImage(ctx, base, *ws)
		if err != nil {
			return "", fmt.Errorf("build workspace overlay image: %w", err)
		}
		return final, nil
	}
}

func isComposeFileMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "open ") && strings.Contains(msg, "docker-compose.yml")
}

// needsMigration returns true when a workspace is running but has no stored
// password — i.e. it was started before automatic password management existed.
func needsMigration(isRunning bool, hasPassword bool) bool {
	return isRunning && !hasPassword
}

// checkPortConflict returns an error if another running workspace already
// occupies the target port. It skips the target workspace itself (a restart
// scenario where the container is still shutting down).
func checkPortConflict(ports map[string]int, targetName string, targetPort int) error {
	for name, port := range ports {
		if name == targetName {
			continue
		}
		if port == targetPort {
			return fmt.Errorf("port %d is already in use by running workspace %q — stop it first with: jailoc down %s", targetPort, name, name)
		}
	}
	return nil
}

// writeEntrypoint writes the embedded entrypoint.sh to the workspace cache dir
// so it can be bind-mounted into the container. Uses 0o755 (not the usual 0o600)
// because Docker bind-mounts preserve host permissions and the script must be
// executable inside the container.
func writeEntrypoint(cacheDir string) error {
	p := filepath.Join(cacheDir, "entrypoint.sh")
	if err := os.WriteFile(p, embed.Entrypoint(), 0o755); err != nil { //nolint:gosec // 0o755 required: bind-mount preserves host perms, script must be executable in container
		return fmt.Errorf("write entrypoint: %w", err)
	}
	if err := os.Chmod(p, 0o755); err != nil { //nolint:gosec // ensure +x even when file already existed
		return fmt.Errorf("chmod entrypoint: %w", err)
	}
	return nil
}

func writeDindEntrypoint(cacheDir string) error {
	p := filepath.Join(cacheDir, "dind-entrypoint.sh")
	if err := os.WriteFile(p, embed.DindEntrypoint(), 0o755); err != nil { //nolint:gosec // 0o755 required: bind-mount preserves host perms, script must be executable in container
		return fmt.Errorf("write dind entrypoint: %w", err)
	}
	if err := os.Chmod(p, 0o755); err != nil { //nolint:gosec // ensure +x even when file already existed
		return fmt.Errorf("chmod dind entrypoint: %w", err)
	}
	return nil
}

const tuiPluginContainerDir = "/etc/jailoc-tui-plugin"

func writeTUIConfig(baseDir string) error {
	if err := writeTUIPlugin(baseDir); err != nil {
		return err
	}

	// Host-side tui.json: opencode attach on the host reads this file, so the
	// plugin path must resolve on the host filesystem. Shared across all
	// workspaces — the plugin payload is identical and workspace identity
	// comes from JAILOC_WORKSPACE at runtime.
	hostPluginDir := filepath.Join(baseDir, "tui-plugin")
	if err := writeTUIJSON(filepath.Join(baseDir, "tui.json"), "file://"+hostPluginDir); err != nil {
		return err
	}

	// Container-side tui.json: mounted into the container where the plugin
	// directory is bind-mounted at /etc/jailoc-tui-plugin.
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

// dockerDesktopSSHSock is the magic socket path used by Docker Desktop and OrbStack.
const dockerDesktopSSHSock = "/run/host-services/ssh-auth.sock"

// ocConfigGitignore is the .gitignore content that OpenCode (v1.14.22+, shipped
// since jailoc 1.13) expects in its config directory. Without it, OpenCode
// crashes with EROFS on read-only filesystems.
// See: https://github.com/anomalyco/opencode/issues/23040
const ocConfigGitignore = `node_modules
package.json
package-lock.json
bun.lock
.gitignore
`

func ensureOCConfigGitignore(hostDir string) error {
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
	if err := os.WriteFile(gitignorePath, []byte(ocConfigGitignore), 0o644); err != nil { //nolint:gosec // 0o644: standard file perms
		return fmt.Errorf("write %q: %w", gitignorePath, err)
	}
	return nil
}

var osStat = os.Stat

func resolveSSHAuthSock(enabled bool) string {
	if !enabled {
		return ""
	}
	if _, err := osStat(dockerDesktopSSHSock); err == nil {
		return dockerDesktopSSHSock
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if _, err := osStat(sock); err == nil {
			return sock
		}
	}
	return ""
}

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

func resolveSSHKnownHosts(enabled bool) string {
	if !enabled {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := osStat(path); err == nil {
		return path
	}
	return ""
}

func init() {
	rootCmd.AddCommand(upCmd)
}
