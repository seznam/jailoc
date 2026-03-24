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

func TestResolveImageStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		wsImage       string
		defaultsImage string
		wsDockerfile  string
		wantStrategy  imageStrategy
		wantImage     string
	}{
		{
			name:          "workspace image only uses direct image",
			wsImage:       "foo:1",
			defaultsImage: "",
			wsDockerfile:  "",
			wantStrategy:  strategyDirectImage,
			wantImage:     "foo:1",
		},
		{
			name:          "workspace image wins over defaults and dockerfile",
			wsImage:       "foo:1",
			defaultsImage: "base:2",
			wsDockerfile:  "/df",
			wantStrategy:  strategyDirectImage,
			wantImage:     "foo:1",
		},
		{
			name:          "defaults image without dockerfile uses direct defaults",
			wsImage:       "",
			defaultsImage: "base:2",
			wsDockerfile:  "",
			wantStrategy:  strategyDefaultsDirect,
			wantImage:     "base:2",
		},
		{
			name:          "defaults image with dockerfile uses defaults overlay",
			wsImage:       "",
			defaultsImage: "base:2",
			wsDockerfile:  "/path/Dockerfile",
			wantStrategy:  strategyDefaultsOverlay,
			wantImage:     "base:2",
		},
		{
			name:          "no images and no dockerfile uses cascade",
			wsImage:       "",
			defaultsImage: "",
			wsDockerfile:  "",
			wantStrategy:  strategyCascade,
			wantImage:     "",
		},
		{
			name:          "dockerfile without images still uses cascade",
			wsImage:       "",
			defaultsImage: "",
			wsDockerfile:  "/path",
			wantStrategy:  strategyCascade,
			wantImage:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotStrategy, gotImage := resolveImageStrategy(tc.wsImage, tc.defaultsImage, tc.wsDockerfile)

			if gotStrategy != tc.wantStrategy {
				t.Fatalf("resolveImageStrategy(%q, %q, %q) strategy = %v, want %v", tc.wsImage, tc.defaultsImage, tc.wsDockerfile, gotStrategy, tc.wantStrategy)
			}

			if gotImage != tc.wantImage {
				t.Fatalf("resolveImageStrategy(%q, %q, %q) image = %q, want %q", tc.wsImage, tc.defaultsImage, tc.wsDockerfile, gotImage, tc.wantImage)
			}
		})
	}
}
