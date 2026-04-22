package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAttachHostArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		serverURL string
		password  string
		dir       string
		want      []string
	}{
		{
			name:      "no dir no password",
			serverURL: "http://localhost:4096",
			password:  "",
			dir:       "",
			want:      []string{"attach", "http://localhost:4096"},
		},
		{
			name:      "dir only",
			serverURL: "http://localhost:4096",
			password:  "",
			dir:       "/home/user/project/sub",
			want:      []string{"attach", "http://localhost:4096", "--dir", "/home/user/project/sub"},
		},
		{
			name:      "password only",
			serverURL: "http://localhost:4096",
			password:  "secret",
			dir:       "",
			want:      []string{"attach", "http://localhost:4096", "--password", "secret"},
		},
		{
			name:      "both dir and password",
			serverURL: "http://localhost:4096",
			password:  "secret",
			dir:       "/path",
			want:      []string{"attach", "http://localhost:4096", "--password", "secret", "--dir", "/path"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := attachHostArgs(tt.serverURL, tt.password, tt.dir)
			if !slicesEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAttachExecArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		serverURL string
		dir       string
		want      []string
	}{
		{
			name:      "no dir",
			serverURL: "http://localhost:4096",
			dir:       "",
			want:      []string{"opencode", "attach", "http://localhost:4096"},
		},
		{
			name:      "with dir",
			serverURL: "http://localhost:4096",
			dir:       "/home/user/project",
			want:      []string{"opencode", "attach", "http://localhost:4096", "--dir", "/home/user/project"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := attachExecArgs(tt.serverURL, tt.dir)
			if !slicesEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	return reflect.DeepEqual(a, b)
}

func TestMonitorAttach(t *testing.T) {
	t.Parallel()

	noHealth := func(context.Context) (string, error) { return "", nil }

	t.Run("cancels when container stops", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "", nil
		}, noHealth, "original", time.Millisecond)

		cause := waitForCause(t, ctx)
		if cause == nil || !strings.Contains(cause.Error(), "stopped") {
			t.Fatalf("got cause %v, want stopped error", cause)
		}
	})

	t.Run("cancels when container restarts", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "replacement", nil
		}, noHealth, "original", time.Millisecond)

		cause := waitForCause(t, ctx)
		if cause == nil || !strings.Contains(cause.Error(), "restarted") {
			t.Fatalf("got cause %v, want restarted error", cause)
		}
	})

	t.Run("cancels when container lookup fails", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "", errors.New("boom")
		}, noHealth, "original", time.Millisecond)

		cause := waitForCause(t, ctx)
		if cause == nil || !strings.Contains(cause.Error(), "monitor opencode container") {
			t.Fatalf("got cause %v, want monitor error", cause)
		}
	})

	t.Run("cancels when unhealthy", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "original", nil
		}, func(context.Context) (string, error) {
			return "unhealthy", nil
		}, "original", time.Millisecond)

		cause := waitForCause(t, ctx)
		if !errors.Is(cause, errUnhealthy) {
			t.Fatalf("got cause %v, want errUnhealthy", cause)
		}
	})

	t.Run("continues when healthy", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "original", nil
		}, func(context.Context) (string, error) {
			return "healthy", nil
		}, "original", time.Millisecond)

		assertNotCanceled(t, ctx, 50*time.Millisecond)
	})

	t.Run("continues when no healthcheck", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "original", nil
		}, noHealth, "original", time.Millisecond)

		assertNotCanceled(t, ctx, 50*time.Millisecond)
	})

	t.Run("continues when starting", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "original", nil
		}, func(context.Context) (string, error) {
			return "starting", nil
		}, "original", time.Millisecond)

		assertNotCanceled(t, ctx, 50*time.Millisecond)
	})

	t.Run("ignores transient health errors", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "original", nil
		}, func(context.Context) (string, error) {
			return "", errors.New("transient failure")
		}, "original", time.Millisecond)

		assertNotCanceled(t, ctx, 50*time.Millisecond)
	})
}

func TestAttachResultPrefersWatchdogCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("opencode container restarted during attach"))

	err := attachResult(ctx, nil)
	if err == nil {
		t.Fatal("expected watchdog cause, got nil")
	}
	if !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("got error %q, want restarted cause", err)
	}
}

func TestRunCommandWithContextReturnsExitError(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", "exit 7") //nolint:gosec // static test command
	err := runCommandWithContext(context.Background(), cmd, nil, 0)
	if err == nil {
		t.Fatal("expected exit error, got nil")
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("got %T, want *exec.ExitError", err)
	}
}

