//go:build integration

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/image"
	dockerclient "github.com/moby/moby/client"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func skipWithoutDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cli, err := dockerclient.New()
	if err != nil {
		t.Skip("Docker client not available: ", err)
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.Ping(ctx, dockerclient.PingOptions{}); err != nil {
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

	cli, err := dockerclient.New(dockerclient.WithAPIVersionNegotiation())
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
