package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	containertypes "github.com/moby/moby/api/types/container"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	composeFile := "/tmp/jailoc/compose.yml"
	workDir := "/tmp/jailoc"
	workspace := "default"

	client := NewClient(composeFile, workDir, workspace)

	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.composeFile != composeFile {
		t.Fatalf("unexpected composeFile: got %q, want %q", client.composeFile, composeFile)
	}
	if client.workDir != workDir {
		t.Fatalf("unexpected workDir: got %q, want %q", client.workDir, workDir)
	}
	if client.workspace != workspace {
		t.Fatalf("unexpected workspace: got %q, want %q", client.workspace, workspace)
	}
}

func TestWriterLogConsumer(t *testing.T) {
	t.Parallel()

	t.Run("Log writes message with newline", func(t *testing.T) {
		t.Parallel()

		buf := &bytes.Buffer{}
		consumer := &writerLogConsumer{w: buf}

		consumer.Log("svc", "hello")

		if buf.String() != "hello\n" {
			t.Fatalf("unexpected output: got %q, want %q", buf.String(), "hello\n")
		}
	})

	t.Run("Err writes message with newline", func(t *testing.T) {
		t.Parallel()

		buf := &bytes.Buffer{}
		consumer := &writerLogConsumer{w: buf}

		consumer.Err("svc", "err msg")

		if buf.String() != "err msg\n" {
			t.Fatalf("unexpected output: got %q, want %q", buf.String(), "err msg\n")
		}
	})

	t.Run("Status is a no-op", func(t *testing.T) {
		t.Parallel()

		buf := &bytes.Buffer{}
		consumer := &writerLogConsumer{w: buf}

		consumer.Status("svc", "status")

		if buf.String() != "" {
			t.Fatalf("expected empty buffer, got %q", buf.String())
		}
	})
}

func TestCurrentOpencodeContainer(t *testing.T) {
	t.Parallel()

	t.Run("selects newest running opencode container", func(t *testing.T) {
		t.Parallel()

		got := currentOpencodeContainer([]api.ContainerSummary{
			{ID: "old", Service: "opencode", State: containertypes.StateRunning, Created: 100},
			{ID: "new", Service: "opencode", State: containertypes.StateRunning, Created: 200},
			{ID: "dind", Service: "dind", State: containertypes.StateRunning, Created: 300},
		})

		if got.ID != "new" {
			t.Fatalf("got container %q, want %q", got.ID, "new")
		}
	})

	t.Run("ignores non-running opencode containers", func(t *testing.T) {
		t.Parallel()

		got := currentOpencodeContainer([]api.ContainerSummary{
			{ID: "exited", Service: "opencode", State: containertypes.StateExited, Created: 200},
			{ID: "running", Service: "opencode", State: containertypes.StateRunning, Created: 100},
		})

		if got.ID != "running" {
			t.Fatalf("got container %q, want %q", got.ID, "running")
		}
	})

	t.Run("returns zero value when no running opencode container exists", func(t *testing.T) {
		t.Parallel()

		got := currentOpencodeContainer([]api.ContainerSummary{
			{ID: "dind", Service: "dind", State: containertypes.StateRunning, Created: 100},
			{ID: "stopped", Service: "opencode", State: containertypes.StateExited, Created: 200},
		})

		if got.ID != "" {
			t.Fatalf("got container %q, want empty ID", got.ID)
		}
	})

	t.Run("preserves health field from selected container", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name   string
			health containertypes.HealthStatus
		}{
			{"healthy", containertypes.Healthy},
			{"unhealthy", containertypes.Unhealthy},
			{"no healthcheck", ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				got := currentOpencodeContainer([]api.ContainerSummary{
					{ID: "oc", Service: "opencode", State: containertypes.StateRunning, Created: 100, Health: tt.health},
				})

				if got.Health != tt.health {
					t.Fatalf("got health %q, want %q", got.Health, tt.health)
				}
			})
		}
	})
}

