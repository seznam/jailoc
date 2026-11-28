package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
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

func TestValidateWorkspacePathRejectsBroadPaths(t *testing.T) {
	// Outside /tmp and /var, which workspace paths may not use.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(cwd, ".test-broad-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	for _, dir := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, "projects")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	homeLink := filepath.Join(base, "home-link")
	if err := os.Symlink(home, homeLink); err != nil {
		t.Fatalf("create home symlink: %v", err)
	}
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink("/", rootLink); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "root path", path: "/", wantErr: "/ is the host root filesystem"},
		{name: "home directory", path: home, wantErr: "it contains ~/.ssh"},
		{name: "symlink to home directory", path: homeLink, wantErr: "resolves to"},
		{name: "symlink to root path", path: rootLink, wantErr: "resolves to"},
		{name: "subdirectory of home", path: filepath.Join(home, "projects")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateWorkspacePath(tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("ValidateWorkspacePath(%q) = %v, want nil", tc.path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidateWorkspacePath(%q) = %v, want error containing %q", tc.path, err, tc.wantErr)
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

func TestExecuteUpdateCheck(t *testing.T) {
	// Suppress update check side effects. t.Setenv is incompatible with t.Parallel(),
	// so this test cannot be parallel.
	t.Setenv("JAILOC_NO_UPDATE_NOTIFIER", "1")

	// Drive Cobra down a controlled, side-effect-free path.
	rootCmd.SetArgs([]string{"--help"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	// Call Execute() with "dev" version (which also gates the update check).
	// This is a smoke test to verify the update check wiring in Execute() doesn't panic.
	err := Execute("dev", "test-commit", "test-date")
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
}

func TestIsSelfUpdateInvocation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"path arg", []string{"/tmp"}, false},
		{"other command", []string{"list"}, false},
		{"self-update", []string{"self-update"}, true},
		{"self-update check", []string{"self-update", "--check"}, true},
		{"self-update version value", []string{"self-update", "--version", "v1.2.3"}, true},
		{"root flag before", []string{"--no-color", "self-update"}, true},
		{"root flag with value before", []string{"-w", "foo", "self-update"}, true},
		{"config value before", []string{"--config", "/x/c.toml", "self-update", "--force"}, true},
		{"root version flag", []string{"--version"}, false},
		{"self-update as flag value", []string{"--attach", "self-update"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSelfUpdateInvocation(rootCmd, tt.args); got != tt.want {
				t.Errorf("isSelfUpdateInvocation(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
