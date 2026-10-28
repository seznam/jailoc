package docker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/build"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/fatih/color"
	archive "github.com/moby/go-archive"
	"github.com/moby/term"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/workspace"
)

func displayStream(r io.Reader) error {
	fd, isTerminal := term.GetFdInfo(os.Stderr)
	return jsonmessage.DisplayJSONMessagesStream(r, os.Stderr, fd, isTerminal, nil)
}

func ResolveBaseImage(ctx context.Context, cfg *config.Config, version string) (string, error) {
	slog.Debug("resolving base image", "version", version)

	if cfg != nil && strings.TrimSpace(cfg.Base.Dockerfile) != "" {
		source := strings.TrimSpace(cfg.Base.Dockerfile)
		_, _ = color.New(color.FgCyan).Printf("Loading preset Dockerfile from %s...\n", source)
		content, err := loadDockerfile(ctx, source)
		if err != nil {
			return "", fmt.Errorf("load dockerfile from %q: %w", source, err)
		}

		engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		if err != nil {
			return "", fmt.Errorf("create Docker Engine client for preset build: %w", err)
		}
		defer func() { _ = engineCli.Close() }()

		_, _ = color.New(color.FgCyan).Printf("Building preset base image...\n")
		tag, err := buildPresetImage(ctx, engineCli, content)
		if err != nil {
			return "", fmt.Errorf("build preset image: %w", err)
		}

		slog.Debug("base image resolved", "tag", tag)
		return tag, nil
	}

	const embeddedTag = "jailoc-base:embedded"
	engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client for embedded build: %w", err)
	}
	defer func() { _ = engineCli.Close() }()

	_, _ = color.New(color.FgCyan).Printf("Building embedded base image...\n")
	if err := buildEmbeddedImage(ctx, engineCli, embeddedTag); err != nil {
		return "", fmt.Errorf("build embedded base image: %w", err)
	}

	slog.Debug("base image resolved", "tag", embeddedTag)
	return embeddedTag, nil
}

// proxyBuildArgs returns Docker build args for proxy environment variables
// set in the current process environment. Docker Engine API (unlike Docker CLI)
// does not forward these automatically — they must be passed explicitly.
func proxyBuildArgs() map[string]*string {
	keys := []string{
		"HTTP_PROXY", "http_proxy",
		"HTTPS_PROXY", "https_proxy",
		"NO_PROXY", "no_proxy",
		"FTP_PROXY", "ftp_proxy",
		"ALL_PROXY", "all_proxy",
	}

	args := make(map[string]*string)
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			args[k] = &v
		}
	}

	return args
}

func buildEmbeddedImage(ctx context.Context, cli dockerclient.APIClient, tag string) error {
	tmpDir, err := os.MkdirTemp("", "jailoc-embedded-dockerfile-")
	if err != nil {
		return fmt.Errorf("create temp directory for embedded Dockerfile: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	buildContextDir := tmpDir
	slog.Debug("building image", "tag", tag, "context", buildContextDir)

	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, embed.Dockerfile(), 0o600); err != nil {
		return fmt.Errorf("write embedded Dockerfile to %q: %w", dockerfilePath, err)
	}

	buildCtx, err := archive.TarWithOptions(tmpDir, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("create build context tar for %q: %w", tmpDir, err)
	}
	defer func() { _ = buildCtx.Close() }()

	resp, err := cli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Tags:      []string{tag},
		BuildArgs: proxyBuildArgs(),
		Remove:    true,
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

	hash := sha256.Sum256(dockerfileContent)
	presetTag := fmt.Sprintf("jailoc-base:preset-%x", hash[:8])
	slog.Debug("building image", "tag", presetTag, "context", tmpDir)

	buildCtx, err := archive.TarWithOptions(tmpDir, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("create build context tar for %q: %w", tmpDir, err)
	}
	defer func() { _ = buildCtx.Close() }()

	resp, err := cli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Tags:      []string{presetTag},
		BuildArgs: proxyBuildArgs(),
		Remove:    true,
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

	_, _ = color.New(color.FgCyan).Printf("Loading workspace Dockerfile from %s...\n", ws.Dockerfile)
	dockerfileContent, err := loadDockerfile(ctx, ws.Dockerfile)
	if err != nil {
		return "", fmt.Errorf("load workspace dockerfile from %q: %w", ws.Dockerfile, err)
	}

	dockerfileContent = injectBaseArg(dockerfileContent)

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

	// Workspace overlays are rebuilt unconditionally on every workspace startup.
	// We do NOT check whether overlayTag already exists locally and skip the build —
	// that would silently run a stale image when the user edits their Dockerfile
	// but the edit produces a build error. Build failure must abort startup.
	// Daemon-side layer cache already provides the fast path for unchanged Dockerfiles;
	// adding a second cache layer here would only mask user errors.
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
	overlayBuildArgs := proxyBuildArgs()
	overlayBuildArgs["BASE"] = &baseArg

	_, _ = color.New(color.FgCyan).Printf("Building workspace overlay image (sending build context to Docker daemon)...\n")
	resp, err := engineCli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Tags:       []string{overlayTag},
		BuildArgs:  overlayBuildArgs,
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

// BuildAgentImage installs the pinned npm package of harness h on top of base
// and returns the tag of the result.
func BuildAgentImage(ctx context.Context, base string, h agent.Harness) (string, error) {
	label := fmt.Sprintf("%s %s", h.Title, h.Version)
	return buildLayerImage(ctx, base, "jailoc-agent-"+h.Name, label, func(user string) []byte {
		return agentDockerfile(h, user)
	})
}

// BuildDockerImage adds the embedded Docker layer (Docker CE, buildx,
// compose, nftables) on top of base and returns the tag of the result.
func BuildDockerImage(ctx context.Context, base string) (string, error) {
	return buildLayerImage(ctx, base, "jailoc-docker", "Docker", func(user string) []byte {
		return dockerLayerDockerfile(embed.DockerLayer(), user)
	})
}

// buildLayerImage builds the Dockerfile that dockerfile returns for the user
// of base, with base as the BASE build argument, and tags the result repo
// with a hash of the base image ID and the Dockerfile.
func buildLayerImage(ctx context.Context, base, repo, label string, dockerfile func(user string) []byte) (string, error) {
	if err := EnsureImage(ctx, base); err != nil {
		return "", err
	}
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client for the %s image layer: %w", label, err)
	}
	defer func() { _ = cli.Close() }()
	inspect, err := cli.ImageInspect(ctx, base)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", base, err)
	}
	user := ""
	if inspect.Config != nil {
		user = inspect.Config.User
	}
	content := dockerfile(user)

	tmpDir, err := os.MkdirTemp("", "jailoc-layer-dockerfile-")
	if err != nil {
		return "", fmt.Errorf("create temp directory for the %s image layer: %w", label, err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), content, 0o600); err != nil {
		return "", fmt.Errorf("write the %s image layer Dockerfile: %w", label, err)
	}
	buildCtx, err := archive.TarWithOptions(tmpDir, &archive.TarOptions{})
	if err != nil {
		return "", fmt.Errorf("create build context tar for %q: %w", tmpDir, err)
	}
	defer func() { _ = buildCtx.Close() }()

	hash := sha256.Sum256(append([]byte(inspect.ID+"\n"), content...))
	tag := fmt.Sprintf("%s:%x", repo, hash[:8])
	buildArgs := proxyBuildArgs()
	buildArgs["BASE"] = &base
	_, _ = color.New(color.FgCyan).Printf("Installing %s into the image...\n", label)
	resp, err := cli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Tags:      []string{tag},
		BuildArgs: buildArgs,
		Remove:    true,
	})
	if err != nil {
		return "", fmt.Errorf("build the %s image layer: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := displayStream(resp.Body); err != nil {
		return "", fmt.Errorf("read the %s image layer build output: %w", label, err)
	}
	return tag, nil
}