func TestAnyOpencodeContainer(t *testing.T) {
	t.Parallel()

	t.Run("prefers running over exited", func(t *testing.T) {
		t.Parallel()

		got := anyOpencodeContainer([]api.ContainerSummary{
			{ID: "exited", Service: "opencode", State: containertypes.StateExited, Created: 200, ExitCode: 1},
			{ID: "running", Service: "opencode", State: containertypes.StateRunning, Created: 100},
		})

		if got.ID != "running" {
			t.Fatalf("got container %q, want %q", got.ID, "running")
		}
	})

	t.Run("returns exited container when no running exists", func(t *testing.T) {
		t.Parallel()

		got := anyOpencodeContainer([]api.ContainerSummary{
			{ID: "dind", Service: "dind", State: containertypes.StateRunning, Created: 300},
			{ID: "exited", Service: "opencode", State: containertypes.StateExited, Created: 200, ExitCode: 1},
		})

		if got.ID != "exited" {
			t.Fatalf("got container %q, want %q", got.ID, "exited")
		}
		if got.ExitCode != 1 {
			t.Fatalf("got exit code %d, want 1", got.ExitCode)
		}
	})

	t.Run("selects newest among same state", func(t *testing.T) {
		t.Parallel()

		got := anyOpencodeContainer([]api.ContainerSummary{
			{ID: "old-exit", Service: "opencode", State: containertypes.StateExited, Created: 100},
			{ID: "new-exit", Service: "opencode", State: containertypes.StateExited, Created: 200},
		})

		if got.ID != "new-exit" {
			t.Fatalf("got container %q, want %q", got.ID, "new-exit")
		}
	})

	t.Run("returns zero value when no opencode container exists", func(t *testing.T) {
		t.Parallel()

		got := anyOpencodeContainer([]api.ContainerSummary{
			{ID: "dind", Service: "dind", State: containertypes.StateRunning, Created: 100},
		})

		if got.ID != "" {
			t.Fatalf("got container %q, want empty ID", got.ID)
		}
	})
}

func TestBuildPresetImageEmptyContent(t *testing.T) {
	t.Parallel()
	_, err := buildPresetImage(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error for nil content")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected error message: got %q, want message containing \"empty\"", err.Error())
	}
	_, err = buildPresetImage(context.Background(), nil, []byte{})
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected error message: got %q, want message containing \"empty\"", err.Error())
	}
}

func TestResolveBaseImageDockerfilePrecedence(t *testing.T) {
	t.Parallel()

	tempFile, err := os.CreateTemp("", "jailoc-empty-dockerfile-*")
	if err != nil {
		t.Fatalf("create temp dockerfile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tempFile.Name()) }) //nolint:gosec // cleaning up temp file created in this test
	if err := tempFile.Close(); err != nil {
		t.Fatalf("close temp dockerfile: %v", err)
	}

	cfg := &config.Config{
		Base: config.BaseConfig{
			Dockerfile: tempFile.Name(),
		},
	}

	_, err = ResolveBaseImage(context.Background(), cfg, "v0.0.0-test")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "build preset image") {
		t.Fatalf("unexpected error message: got %q, want message containing %q", err.Error(), "build preset image")
	}
	if !strings.Contains(err.Error(), "dockerfile content is empty") {
		t.Fatalf("unexpected error message: got %q, want message containing %q", err.Error(), "dockerfile content is empty")
	}
}

func TestResolveBaseImageDockerfileLoadError(t *testing.T) {
	t.Parallel()

	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist.Dockerfile")
	cfg := &config.Config{
		Base: config.BaseConfig{Dockerfile: nonExistentPath},
	}

	_, err := ResolveBaseImage(context.Background(), cfg, "v0.0.0-test")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), nonExistentPath) {
		t.Fatalf("unexpected error message: got %q, want message containing path %q", err.Error(), nonExistentPath)
	}
	if !strings.Contains(err.Error(), "load dockerfile") {
		t.Fatalf("unexpected error message: got %q, want message containing %q", err.Error(), "load dockerfile")
	}
}

func TestBuildOverlayImageNoDockerfile(t *testing.T) {
	t.Parallel()

	base := "registry.example.com/base:v1"
	tag, err := BuildOverlayImage(context.Background(), base, workspace.Resolved{
		Name:       "ws-a",
		Dockerfile: "",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if tag != base {
		t.Fatalf("unexpected tag: got %q, want %q", tag, base)
	}
}

func TestBuildOverlayImageEmptyBase(t *testing.T) {
	t.Parallel()

	wsDockerfile := filepath.Join(t.TempDir(), "overlay.Dockerfile")
	if err := os.WriteFile(wsDockerfile, []byte("ARG BASE\nFROM ${BASE}\n"), 0o600); err != nil {
		t.Fatalf("write workspace dockerfile: %v", err)
	}

	_, err := BuildOverlayImage(context.Background(), "", workspace.Resolved{
		Name:       "ws-a",
		Dockerfile: wsDockerfile,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "base image is empty") {
		t.Fatalf("unexpected error: got %q, want substring %q", err.Error(), "base image is empty")
	}
}

func TestBuildOverlayImageDockerfileLoadError(t *testing.T) {
	t.Parallel()

	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist.Dockerfile")
	_, err := BuildOverlayImage(context.Background(), "registry.example.com/base:v1", workspace.Resolved{
		Name:       "ws-a",
		Dockerfile: nonExistentPath,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "load workspace dockerfile") {
		t.Fatalf("unexpected error: got %q, want substring %q", err.Error(), "load workspace dockerfile")
	}
	if !strings.Contains(err.Error(), nonExistentPath) {
		t.Fatalf("unexpected error: got %q, want path %q", err.Error(), nonExistentPath)
	}
}
