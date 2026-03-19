package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsDuplicate(t *testing.T) {
	tests := []struct {
		name   string
		paths  []string
		target string
		want   bool
	}{
		{
			name:   "empty slice",
			paths:  []string{},
			target: "/path/to/something",
			want:   false,
		},
		{
			name:   "single element exact match",
			paths:  []string{"/home/user/projects"},
			target: "/home/user/projects",
			want:   true,
		},
		{
			name:   "single element no match",
			paths:  []string{"/home/user/projects"},
			target: "/home/user/other",
			want:   false,
		},
		{
			name:   "multiple elements match in middle",
			paths:  []string{"/path/a", "/path/b", "/path/c"},
			target: "/path/b",
			want:   true,
		},
		{
			name:   "multiple elements no match",
			paths:  []string{"/path/a", "/path/b", "/path/c"},
			target: "/path/d",
			want:   false,
		},
		{
			name:   "case sensitivity different",
			paths:  []string{"Foo"},
			target: "foo",
			want:   false,
		},
		{
			name:   "case sensitivity same",
			paths:  []string{"foo"},
			target: "foo",
			want:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isDuplicate(tc.paths, tc.target)
			if got != tc.want {
				t.Errorf("isDuplicate(%#v, %q) = %v, want %v", tc.paths, tc.target, got, tc.want)
			}
		})
	}
}

func TestResolveTargetDir(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError bool
		checkPath func(t *testing.T, path string)
	}{
		{
			name:      "valid existing path as argument",
			wantError: false,
			checkPath: func(t *testing.T, path string) {
				if !filepath.IsAbs(path) {
					t.Fatalf("expected absolute path, got %q", path)
				}
			},
		},
		{
			name:      "nonexistent path",
			args:      []string{"/nonexistent/path/that/does/not/exist"},
			wantError: true,
		},
		{
			name:      "no arguments uses current working directory",
			args:      []string{},
			wantError: false,
			checkPath: func(t *testing.T, path string) {
				if !filepath.IsAbs(path) {
					t.Fatalf("expected absolute path, got %q", path)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var args []string

			switch tc.name {
			case "valid existing path as argument":
				tmpDir := t.TempDir()
				args = []string{tmpDir}
			case "nonexistent path", "no arguments uses current working directory":
				args = tc.args
			}

			got, err := resolveTargetDir(args)
			if (err != nil) != tc.wantError {
				t.Fatalf("resolveTargetDir(%v) error = %v, wantError %v", args, err, tc.wantError)
			}

			if !tc.wantError && tc.checkPath != nil {
				tc.checkPath(t, got)
			}
		})
	}
}

func TestAddComposePath(t *testing.T) {
	tests := []struct {
		name          string
		workspace     string
		expectedRegex func(home string) string
	}{
		{
			name:      "default workspace",
			workspace: "default",
			expectedRegex: func(home string) string {
				return filepath.Join(home, ".cache", "jailoc", "default", "docker-compose.yml")
			},
		},
		{
			name:      "api workspace",
			workspace: "api",
			expectedRegex: func(home string) string {
				return filepath.Join(home, ".cache", "jailoc", "api", "docker-compose.yml")
			},
		},
		{
			name:      "custom workspace name",
			workspace: "my-production-env",
			expectedRegex: func(home string) string {
				return filepath.Join(home, ".cache", "jailoc", "my-production-env", "docker-compose.yml")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			got := addComposePath(tc.workspace)
			expected := tc.expectedRegex(home)

			if got != expected {
				t.Errorf("addComposePath(%q) = %q, want %q", tc.workspace, got, expected)
			}

			if !filepath.IsAbs(got) {
				t.Errorf("expected absolute path, got %q", got)
			}
		})
	}

	t.Run("fallback to TempDir when UserHomeDir fails", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", "")

		got := addComposePath("test-workspace")

		if !filepath.IsAbs(got) {
			t.Fatalf("expected absolute path, got %q", got)
		}

		if !strings.HasPrefix(got, os.TempDir()) {
			t.Errorf("expected path to start with TempDir %q, got %q", os.TempDir(), got)
		}

		_ = home
	})
}
