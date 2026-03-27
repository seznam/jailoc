package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateComposeSinglePath(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "alpha",
		Port:             4111,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/Users/test/work/project"},
		OpenCodePassword: "secret",
		Env:              nil,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "name: jailoc-alpha")
	assertContains(t, rendered, "image: ghcr.io/seznam/jailoc:test")
	assertContains(t, rendered, "- \"4111:4096\"")
	assertContains(t, rendered, "- /Users/test/work/project:/Users/test/work/project")
	assertContains(t, rendered, "- OPENCODE_SERVER_PASSWORD=secret")
	assertContains(t, rendered, "opencode-data-alpha")
	assertContains(t, rendered, "opencode-cache-alpha")
	assertContains(t, rendered, "working_dir: /Users/test/work/project")
	assertContains(t, rendered, "healthcheck:")
	assertContains(t, rendered, "$$OPENCODE_SERVER_PASSWORD")
	assertContains(t, rendered, "/global/health")
}

func TestGenerateComposeMultiplePaths(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "beta",
		Port:          4222,
		Image:         "ghcr.io/seznam/jailoc:latest",
		Paths: []string{
			"/repos/api",
			"/repos/web-app",
		},
		Env: nil,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "- /repos/api:/repos/api")
	assertContains(t, rendered, "- /repos/web-app:/repos/web-app")
	assertContains(t, rendered, "working_dir: /repos/api")
}

func TestGenerateComposeEmptyPasswordRendersEmptyValue(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "gamma",
		Port:          4333,
		Image:         "ghcr.io/seznam/jailoc:dev",
		Paths:         []string{"/tmp/work"},
		Env:           nil,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "- OPENCODE_SERVER_PASSWORD=")
	if strings.Contains(rendered, "${OPENCODE_SERVER_PASSWORD") {
		t.Fatalf("expected no shell interpolation for OPENCODE_SERVER_PASSWORD, got:\n%s", rendered)
	}
}

func TestGenerateComposeVolumeNamesIncludeWorkspaceName(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "delta",
		Port:          4444,
		Image:         "ghcr.io/seznam/jailoc:main",
		Paths:         []string{"/tmp/repo"},
		Env:           nil,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "- opencode-data-delta:/home/agent/.local/share/opencode")
	assertContains(t, rendered, "- opencode-cache-delta:/home/agent/.cache")
	assertContains(t, rendered, "opencode-data-delta:")
	assertContains(t, rendered, "opencode-cache-delta:")
}

func TestWriteComposeFileHappyPath(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "test-ws",
		Port:             4500,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/workspace"},
		OpenCodePassword: "testpass",
		Env:              nil,
	}

	destPath := filepath.Join(t.TempDir(), "docker-compose.yml")

	err := WriteComposeFile(params, destPath)
	if err != nil {
		t.Fatalf("WriteComposeFile returned error: %v", err)
	}

	content, err := os.ReadFile(destPath) //nolint:gosec
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if len(content) == 0 {
		t.Fatal("written compose file is empty")
	}

	rendered := string(content)
	assertContains(t, rendered, "name: jailoc-test-ws")
	assertContains(t, rendered, "image: ghcr.io/seznam/jailoc:test")
	assertContains(t, rendered, "- \"4500:4096\"")

	stat, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if stat.Mode().Perm() != 0o600 {
		t.Fatalf("expected file permissions 0o600, got %#o", stat.Mode().Perm())
	}
}

func TestWriteComposeFileErrorPath(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "test-ws",
		Port:          4500,
		Image:         "ghcr.io/seznam/jailoc:test",
		Paths:         []string{"/tmp/workspace"},
		Env:           nil,
	}

	destPath := "/nonexistent/directory/docker-compose.yml"

	err := WriteComposeFile(params, destPath)
	if err == nil {
		t.Fatal("expected WriteComposeFile to return error for invalid destination, got nil")
	}

	if !strings.Contains(err.Error(), "write compose file") {
		t.Fatalf("expected error message to contain 'write compose file', got: %v", err)
	}
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected rendered compose to contain %q, got:\n%s", needle, haystack)
	}
}

func TestGenerateComposeEnv(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "env-test",
		Port:             4600,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/work"},
		OpenCodePassword: "secret",
		Env:              []string{"MY_VAR=hello", "OTHER=world"},
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	// Check that user env vars are present (double-quoted in YAML)
	assertContains(t, rendered, `- "MY_VAR=hello"`)
	assertContains(t, rendered, `- "OTHER=world"`)

	// Check that system env vars are still present
	assertContains(t, rendered, "- OPENCODE_LOG=debug")
	assertContains(t, rendered, "- DOCKER_HOST=tcp://dind:2376")
	assertContains(t, rendered, "- DOCKER_TLS_VERIFY=1")
}

func TestGenerateComposeEmptyEnv(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "empty-env-test",
		Port:             4700,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/work"},
		OpenCodePassword: "secret",
		Env:              nil,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	// Check that system env vars are still present
	assertContains(t, rendered, "- OPENCODE_LOG=debug")
	assertContains(t, rendered, "- DOCKER_HOST=tcp://dind:2376")
	assertContains(t, rendered, "- DOCKER_TLS_VERIFY=1")

	// Verify no extra blank entries are added when Env is nil
	lines := strings.Split(rendered, "\n")
	environmentIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "environment:") {
			environmentIdx = i
			break
		}
	}
	if environmentIdx >= 0 {
		afterEnv := lines[environmentIdx+1]
		if !strings.HasPrefix(afterEnv, "      -") {
			t.Errorf("expected environment entry starting with '      -', got: %q", afterEnv)
		}
	}
}
