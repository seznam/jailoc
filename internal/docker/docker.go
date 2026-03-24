package docker

import (
	"context"
	"crypto/sha256"
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
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	archive "github.com/moby/go-archive"
	"github.com/moby/term"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/workspace"
)

func displayStream(r io.Reader) error {
	fd, isTerminal := term.GetFdInfo(os.Stderr)
	return jsonmessage.DisplayJSONMessagesStream(r, os.Stderr, fd, isTerminal, nil)
}

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
	container, err := c.opencodeContainer(ctx)
	if err != nil {
		return false, err
	}

	return container.ID != "", nil
}

func (c *Client) CurrentContainerID(ctx context.Context) (string, error) {
	container, err := c.opencodeContainer(ctx)
	if err != nil {
		return "", err
	}

	return container.ID, nil
}

func (c *Client) opencodeContainer(ctx context.Context) (api.ContainerSummary, error) {
	if err := c.initComposeSvc(); err != nil {
		return api.ContainerSummary{}, err
	}

	containers, err := c.svc.Ps(ctx, "jailoc-"+c.workspace, api.PsOptions{All: true})
	if err != nil {
		return api.ContainerSummary{}, fmt.Errorf("compose ps for workspace %q: %w", c.workspace, err)
	}

	return currentOpencodeContainer(containers), nil
}

func currentOpencodeContainer(containers []api.ContainerSummary) api.ContainerSummary {
	var selected api.ContainerSummary

	for _, ct := range containers {
		if ct.Service != "opencode" || ct.State != "running" {
			continue
		}

		if selected.ID == "" || ct.Created > selected.Created {
			selected = ct
		}
	}

	return selected
}

type writerLogConsumer struct{ w io.Writer }

func (wlc *writerLogConsumer) Log(name, msg string) {
	_, _ = fmt.Fprintf(wlc.w, "%s\n", msg)
}

func (wlc *writerLogConsumer) Err(name, msg string) {
	_, _ = fmt.Fprintf(wlc.w, "%s\n", msg)
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

func ResolveBaseImage(ctx context.Context, cfg *config.Config, version string) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.Image.Dockerfile) != "" {
		source := strings.TrimSpace(cfg.Image.Dockerfile)
		content, err := loadDockerfile(ctx, source)
		if err != nil {
			return "", fmt.Errorf("load dockerfile from %q: %w", source, err)
		}

		engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			return "", fmt.Errorf("create Docker Engine client for preset build: %w", err)
		}
		defer func() { _ = engineCli.Close() }()

		tag, err := buildPresetImage(ctx, engineCli, content)
		if err != nil {
			return "", fmt.Errorf("build preset image: %w", err)
		}

		return tag, nil
	}

	if cfg != nil && strings.TrimSpace(cfg.Image.Repository) != "" {
		tag := fmt.Sprintf("%s:%s", strings.TrimSpace(cfg.Image.Repository), version)

		engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			return "", fmt.Errorf("create Docker Engine client for pull: %w", err)
		}
		defer func() { _ = engineCli.Close() }()

		if err := pullImage(ctx, engineCli, tag); err == nil {
			return tag, nil
		}
		fmt.Printf("registry pull failed for %s, falling back to embedded build\n", tag)
	}

	const embeddedTag = "jailoc-base:embedded"
	engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client for embedded build: %w", err)
	}
	defer func() { _ = engineCli.Close() }()

	if err := buildEmbeddedImage(ctx, engineCli, embeddedTag); err != nil {
		return "", fmt.Errorf("build embedded base image: %w", err)
	}

	return embeddedTag, nil
}

func pullImage(ctx context.Context, cli dockerclient.APIClient, tag string) error {
	reader, err := cli.ImagePull(ctx, tag, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %q: %w", tag, err)
	}
	defer func() { _ = reader.Close() }()

	if err := displayStream(reader); err != nil {
		return fmt.Errorf("read pull output for image %q: %w", tag, err)
	}

	return nil
}

