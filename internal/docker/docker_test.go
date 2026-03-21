package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestResolveImageConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	overridePath := baseDockerfileOverridePath()
	want := filepath.Join(home, ".config", "jailoc", "Dockerfile")
	if overridePath != want {
		t.Fatalf("unexpected base dockerfile path: got %q, want %q", overridePath, want)
	}

	exists, err := fileExists(overridePath)
	if err != nil {
		t.Fatalf("fileExists returned error: %v", err)
	}
	if exists {
		t.Fatalf("expected no file at %q", overridePath)
	}

	configDir := filepath.Dir(overridePath)
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir %q: %v", configDir, err)
	}

	if err := os.WriteFile(overridePath, []byte("FROM alpine:3.22\n"), 0o600); err != nil {
		t.Fatalf("create override dockerfile %q: %v", overridePath, err)
	}

	exists, err = fileExists(overridePath)
	if err != nil {
		t.Fatalf("fileExists returned error: %v", err)
	}
	if !exists {
		t.Fatalf("expected file at %q", overridePath)
	}
}

func TestApplyWorkspaceLayerInputValidation(t *testing.T) {
	t.Run("empty base image", func(t *testing.T) {
		ctx := context.Background()
		_, err := ApplyWorkspaceLayer(ctx, "", "workspace")
		if err == nil {
			t.Fatal("expected error for empty base image")
		}
		if err.Error() != "base image is empty" {
			t.Fatalf("unexpected error: got %q, want %q", err.Error(), "base image is empty")
		}
	})

	t.Run("empty workspace name", func(t *testing.T) {
		ctx := context.Background()
		_, err := ApplyWorkspaceLayer(ctx, "image:tag", "")
		if err == nil {
			t.Fatal("expected error for empty workspace name")
		}
		if err.Error() != "workspace name is empty" {
			t.Fatalf("unexpected error: got %q, want %q", err.Error(), "workspace name is empty")
		}
	})

	t.Run("whitespace-only base image", func(t *testing.T) {
		ctx := context.Background()
		_, err := ApplyWorkspaceLayer(ctx, "   ", "workspace")
		if err == nil {
			t.Fatal("expected error for whitespace-only base image")
		}
		if err.Error() != "base image is empty" {
			t.Fatalf("unexpected error: got %q, want %q", err.Error(), "base image is empty")
		}
	})

	t.Run("whitespace-only workspace name", func(t *testing.T) {
		ctx := context.Background()
		_, err := ApplyWorkspaceLayer(ctx, "image:tag", "   ")
		if err == nil {
			t.Fatal("expected error for whitespace-only workspace name")
		}
		if err.Error() != "workspace name is empty" {
			t.Fatalf("unexpected error: got %q, want %q", err.Error(), "workspace name is empty")
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
