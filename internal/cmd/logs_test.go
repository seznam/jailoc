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

			got := logsComposePath(tc.workspaceName)

			if got != expectedPath {
				t.Errorf("logsComposePath(%q) = %q, want %q", tc.workspaceName, got, expectedPath)
			}
		})
	}
}

func TestLogsComposePathFallback(t *testing.T) {
	oldHome, homeWasSet := os.LookupEnv("HOME")
	t.Cleanup(func() {
		if homeWasSet {
			t.Setenv("HOME", oldHome)
		}
	})
	t.Setenv("HOME", "")

	got := logsComposePath("testworkspace")

	expectedPath := filepath.Join(os.TempDir(), "jailoc", "testworkspace", "docker-compose.yml")
	if got != expectedPath {
		t.Errorf("logsComposePath fallback = %q, want %q", got, expectedPath)
	}
}
