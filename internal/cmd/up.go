package cmd

import (
	"bufio"
	"context"
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
	_, hasPasswordErr := password.ReadPasswordFile(ws.Name)
	hasPassword := hasPasswordErr == nil

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
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is already running on port %d\n", ws.Name, ws.Port)
		return nil
	}

	runningPorts, err := docker.RunningWorkspacePorts(ctx)
	if err != nil {
		return fmt.Errorf("check running workspace ports: %w", err)
	}
	if err := checkPortConflict(runningPorts, ws.Name, ws.Port); err != nil {
		return err
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

	_, _ = color.New(color.FgGreen).Printf("Workspace %s started on port %d\n", ws.Name, ws.Port)
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
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc", workspace) + string(filepath.Separator)
	}
	return filepath.Join(home, ".cache", "jailoc", workspace) + string(filepath.Separator)
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

// dockerDesktopSSHSock is the magic socket path used by Docker Desktop and OrbStack.
const dockerDesktopSSHSock = "/run/host-services/ssh-auth.sock"

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
