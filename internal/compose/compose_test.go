package compose

import (
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
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "- /repos/api:/repos/api")
	assertContains(t, rendered, "- /repos/web-app:/repos/web-app")
}

func TestGenerateComposeEmptyPasswordRendersEmptyValue(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "gamma",
		Port:          4333,
		Image:         "ghcr.io/seznam/jailoc:dev",
		Paths:         []string{"/tmp/work"},
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

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected rendered compose to contain %q, got:\n%s", needle, haystack)
	}
}