// asRoot reports whether a layer on an image with this user must switch to
// root for its installation and back afterwards.
func asRoot(user string) bool {
	return user != "" && user != "root" && user != "0" && !strings.HasPrefix(user, "0:") && !strings.HasPrefix(user, "root:")
}

// dockerLayerDockerfile appends to stages, which leave the Docker files in
// /out of the docker stage, a final stage that copies them onto BASE and
// installs nftables unless nft is present, as root, restoring the user.
func dockerLayerDockerfile(stages []byte, user string) []byte {
	var b strings.Builder
	b.WriteString("ARG BASE\n")
	b.Write(stages)
	b.WriteString("\nFROM ${BASE}\n")
	switchUser := asRoot(user)
	if switchUser {
		b.WriteString("USER root\n")
	}
	b.WriteString("RUN command -v nft >/dev/null || (apt-get update \\\n" +
		"    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nftables \\\n" +
		"    && rm -rf /var/lib/apt/lists/*)\n")
	b.WriteString("COPY --from=docker /out/bin/ /usr/local/bin/\n")
	b.WriteString("COPY --from=docker /out/cli-plugins/ /usr/local/lib/docker/cli-plugins/\n")
	if switchUser {
		fmt.Fprintf(&b, "USER %s\n", user)
	}
	return []byte(b.String())
}

// agentDockerfile installs h as root and restores the base image's user.
func agentDockerfile(h agent.Harness, user string) []byte {
	var b strings.Builder
	b.WriteString("ARG BASE\nFROM ${BASE}\n")
	switchUser := asRoot(user)
	if switchUser {
		b.WriteString("USER root\n")
	}
	// npm installs the pinned version without running curl | bash. npm 11
	// runs install scripts of global packages only when allowed; harnesses
	// use them to set up their native binaries.
	fmt.Fprintf(&b, "RUN npm install -g --allow-scripts='%s' '%s@%s' && npm cache clean --force\n", h.Package, h.Package, h.Version)
	if switchUser {
		fmt.Fprintf(&b, "USER %s\n", user)
	}
	return []byte(b.String())
}

// injectBaseArg inserts "ARG BASE\n" after any leading parser directives (# syntax=...,
// # escape=..., # check=...) so that ${BASE} is substitutable in FROM instructions without
// breaking directives that must appear before any instruction.
func injectBaseArg(content []byte) []byte {
	const argLine = "ARG BASE\n"
	i := 0
	for i < len(content) {
		lineEnd := i
		for lineEnd < len(content) && content[lineEnd] != '\n' {
			lineEnd++
		}
		if lineEnd < len(content) {
			lineEnd++
		}
		line := strings.TrimSpace(string(content[i:lineEnd]))
		if line == "" || (strings.HasPrefix(line, "#") && strings.Contains(line, "=")) {
			i = lineEnd
			continue
		}
		break
	}
	result := make([]byte, 0, len(content)+len(argLine))
	result = append(result, content[:i]...)
	result = append(result, argLine...)
	result = append(result, content[i:]...)
	return result
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
