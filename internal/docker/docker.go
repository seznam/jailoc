package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/embed"
)

type Client struct {
	composeFile string
	workDir     string
	workspace   string
	svcOnce     sync.Once
	svcErr      error
	svc         api.Compose
}

func NewClient(composeFile, workDir, workspace string) *Client {
	return &Client{
		composeFile: composeFile,
		workDir:     workDir,
		workspace:   workspace,
	}
}

func (c *Client) Up(ctx context.Context) error {
	if err := c.initComposeSvc(); err != nil {
		return err
	}

	project, err := c.svc.LoadProject(ctx, api.ProjectLoadOptions{
		ConfigPaths: []string{c.composeFile},
		ProjectName: "jailoc-" + c.workspace,
	})
	if err != nil {
		return fmt.Errorf("load compose project for workspace %q: %w", c.workspace, err)
	}

	if err := c.svc.Up(ctx, project, api.UpOptions{Start: api.StartOptions{}}); err != nil {
		return fmt.Errorf("compose up for workspace %q: %w", c.workspace, err)
	}

	return nil
}

func (c *Client) Down(ctx context.Context) error {
	if err := c.initComposeSvc(); err != nil {
		return err
	}

	if err := c.svc.Down(ctx, "jailoc-"+c.workspace, api.DownOptions{}); err != nil {
		return fmt.Errorf("compose down for workspace %q: %w", c.workspace, err)
	}

	return nil
}

func (c *Client) IsRunning(ctx context.Context) (bool, error) {
	if err := c.initComposeSvc(); err != nil {
		return false, err
	}

	containers, err := c.svc.Ps(ctx, "jailoc-"+c.workspace, api.PsOptions{All: true})
	if err != nil {
		return false, fmt.Errorf("compose ps for workspace %q: %w", c.workspace, err)
	}

	for _, ct := range containers {
		if ct.Service == "opencode" && ct.State == "running" {
			return true, nil
		}
	}

	return false, nil
}

type writerLogConsumer struct{ w io.Writer }

func (wlc *writerLogConsumer) Log(name, msg string) {
	fmt.Fprintf(wlc.w, "%s\n", msg)
}

func (wlc *writerLogConsumer) Err(name, msg string) {
	fmt.Fprintf(wlc.w, "%s\n", msg)
}

func (wlc *writerLogConsumer) Status(name, msg string) {}

func (c *Client) Logs(ctx context.Context, follow bool, w io.Writer) error {
	if err := c.initComposeSvc(); err != nil {
		return err
	}

	consumer := &writerLogConsumer{w: w}
	if err := c.svc.Logs(ctx, "jailoc-"+c.workspace, consumer, api.LogOptions{Follow: follow}); err != nil {
		return fmt.Errorf("compose logs for workspace %q: %w", c.workspace, err)
	}

	return nil
}

func (c *Client) Exec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var stdinRC io.ReadCloser
	if stdin != nil {
		if rc, ok := stdin.(io.ReadCloser); ok {
			stdinRC = rc
		} else {
			stdinRC = io.NopCloser(stdin)
		}
	}

	cliOpts := []command.CLIOption{
		command.WithOutputStream(stdout),
		command.WithErrorStream(stderr),
	}
	if stdinRC != nil {
		cliOpts = append(cliOpts, command.WithInputStream(stdinRC))
	}

	dockerCLI, err := command.NewDockerCli(cliOpts...)
	if err != nil {
		return fmt.Errorf("create Docker CLI for exec: %w", err)
	}

	if err := dockerCLI.Initialize(&flags.ClientOptions{}); err != nil {
		return fmt.Errorf("initialize Docker CLI for exec: %w", err)
	}

	svc, err := compose.NewComposeService(dockerCLI)
	if err != nil {
		return fmt.Errorf("create Compose service for exec: %w", err)
	}

	exitCode, err := svc.Exec(ctx, "jailoc-"+c.workspace, api.RunOptions{
		Service:     "opencode",
		Command:     args,
		Tty:         true,
		Interactive: stdin != nil,
		Index:       1,
	})
	if err != nil {
		return fmt.Errorf("compose exec for workspace %q: %w", c.workspace, err)
	}

	if exitCode != 0 {
		return fmt.Errorf("exec exited with code %d", exitCode)
	}

	return nil
}

