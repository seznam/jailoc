package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestNeedsMigration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		isRunning   bool
		hasPassword bool
		want        bool
	}{
		{name: "running_no_password", isRunning: true, hasPassword: false, want: true},
		{name: "running_has_password", isRunning: true, hasPassword: true, want: false},
		{name: "not_running_no_password", isRunning: false, hasPassword: false, want: false},
		{name: "not_running_has_password", isRunning: false, hasPassword: true, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := needsMigration(tc.isRunning, tc.hasPassword)
			if got != tc.want {
				t.Fatalf("needsMigration(%v, %v) = %v, want %v", tc.isRunning, tc.hasPassword, got, tc.want)
			}
		})
	}
}

func TestCheckPortConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ports      map[string]int
		targetName string
		targetPort int
		wantErr    bool
		errSubstr  string
	}{
		{
			name:       "no conflict",
			ports:      map[string]int{"alpha": 4096},
			targetName: "beta",
			targetPort: 4097,
			wantErr:    false,
		},
		{
			name:       "conflict with another workspace",
			ports:      map[string]int{"alpha": 4096},
			targetName: "beta",
			targetPort: 4096,
			wantErr:    true,
			errSubstr:  "alpha",
		},
		{
			name:       "same workspace is not a conflict",
			ports:      map[string]int{"alpha": 4096},
			targetName: "alpha",
			targetPort: 4096,
			wantErr:    false,
		},
		{
			name:       "empty map",
			ports:      map[string]int{},
			targetName: "alpha",
			targetPort: 4096,
			wantErr:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := checkPortConflict(tc.ports, tc.targetName, tc.targetPort)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error %q should contain %q", err.Error(), tc.errSubstr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestWriteTUIConfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	if err := writeTUIConfig(tmpDir); err != nil {
		t.Fatalf("writeTUIConfig failed: %v", err)
	}

	readPluginSpec := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("os.ReadFile(%q) failed: %v", path, err)
		}
		var config map[string][]string
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatalf("json.Unmarshal(%q) failed: %v", path, err)
		}
		plugins, ok := config["plugin"]
		if !ok {
			t.Fatalf("%s missing 'plugin' key", path)
		}
		if len(plugins) != 1 {
			t.Fatalf("%s: expected 1 plugin, got %d", path, len(plugins))
		}
		return plugins[0]
	}

	wantHost := "file://" + filepath.Join(tmpDir, "tui-plugin")
	if got := readPluginSpec(filepath.Join(tmpDir, "tui.json")); got != wantHost {
		t.Fatalf("host tui.json plugin = %q, want %q", got, wantHost)
	}

	wantContainer := "file:///etc/jailoc-tui-plugin"
	if got := readPluginSpec(filepath.Join(tmpDir, "tui-container.json")); got != wantContainer {
		t.Fatalf("container tui.json plugin = %q, want %q", got, wantContainer)
	}

	pkgJSON, err := os.ReadFile(filepath.Join(tmpDir, "tui-plugin", "package.json")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("reading tui-plugin/package.json: %v", err)
	}
	if !bytes.Equal(pkgJSON, embed.TUIPluginJSON()) {
		t.Fatalf("package.json content mismatch")
	}
	var pkg map[string]any
	if err := json.Unmarshal(pkgJSON, &pkg); err != nil {
		t.Fatalf("json.Unmarshal(package.json) failed: %v", err)
	}
	if got, ok := pkg["type"].(string); !ok || got != "module" {
		t.Fatalf("package.json type = %v, want %q", pkg["type"], "module")
	}

	tuiJS, err := os.ReadFile(filepath.Join(tmpDir, "tui-plugin", "tui.js")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("reading tui-plugin/tui.js: %v", err)
	}
	if !bytes.Equal(tuiJS, embed.TUIPluginJS()) {
		t.Fatalf("tui.js content mismatch")
	}
}

func TestEnsureOCConfigGitignore(t *testing.T) {
	t.Parallel()

	t.Run("creates directory and file when missing", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "opencode")
		if err := ensureOCConfigGitignore(dir); err != nil {
			t.Fatalf("ensureOCConfigGitignore() = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".gitignore")) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(data) != ocConfigGitignore {
			t.Fatalf("content = %q, want %q", data, ocConfigGitignore)
		}
	})

	t.Run("no-op when file already exists", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		existing := "existing content\n"
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644); err != nil { //nolint:gosec // test
			t.Fatalf("WriteFile: %v", err)
		}
		if err := ensureOCConfigGitignore(dir); err != nil {
			t.Fatalf("ensureOCConfigGitignore() = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".gitignore")) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(data) != existing {
			t.Fatalf("content changed: got %q, want %q", data, existing)
		}
	})
}
