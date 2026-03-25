package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogsComposePath(t *testing.T) {
	tests := []struct {
		name          string
		workspaceName string
		setupHome     func(t *testing.T) string
	}{
		{
			name:          "normal workspace name with HOME set",
			workspaceName: "default",
			setupHome: func(t *testing.T) string {
				home := t.TempDir()
				t.Setenv("HOME", home)
				return home
			},
		},
		{
			name:          "different workspace name",
			workspaceName: "api",
			setupHome: func(t *testing.T) string {
				home := t.TempDir()
				t.Setenv("HOME", home)
				return home
			},
		},
		{
			name:          "workspace with special characters",
			workspaceName: "prod-eu-01",
			setupHome: func(t *testing.T) string {
				home := t.TempDir()
				t.Setenv("HOME", home)
				return home
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := tc.setupHome(t)
			expectedPath := filepath.Join(home, ".cache", "jailoc", tc.workspaceName, "docker-compose.yml")

			got := filepath.Join(ComposeCacheDir(tc.workspaceName), "docker-compose.yml")

			if got != expectedPath {
				t.Errorf("ComposeCacheDir(%q) compose path = %q, want %q", tc.workspaceName, got, expectedPath)
			}
		})
	}
}

func TestLogsComposePathFallback(t *testing.T) {
	t.Setenv("HOME", "")

	got := filepath.Join(ComposeCacheDir("testworkspace"), "docker-compose.yml")

	expectedPath := filepath.Join(os.TempDir(), "jailoc", "testworkspace", "docker-compose.yml")
	if got != expectedPath {
		t.Errorf("ComposeCacheDir fallback compose path = %q, want %q", got, expectedPath)
	}
}