func TestRunCommandWithContextCancelsCommand(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command("sh", "-c", "sleep 10") //nolint:gosec // static test command
	terminate := func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := runCommandWithContext(ctx, cmd, terminate, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !strings.Contains(err.Error(), "signal: terminated") {
		t.Fatalf("got error %q, want terminated signal", err)
	}
}

func waitForCause(t *testing.T, ctx context.Context) error {
	t.Helper()

	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for context cancellation")
		return nil
	}
}

func assertNotCanceled(t *testing.T, ctx context.Context, wait time.Duration) {
	t.Helper()

	select {
	case <-ctx.Done():
		t.Fatalf("context was canceled unexpectedly: %v", context.Cause(ctx))
	case <-time.After(wait):
	}
}

func TestHostTUIConfigEnv(t *testing.T) {
	tests := []struct {
		name         string
		createConfig bool
		createUser   bool
		existingEnv  string
		wantEnv      bool
	}{
		{
			name:         "returns env when generated config exists and user tui json does not",
			createConfig: true,
			createUser:   false,
			wantEnv:      true,
		},
		{
			name:         "returns nil when user tui json exists",
			createConfig: true,
			createUser:   true,
			wantEnv:      false,
		},
		{
			name:         "returns nil when generated config does not exist",
			createConfig: false,
			createUser:   false,
			wantEnv:      false,
		},
		{
			name:         "returns nil when env already set",
			createConfig: true,
			createUser:   false,
			existingEnv:  "/custom/tui.json",
			wantEnv:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tmp)
			t.Setenv("HOME", tmp)

			configPath := filepath.Join(tmp, "generated", "tui.json")

			if tt.createConfig {
				if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
					t.Fatalf("create config dir: %v", err)
				}
				if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
					t.Fatalf("create generated tui.json: %v", err)
				}
			}

			if tt.createUser {
				ocDir, err := openCodeConfigDir()
				if err != nil {
					t.Fatalf("opencode config dir: %v", err)
				}
				userTUI := filepath.Join(ocDir, "tui.json")
				if err := os.MkdirAll(filepath.Dir(userTUI), 0o750); err != nil {
					t.Fatalf("create dir: %v", err)
				}
				if err := os.WriteFile(userTUI, []byte("{}"), 0o600); err != nil {
					t.Fatalf("create tui.json: %v", err)
				}
			}

			if tt.existingEnv != "" {
				t.Setenv("OPENCODE_TUI_CONFIG", tt.existingEnv)
			}

			got := hostTUIConfigEnv(configPath)
			var want []string
			if tt.wantEnv {
				want = []string{"OPENCODE_TUI_CONFIG=" + configPath}
			}
			if !slicesEqual(got, want) {
				t.Errorf("hostTUIConfigEnv(%q) = %v, want %v", configPath, got, want)
			}
		})
	}
}

func TestExecTUIConfigEnv(t *testing.T) {
	t.Setenv("OPENCODE_TUI_CONFIG", "/host/custom.json")

	got := execTUIConfigEnv("/etc/jailoc-tui.json")
	want := []string{"OPENCODE_TUI_CONFIG=/etc/jailoc-tui.json"}
	if !slicesEqual(got, want) {
		t.Fatalf("execTUIConfigEnv() = %v, want %v", got, want)
	}
}

func TestEnvWithOverrides(t *testing.T) {
	t.Parallel()

	base := []string{
		"PATH=/usr/bin",
		"JAILOC=old",
		"JAILOC_WORKSPACE=stale",
		"OTHER=value",
	}

	got := envWithOverrides(
		base,
		"JAILOC=1",
		"JAILOC_WORKSPACE=default",
		"OPENCODE_TUI_CONFIG=/tmp/tui.json",
	)

	want := []string{
		"PATH=/usr/bin",
		"OTHER=value",
		"JAILOC=1",
		"JAILOC_WORKSPACE=default",
		"OPENCODE_TUI_CONFIG=/tmp/tui.json",
	}

	if !slicesEqual(got, want) {
		t.Fatalf("envWithOverrides() = %v, want %v", got, want)
	}

	if countEnvKey(got, "JAILOC") != 1 {
		t.Fatalf("expected one JAILOC entry, got %v", got)
	}
	if countEnvKey(got, "JAILOC_WORKSPACE") != 1 {
		t.Fatalf("expected one JAILOC_WORKSPACE entry, got %v", got)
	}
}

func countEnvKey(env []string, key string) int {
	count := 0
	for _, entry := range env {
		entryKey, _, ok := strings.Cut(entry, "=")
		if ok && entryKey == key {
			count++
		}
	}
	return count
}
