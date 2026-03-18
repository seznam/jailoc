package docker

import (
	"bytes"
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir %q: %v", configDir, err)
	}

	if err := os.WriteFile(overridePath, []byte("FROM alpine:3.22\n"), 0o644); err != nil {
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
