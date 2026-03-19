package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComposeCacheDir(t *testing.T) {
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

			got := composeCacheDir(tc.workspace)

			if got != tc.wantPath {
				t.Fatalf("composeCacheDir(%q) = %q, want %q", tc.workspace, got, tc.wantPath)
			}

			if !hasTrailingSeparator(got) {
				t.Fatalf("composeCacheDir(%q) should end with path separator, got %q", tc.workspace, got)
			}
		})
	}
}

func TestComposeCacheDirFallback(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/home/that/does/not/exist")

	got := composeCacheDir("fallback-test")

	if !filepath.IsAbs(got) {
		t.Fatalf("composeCacheDir should return absolute path, got %q", got)
	}

	if !hasTrailingSeparator(got) {
		t.Fatalf("composeCacheDir should end with path separator, got %q", got)
	}

	if !filepath.IsLocal(got) && !contains(got, "fallback-test") {
		t.Fatalf("composeCacheDir should contain workspace name, got %q", got)
	}
}

func hasTrailingSeparator(path string) bool {
	if len(path) == 0 {
		return false
	}
	return path[len(path)-1] == os.PathSeparator
}

func contains(s, substr string) bool {
	return filepath.Base(filepath.Dir(s)) == "jailoc" || len(substr) > 0 && s[len(s)-len(substr)-1:len(s)-1] == substr
}