func buildEmbeddedImage(ctx context.Context, cli dockerclient.APIClient, tag string) error {
	tmpDir, err := os.MkdirTemp("", "jailoc-embedded-dockerfile-")
	if err != nil {
		return fmt.Errorf("create temp directory for embedded Dockerfile: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, embed.Dockerfile(), 0o600); err != nil {
		return fmt.Errorf("write embedded Dockerfile to %q: %w", dockerfilePath, err)
	}

	entrypointPath := filepath.Join(tmpDir, "entrypoint.sh")
	if err := os.WriteFile(entrypointPath, embed.Entrypoint(), 0o600); err != nil {
		return fmt.Errorf("write embedded entrypoint.sh to %q: %w", entrypointPath, err)
	}

	buildCtx, err := archive.TarWithOptions(tmpDir, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("create build context tar for %q: %w", tmpDir, err)
	}
	defer func() { _ = buildCtx.Close() }()

	resp, err := cli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Tags:   []string{tag},
		Remove: true,
	})
	if err != nil {
		return fmt.Errorf("build embedded image in %q: %w", tmpDir, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := displayStream(resp.Body); err != nil {
		return fmt.Errorf("read embedded build output: %w", err)
	}

	return nil
}

func buildPresetImage(ctx context.Context, cli dockerclient.APIClient, dockerfileContent []byte) (string, error) {
	if len(dockerfileContent) == 0 {
		return "", fmt.Errorf("dockerfile content is empty")
	}

	tmpDir, err := os.MkdirTemp("", "jailoc-preset-dockerfile-")
	if err != nil {
		return "", fmt.Errorf("create temp directory for preset Dockerfile: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, dockerfileContent, 0o600); err != nil {
		return "", fmt.Errorf("write preset Dockerfile to %q: %w", dockerfilePath, err)
	}

	entrypointPath := filepath.Join(tmpDir, "entrypoint.sh")
	if err := os.WriteFile(entrypointPath, embed.Entrypoint(), 0o600); err != nil {
		return "", fmt.Errorf("write entrypoint.sh to %q: %w", entrypointPath, err)
	}

	hash := sha256.Sum256(dockerfileContent)
	presetTag := fmt.Sprintf("jailoc-base:preset-%x", hash[:8])

	buildCtx, err := archive.TarWithOptions(tmpDir, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("create build context tar for %q: %w", tmpDir, err)
	}
	defer func() { _ = buildCtx.Close() }()

	resp, err := cli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Tags:   []string{presetTag},
		Remove: true,
	})
	if err != nil {
		return "", fmt.Errorf("build preset image in %q: %w", tmpDir, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := displayStream(resp.Body); err != nil {
		return "", fmt.Errorf("read preset build output: %w", err)
	}

	return presetTag, nil
}

func BuildOverlayImage(ctx context.Context, base string, ws workspace.Resolved) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("base image is empty")
	}

	if strings.TrimSpace(ws.Dockerfile) == "" {
		return base, nil
	}

	dockerfileContent, err := loadDockerfile(ctx, ws.Dockerfile)
	if err != nil {
		return "", fmt.Errorf("load workspace dockerfile from %q: %w", ws.Dockerfile, err)
	}

	buildContextDir, cleanupCtx, err := resolveOverlayBuildContext(ws)
	if err != nil {
		return "", fmt.Errorf("determine build context for workspace %q: %w", ws.Name, err)
	}
	defer cleanupCtx()

	tmpDockerfile, err := os.CreateTemp(buildContextDir, "jailoc-overlay-*.Dockerfile")
	if err != nil {
		return "", fmt.Errorf("write temporary workspace Dockerfile in %q: %w", buildContextDir, err)
	}
	tmpDockerfilePath := tmpDockerfile.Name()
	defer func() { _ = os.Remove(tmpDockerfilePath) }()

	if _, err := tmpDockerfile.Write(dockerfileContent); err != nil {
		_ = tmpDockerfile.Close()
		return "", fmt.Errorf("write temporary workspace Dockerfile %q: %w", tmpDockerfilePath, err)
	}
	if err := tmpDockerfile.Close(); err != nil {
		return "", fmt.Errorf("close temporary workspace Dockerfile %q: %w", tmpDockerfilePath, err)
	}

	hash := sha256.Sum256(dockerfileContent)
	hashHex := fmt.Sprintf("%x", hash)
	overlayTag := fmt.Sprintf("jailoc-%s:%s", ws.Name, hashHex[:8])

	engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client for workspace %q overlay build: %w", ws.Name, err)
	}
	defer func() { _ = engineCli.Close() }()

	buildCtx, err := archive.TarWithOptions(buildContextDir, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("create build context tar for workspace %q from %q: %w", ws.Name, buildContextDir, err)
	}
	defer func() { _ = buildCtx.Close() }()

	baseArg := base
	resp, err := engineCli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Tags:       []string{overlayTag},
		BuildArgs:  map[string]*string{"BASE": &baseArg},
		Dockerfile: filepath.Base(tmpDockerfilePath),
		Remove:     true,
	})
	if err != nil {
		return "", fmt.Errorf("build workspace overlay image %q from %q: %w", overlayTag, buildContextDir, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := displayStream(resp.Body); err != nil {
		return "", fmt.Errorf("read workspace overlay build output for %q: %w", ws.Name, err)
	}

	return overlayTag, nil
}

func resolveOverlayBuildContext(ws workspace.Resolved) (dir string, cleanup func(), err error) {
	if strings.TrimSpace(ws.BuildContext) != "" {
		return ws.BuildContext, func() {}, nil
	}

	sourceKind, err := detectSourceType(ws.Dockerfile)
	if err != nil {
		return "", func() {}, fmt.Errorf("detect dockerfile source type: %w", err)
	}

	if sourceKind == sourceLocal {
		return filepath.Dir(ws.Dockerfile), func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", "jailoc-overlay-context-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary build context for HTTP dockerfile: %w", err)
	}

	return tmpDir, func() { _ = os.RemoveAll(tmpDir) }, nil
}
