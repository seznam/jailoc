package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestIsComposeFileMissing(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "error with no such file or directory",
			err:      errors.New("stat /path: no such file or directory"),
			expected: true,
		},
		{
			name:     "error with open and docker-compose.yml",
			err:      errors.New("open /home/.cache/jailoc/default/docker-compose.yml: permission denied"),
			expected: true,
		},
		{
			name:     "error with non-matching message",
			err:      errors.New("permission denied"),
			expected: false,
		},
		{
			name:     "wrapped error with no such file or directory",
			err:      fmt.Errorf("failed to load: %w", errors.New("stat /path: no such file or directory")),
			expected: true,
		},
		{
			name:     "wrapped error with open and docker-compose.yml",
			err:      fmt.Errorf("compose error: %w", errors.New("open /cache/docker-compose.yml: no such file")),
			expected: true,
		},
		{
			name:     "error with open but missing docker-compose.yml",
			err:      errors.New("open /home/config.toml: permission denied"),
			expected: false,
		},
		{
			name:     "error with docker-compose.yml but missing open",
			err:      errors.New("docker-compose.yml not found"),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isComposeFileMissing(tc.err)
			if result != tc.expected {
				t.Errorf("isComposeFileMissing(%v) = %v, want %v", tc.err, result, tc.expected)
			}
		})
	}
}

func TestExportedComposeCacheDir(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		homeEnv   string
		wantPath  string
	}{
		{
			name:      "normal workspace with HOME set",
			workspace: "default",
			homeEnv:   "/tmp/fakehome",
			wantPath:  filepath.Join("/tmp/fakehome", ".cache", "jailoc", "default") + string(os.PathSeparator),
		},
		{
			name:      "different workspace name",
			workspace: "api",
			homeEnv:   "/tmp/fakehome",
			wantPath:  filepath.Join("/tmp/fakehome", ".cache", "jailoc", "api") + string(os.PathSeparator),
		},
		{
			name:      "workspace with special chars",
			workspace: "my-app-prod",
			homeEnv:   "/tmp/fakehome",
			wantPath:  filepath.Join("/tmp/fakehome", ".cache", "jailoc", "my-app-prod") + string(os.PathSeparator),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.homeEnv)

			got := ComposeCacheDir(tc.workspace)

			if got != tc.wantPath {
				t.Fatalf("ComposeCacheDir(%q) = %q, want %q", tc.workspace, got, tc.wantPath)
			}

			if !hasTrailingSeparator(got) {
				t.Fatalf("ComposeCacheDir(%q) should end with path separator, got %q", tc.workspace, got)
			}
		})
	}
}

func TestExportedComposeCacheDirFallback(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/home/that/does/not/exist")

	got := ComposeCacheDir("fallback-test")

	if !filepath.IsAbs(got) {
		t.Fatalf("ComposeCacheDir should return absolute path, got %q", got)
	}

	if !hasTrailingSeparator(got) {
		t.Fatalf("ComposeCacheDir should end with path separator, got %q", got)
	}
}