func (c *Client) initComposeSvc() error {
	c.svcOnce.Do(func() {
		dockerCLI, err := command.NewDockerCli()
		if err != nil {
			c.svcErr = fmt.Errorf("create Docker CLI: %w", err)
			return
		}
		if err := dockerCLI.Initialize(&flags.ClientOptions{}); err != nil {
			c.svcErr = fmt.Errorf("initialize Docker CLI: %w", err)
			return
		}
		svc, err := compose.NewComposeService(dockerCLI)
		if err != nil {
			c.svcErr = fmt.Errorf("create Compose service: %w", err)
			return
		}
		c.svc = svc
	})

	return c.svcErr
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
		engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			return "", fmt.Errorf("create Docker Engine client: %w", err)
		}
		defer engineCli.Close()

		buildCtx, err := archive.TarWithOptions(configDir, &archive.TarOptions{})
		if err != nil {
			return "", fmt.Errorf("create build context tar for %q: %w", configDir, err)
		}
		defer buildCtx.Close()

		resp, err := engineCli.ImageBuild(ctx, buildCtx, dockertypes.ImageBuildOptions{
			Tags:   []string{localTag},
			Remove: true,
		})
		if err != nil {
			return "", fmt.Errorf("build local base image from %q: %w", configDir, err)
		}
		defer resp.Body.Close()

		if _, err := io.Copy(os.Stderr, resp.Body); err != nil {
			return "", fmt.Errorf("read build output: %w", err)
		}

		return localTag, nil
	}

	if cfg != nil && strings.TrimSpace(cfg.Image.Repository) != "" {
		tag := fmt.Sprintf("%s:%s", cfg.Image.Repository, version)
		engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to create Docker Engine client to pull image %q: %v\n", tag, err)
		} else {
			defer engineCli.Close()

			reader, err := engineCli.ImagePull(ctx, tag, image.PullOptions{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to pull image %q: %v\n", tag, err)
			} else {
				defer reader.Close()

				if _, err := io.Copy(os.Stderr, reader); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to read pull output for image %q: %v\n", tag, err)
				} else {
					return tag, nil
				}
			}
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

	entrypointPath := filepath.Join(tmpDir, "entrypoint.sh")
	if err := os.WriteFile(entrypointPath, embed.Entrypoint(), 0o755); err != nil {
		return "", fmt.Errorf("write embedded entrypoint.sh to %q: %w", entrypointPath, err)
	}

	const embeddedTag = "jailoc-base:embedded"
	engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer engineCli.Close()

	buildCtx, err := archive.TarWithOptions(tmpDir, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("create build context tar for %q: %w", tmpDir, err)
	}
	defer buildCtx.Close()

	resp, err := engineCli.ImageBuild(ctx, buildCtx, dockertypes.ImageBuildOptions{
		Tags:   []string{embeddedTag},
		Remove: true,
	})
	if err != nil {
		return "", fmt.Errorf("build embedded base image in %q: %w", tmpDir, err)
	}
	defer resp.Body.Close()

	if _, err := io.Copy(os.Stderr, resp.Body); err != nil {
		return "", fmt.Errorf("read build output: %w", err)
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

	engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer engineCli.Close()

	buildCtx, err := archive.TarWithOptions(configDir, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("create build context tar for %q: %w", configDir, err)
	}
	defer buildCtx.Close()

	resp, err := engineCli.ImageBuild(ctx, buildCtx, dockertypes.ImageBuildOptions{
		Tags:       []string{workspaceTag},
		BuildArgs:  map[string]*string{"BASE": &base},
		Dockerfile: workspaceName + ".Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return "", fmt.Errorf("build workspace image %q from %q: %w", workspaceTag, configDir, err)
	}
	defer resp.Body.Close()

	if _, err := io.Copy(os.Stderr, resp.Body); err != nil {
		return "", fmt.Errorf("read build output: %w", err)
	}

	return workspaceTag, nil
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
