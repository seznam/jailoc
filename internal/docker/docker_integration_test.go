//go:build integration

package docker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func skipWithoutDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skip("Docker client not available: ", err)
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.Ping(ctx); err != nil {
		t.Skip("Docker daemon not reachable: ", err)
	}
}

func TestResolveBaseImageNilCfg(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	tag, err := ResolveBaseImage(context.Background(), nil, "v0.0.0-test")
	if err == nil {
		if tag != "jailoc-base:embedded" {
			t.Fatalf("unexpected tag on nil cfg without error: got %q, want %q", tag, "jailoc-base:embedded")
		}
		return
	}

	if tag != "" {
		t.Fatalf("expected empty tag on error, got %q", tag)
	}
}

func TestResolveBaseImageEmbeddedFallbackWhenNoImageConfig(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	cfg := &config.Config{}
	tag, err := ResolveBaseImage(context.Background(), cfg, "v0.0.0-test")
	if err == nil {
		if tag != "jailoc-base:embedded" {
			t.Fatalf("unexpected tag without error: got %q, want %q", tag, "jailoc-base:embedded")
		}
		return
	}

	if strings.Contains(err.Error(), "load dockerfile") {
		t.Fatalf("unexpected dockerfile load error for empty image config: %v", err)
	}
	if tag != "" {
		t.Fatalf("expected empty tag on error, got %q", tag)
	}
}

func TestBuildOverlayImageDefaultBuildContext(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	tmpDir := t.TempDir()
	wsDockerfile := filepath.Join(tmpDir, "overlay.Dockerfile")
	if err := os.WriteFile(wsDockerfile, []byte("ARG BASE\nFROM ${BASE}\n"), 0o600); err != nil {
		t.Fatalf("write workspace dockerfile: %v", err)
	}

	_, err := BuildOverlayImage(context.Background(), "registry.example.com/base:v1", workspace.Resolved{
		Name:       "ws-default-ctx",
		Dockerfile: wsDockerfile,
	})
	if err == nil {
		t.Fatal("expected docker build error (daemon/build stage), got nil")
	}
	if strings.Contains(err.Error(), "determine build context") {
		t.Fatalf("unexpected build context resolution error: %v", err)
	}
	if strings.Contains(err.Error(), "write temporary workspace Dockerfile") {
		t.Fatalf("unexpected Dockerfile temp write error: %v", err)
	}
}

func TestBuildEmbeddedImage(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create Docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	const tag = "jailoc-base:integration-test"
	if err := buildEmbeddedImage(ctx, cli, tag); err != nil {
		t.Fatalf("buildEmbeddedImage: %v", err)
	}

	t.Cleanup(func() {
		_, _ = cli.ImageRemove(context.Background(), tag, image.RemoveOptions{Force: true})
	})

	inspect, _, err := cli.ImageInspectWithRaw(ctx, tag)
	if err != nil {
		t.Fatalf("inspect built image %q: %v", tag, err)
	}
	if inspect.ID == "" {
		t.Fatal("built image has empty ID")
	}
}

func TestBuildOverlayImageImplicitBaseArg(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	tmpDir := t.TempDir()
	wsDockerfile := filepath.Join(tmpDir, "overlay.Dockerfile")
	// Dockerfile uses ${BASE} without declaring ARG BASE — jailoc must prepend it automatically.
	if err := os.WriteFile(wsDockerfile, []byte("FROM ${BASE}\nRUN echo hello\n"), 0o600); err != nil {
		t.Fatalf("write workspace dockerfile: %v", err)
	}

	_, err := BuildOverlayImage(context.Background(), "registry.example.com/base:v1", workspace.Resolved{
		Name:       "ws-implicit-base",
		Dockerfile: wsDockerfile,
	})
	if err == nil {
		t.Fatal("expected docker build error (image pull), got nil")
	}
	if strings.Contains(err.Error(), "should not be blank") {
		t.Fatalf("BASE arg was not injected: %v", err)
	}
}

func TestBuildOverlayImageExplicitBuildContext(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	dockerfileDir := t.TempDir()
	buildContextDir := t.TempDir()
	wsDockerfile := filepath.Join(dockerfileDir, "overlay.Dockerfile")
	if err := os.WriteFile(wsDockerfile, []byte("ARG BASE\nFROM ${BASE}\n"), 0o600); err != nil {
		t.Fatalf("write workspace dockerfile: %v", err)
	}

	_, err := BuildOverlayImage(context.Background(), "registry.example.com/base:v1", workspace.Resolved{
		Name:         "ws-explicit-ctx",
		Dockerfile:   wsDockerfile,
		BuildContext: buildContextDir,
	})
	if err == nil {
		t.Fatal("expected docker build error (daemon/build stage), got nil")
	}
	if strings.Contains(err.Error(), "determine build context") {
		t.Fatalf("unexpected build context resolution error: %v", err)
	}
	if strings.Contains(err.Error(), "write temporary workspace Dockerfile") {
		t.Fatalf("unexpected Dockerfile temp write error: %v", err)
	}
}

func TestEnsureImageReplacesForeignPlatform(t *testing.T) {
	skipWithoutDocker(t)
	ctx := context.Background()
	const ref = "busybox:1.36.1"
	foreign := "linux/amd64"
	if runtime.GOARCH == "amd64" {
		foreign = "linux/arm64"
	}
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.ImageRemove(ctx, ref, image.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Fatalf("remove %s: %v", ref, err)
	}
	if err := pullImage(ctx, cli, ref, foreign); err != nil {
		t.Fatalf("pull %s for %s: %v", ref, foreign, err)
	}
	if info, err := cli.ImageInspect(ctx, ref); err != nil || imagePlatform(info) == "linux/"+runtime.GOARCH {
		t.Fatalf("precondition: %s is cached for the host platform: %v", ref, err)
	}
	if err := EnsureImage(ctx, ref); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}
	info, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := imagePlatform(info), "linux/"+runtime.GOARCH; got != want {
		t.Fatalf("image platform = %s, want %s", got, want)
	}
}
