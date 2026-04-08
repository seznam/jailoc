package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/seznam/jailoc/internal/embed"
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

func TestResolveSSHAuthSock(t *testing.T) {
	t.Run("disabled returns empty", func(t *testing.T) {
		got := resolveSSHAuthSock(false)
		if got != "" {
			t.Fatalf("expected empty when disabled, got %q", got)
		}
	})

	t.Run("docker desktop magic socket found", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })
		osStat = func(name string) (os.FileInfo, error) {
			if name == dockerDesktopSSHSock {
				return nil, nil
			}
			return nil, os.ErrNotExist
		}

		got := resolveSSHAuthSock(true)
		if got != dockerDesktopSSHSock {
			t.Fatalf("expected %q, got %q", dockerDesktopSSHSock, got)
		}
	})

	t.Run("falls back to SSH_AUTH_SOCK env", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })

		fakeSocket := "/tmp/fake-ssh-agent.sock"
		osStat = func(name string) (os.FileInfo, error) {
			if name == fakeSocket {
				return nil, nil
			}
			return nil, os.ErrNotExist
		}
		t.Setenv("SSH_AUTH_SOCK", fakeSocket)

		got := resolveSSHAuthSock(true)
		if got != fakeSocket {
			t.Fatalf("expected %q, got %q", fakeSocket, got)
		}
	})

	t.Run("returns empty when nothing found", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })
		osStat = func(name string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}
		t.Setenv("SSH_AUTH_SOCK", "")

		got := resolveSSHAuthSock(true)
		if got != "" {
			t.Fatalf("expected empty when no socket found, got %q", got)
		}
	})

	t.Run("returns empty when SSH_AUTH_SOCK env points to nonexistent file", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })
		osStat = func(name string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}
		t.Setenv("SSH_AUTH_SOCK", "/nonexistent/agent.sock")

		got := resolveSSHAuthSock(true)
		if got != "" {
			t.Fatalf("expected empty when socket file missing, got %q", got)
		}
	})
}

func TestResolveGitConfig(t *testing.T) {
	t.Run("disabled returns empty", func(t *testing.T) {
		got := resolveGitConfig(false)
		if got != "" {
			t.Fatalf("expected empty when disabled, got %q", got)
		}
	})

	t.Run("finds home gitconfig", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })

		home := t.TempDir()
		t.Setenv("HOME", home)
		expected := filepath.Join(home, ".gitconfig")

		osStat = func(name string) (os.FileInfo, error) {
			if name == expected {
				return nil, nil
			}
			return nil, os.ErrNotExist
		}

		got := resolveGitConfig(true)
		if got != expected {
			t.Fatalf("expected %q, got %q", expected, got)
		}
	})

	t.Run("falls back to XDG git config", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })

		home := t.TempDir()
		t.Setenv("HOME", home)
		expected := filepath.Join(home, ".config", "git", "config")

		osStat = func(name string) (os.FileInfo, error) {
			if name == expected {
				return nil, nil
			}
			return nil, os.ErrNotExist
		}

		got := resolveGitConfig(true)
		if got != expected {
			t.Fatalf("expected %q, got %q", expected, got)
		}
	})

	t.Run("returns empty when nothing found", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })
		osStat = func(name string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}
		t.Setenv("HOME", t.TempDir())

		got := resolveGitConfig(true)
		if got != "" {
			t.Fatalf("expected empty when no gitconfig found, got %q", got)
		}
	})
}

func TestResolveSSHKnownHosts(t *testing.T) {
	t.Run("disabled returns empty", func(t *testing.T) {
		got := resolveSSHKnownHosts(false)
		if got != "" {
			t.Fatalf("expected empty when disabled, got %q", got)
		}
	})

	t.Run("finds known_hosts", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })

		home := t.TempDir()
		t.Setenv("HOME", home)
		expected := filepath.Join(home, ".ssh", "known_hosts")

		osStat = func(name string) (os.FileInfo, error) {
			if name == expected {
				return nil, nil
			}
			return nil, os.ErrNotExist
		}

		got := resolveSSHKnownHosts(true)
		if got != expected {
			t.Fatalf("expected %q, got %q", expected, got)
		}
	})

	t.Run("returns empty when not found", func(t *testing.T) {
		orig := osStat
		t.Cleanup(func() { osStat = orig })
		osStat = func(name string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}
		t.Setenv("HOME", t.TempDir())

		got := resolveSSHKnownHosts(true)
		if got != "" {
			t.Fatalf("expected empty when no known_hosts found, got %q", got)
		}
	})
}

func TestWriteEntrypointToCache(t *testing.T) {
	t.Parallel()

	t.Run("creates executable file with correct content", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		if err := writeEntrypoint(tmpDir); err != nil {
			t.Fatalf("writeEntrypoint failed: %v", err)
		}

		entrypointPath := filepath.Join(tmpDir, "entrypoint.sh")
		info, err := os.Stat(entrypointPath)
		if err != nil {
			t.Fatalf("os.Stat(%q) failed: %v", entrypointPath, err)
		}

		if info.Mode()&0o111 == 0 {
			t.Fatalf("entrypoint.sh permissions %o should have at least one executable bit set", info.Mode()&0o777)
		}

		data, err := os.ReadFile(entrypointPath) //nolint:gosec // path constructed from t.TempDir(), fully controlled
		if err != nil {
			t.Fatalf("os.ReadFile(%q) failed: %v", entrypointPath, err)
		}
		if !bytes.Equal(data, embed.Entrypoint()) {
			t.Fatalf("entrypoint.sh content does not match embedded asset")
		}
	})

	t.Run("fixes permissions on existing file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		entrypointPath := filepath.Join(tmpDir, "entrypoint.sh")
		if err := os.WriteFile(entrypointPath, []byte("old"), 0o600); err != nil { //nolint:gosec // test setup
			t.Fatalf("setup WriteFile failed: %v", err)
		}

		if err := writeEntrypoint(tmpDir); err != nil {
			t.Fatalf("writeEntrypoint failed: %v", err)
		}

		info, err := os.Stat(entrypointPath)
		if err != nil {
			t.Fatalf("os.Stat(%q) failed: %v", entrypointPath, err)
		}

		if info.Mode().Perm() != 0o755 {
			t.Fatalf("expected permissions 0o755, got %o", info.Mode().Perm())
		}
	})
}
