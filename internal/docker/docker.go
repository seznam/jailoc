package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/embed"
)

type Client struct {
	composeFile string
	workDir     string
	workspace   string
}

func NewClient(composeFile, workDir, workspace string) *Client {
	return &Client{
		composeFile: composeFile,
		workDir:     workDir,
		workspace:   workspace,
	}
}

func (c *Client) Up(ctx context.Context) error {
	if err := c.runCompose(ctx, nil, nil, nil, "up", "-d"); err != nil {
		return fmt.Errorf("compose up for workspace %q: %w", c.workspace, err)
	}

	return nil
}

func (c *Client) Down(ctx context.Context) error {
	if err := c.runCompose(ctx, nil, nil, nil, "down"); err != nil {
		return fmt.Errorf("compose down for workspace %q: %w", c.workspace, err)
	}

	return nil
}

func (c *Client) IsRunning(ctx context.Context) (bool, error) {
	var out bytes.Buffer

	if err := c.runCompose(ctx, nil, &out, os.Stderr, "ps", "--format", "json"); err != nil {
		return false, fmt.Errorf("compose ps for workspace %q: %w", c.workspace, err)
	}

	running, err := parseServiceState(out.Bytes(), "opencode")
	if err != nil {
		return false, fmt.Errorf("parse compose ps output for workspace %q: %w", c.workspace, err)
	}

	return running, nil
}

func (c *Client) Logs(ctx context.Context, follow bool, w io.Writer) error {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}

	if err := c.runCompose(ctx, nil, w, os.Stderr, args...); err != nil {
		return fmt.Errorf("compose logs for workspace %q: %w", c.workspace, err)
	}

	return nil
}

func (c *Client) Exec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	composeArgs := []string{"exec", "opencode"}
	composeArgs = append(composeArgs, args...)

	if err := c.runCompose(ctx, stdin, stdout, stderr, composeArgs...); err != nil {
		return fmt.Errorf("compose exec for workspace %q: %w", c.workspace, err)
	}

	return nil
}

func ResolveImage(ctx context.Context, cfg *config.Config, version string) (string, error) {
	configDir := config.ConfigDir()
	baseOverride := baseDockerfileOverridePath()

	hasBaseOverride, err := fileExists(baseOverride)
	if err != nil {
		return "", fmt.Errorf("check base Dockerfile override at %q: %w", baseOverride, err)
	}

	if hasBaseOverride {
		const localTag = "jailoc-base:local"
		if err := runDockerCommand(ctx, configDir, nil, nil, nil, "build", "-t", localTag, configDir); err != nil {
			return "", fmt.Errorf("build local base image from %q: %w", configDir, err)
		}

		return localTag, nil
	}

	if cfg != nil && strings.TrimSpace(cfg.Image.Repository) != "" {
		tag := fmt.Sprintf("%s:%s", cfg.Image.Repository, version)
		if err := runDockerCommand(ctx, "", nil, nil, nil, "pull", tag); err == nil {
			return tag, nil
		} else {
			fmt.Fprintf(os.Stderr, "warning: failed to pull image %q: %v\n", tag, err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "jailoc-embedded-dockerfile-")
	if err != nil {
		return "", fmt.Errorf("create temp directory for embedded Dockerfile: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, embed.Dockerfile(), 0o644); err != nil {
		return "", fmt.Errorf("write embedded Dockerfile to %q: %w", dockerfilePath, err)
	}

	const embeddedTag = "jailoc-base:embedded"
	if err := runDockerCommand(ctx, tmpDir, nil, nil, nil, "build", "-t", embeddedTag, "."); err != nil {
		return "", fmt.Errorf("build embedded base image in %q: %w", tmpDir, err)
	}

	fmt.Fprintf(os.Stderr, "warning: using embedded Dockerfile fallback image %q\n", embeddedTag)

	return embeddedTag, nil
}

func ApplyWorkspaceLayer(ctx context.Context, base, workspaceName string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("base image is empty")
	}

	if strings.TrimSpace(workspaceName) == "" {
		return "", fmt.Errorf("workspace name is empty")
	}

	workspaceDockerfile := workspaceDockerfilePath(workspaceName)
	exists, err := fileExists(workspaceDockerfile)
	if err != nil {
		return "", fmt.Errorf("check workspace Dockerfile at %q: %w", workspaceDockerfile, err)
	}

	if !exists {
		return base, nil
	}

	configDir := config.ConfigDir()
	workspaceTag := fmt.Sprintf("jailoc-%s:latest", workspaceName)

	if err := runDockerCommand(
		ctx,
		configDir,
		nil,
		nil,
		nil,
		"build",
		"--build-arg",
		fmt.Sprintf("BASE=%s", base),
		"-t",
		workspaceTag,
		configDir,
	); err != nil {
		return "", fmt.Errorf("build workspace image %q from %q: %w", workspaceTag, configDir, err)
	}

	return workspaceTag, nil
}

func (c *Client) runCompose(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, composeArgs ...string) error {
	args := []string{"compose", "-f", c.composeFile}
	args = append(args, composeArgs...)

	if err := runDockerCommand(ctx, c.workDir, stdin, stdout, stderr, args...); err != nil {
		return fmt.Errorf("run docker compose command %q: %w", strings.Join(composeArgs, " "), err)
	}

	return nil
}

func runDockerCommand(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run docker %s: %w", strings.Join(args, " "), err)
	}

	return nil
}

func parseServiceState(data []byte, service string) (bool, error) {
	type composeService struct {
		Service string `json:"Service"`
		State   string `json:"State"`
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var item composeService
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return false, fmt.Errorf("decode compose ps line %q: %w", line, err)
		}

		if item.Service == service && item.State == "running" {
			return true, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scan compose ps output: %w", err)
	}

	return false, nil
}

func fileExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("stat file %q: %w", path, err)
	}

	return true, nil
}

func baseDockerfileOverridePath() string {
	return config.BaseDockerfileOverridePath()
}

func workspaceDockerfilePath(workspace string) string {
	return config.WorkspaceDockerfilePath(workspace)
}
