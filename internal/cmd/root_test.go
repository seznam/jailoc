package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTargetPath(t *testing.T) {
	t.Run("no args returns current working directory", func(t *testing.T) {
		t.Parallel()

		got, err := resolveTargetPath([]string{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cwd, _ := os.Getwd()
		if got != cwd {
			t.Errorf("got %q, want CWD %q", got, cwd)
		}
	})

	t.Run("absolute path to existing directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		got, err := resolveTargetPath([]string{dir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != dir {
			t.Errorf("got %q, want %q", got, dir)
		}
	})

	t.Run("relative path resolves to absolute", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		rel, err := filepath.Rel(".", dir)
		if err != nil {
			// On some systems, TempDir may not be relative to CWD; skip
			t.Skipf("cannot compute relative path: %v", err)
		}

		got, err := resolveTargetPath([]string{rel})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !filepath.IsAbs(got) {
			t.Errorf("expected absolute path, got %q", got)
		}
	})

	t.Run("nonexistent path returns error", func(t *testing.T) {
		t.Parallel()

		_, err := resolveTargetPath([]string{"/nonexistent/path/that/does/not/exist"})
		if err == nil {
			t.Fatal("expected error for nonexistent path, got nil")
		}

		assertContains(t, err.Error(), "does not exist")
	})

	t.Run("file path returns error", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		file := filepath.Join(dir, "somefile.txt")
		if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
			t.Fatalf("create test file: %v", err)
		}

		_, err := resolveTargetPath([]string{file})
		if err == nil {
			t.Fatal("expected error for file path, got nil")
		}

		assertContains(t, err.Error(), "not a directory")
	})

	t.Run("tilde path expands home directory", func(t *testing.T) {
		t.Parallel()

		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot get home dir: %v", err)
		}

		got, err := resolveTargetPath([]string{"~"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != home {
			t.Errorf("got %q, want home %q", got, home)
		}
	})
}

func TestIsBroadPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot get home dir: %v", err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "root path", path: "/", want: true},
		{name: "home directory", path: home, want: true},
		{name: "normal project path", path: "/home/user/projects/myapp", want: false},
		{name: "subdirectory of home", path: filepath.Join(home, "projects"), want: false},
		{name: "empty path", path: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := isBroadPath(tc.path)
			if got != tc.want {
				t.Errorf("isBroadPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}
