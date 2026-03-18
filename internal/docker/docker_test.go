package docker

import (
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

func TestIsRunningParsing(t *testing.T) {
	t.Parallel()

	t.Run("running service returns true", func(t *testing.T) {
		t.Parallel()

		input := []byte("{\"Service\":\"opencode\",\"State\":\"running\"}\n")

		got, err := parseServiceState(input, "opencode")
		if err != nil {
			t.Fatalf("parseServiceState returned error: %v", err)
		}
		if !got {
			t.Fatal("expected running=true")
		}
	})

	t.Run("different service returns false", func(t *testing.T) {
		t.Parallel()

		input := []byte("{\"Service\":\"other\",\"State\":\"running\"}\n")

		got, err := parseServiceState(input, "opencode")
		if err != nil {
			t.Fatalf("parseServiceState returned error: %v", err)
		}
		if got {
			t.Fatal("expected running=false")
		}
	})

	t.Run("not running returns false", func(t *testing.T) {
		t.Parallel()

		input := []byte("{\"Service\":\"opencode\",\"State\":\"exited\"}\n")

		got, err := parseServiceState(input, "opencode")
		if err != nil {
			t.Fatalf("parseServiceState returned error: %v", err)
		}
		if got {
			t.Fatal("expected running=false")
		}
	})

	t.Run("empty output returns false", func(t *testing.T) {
		t.Parallel()

		got, err := parseServiceState(nil, "opencode")
		if err != nil {
			t.Fatalf("parseServiceState returned error: %v", err)
		}
		if got {
			t.Fatal("expected running=false")
		}
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		t.Parallel()

		_, err := parseServiceState([]byte("{not-json}\n"), "opencode")
		if err == nil {
			t.Fatal("expected parsing error")
		}
	})

	t.Run("ndjson checks all lines", func(t *testing.T) {
		t.Parallel()

		input := []byte("{\"Service\":\"db\",\"State\":\"running\"}\n{\"Service\":\"opencode\",\"State\":\"running\"}\n")

		got, err := parseServiceState(input, "opencode")
		if err != nil {
			t.Fatalf("parseServiceState returned error: %v", err)
		}
		if !got {
			t.Fatal("expected running=true")
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
