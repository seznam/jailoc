package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		dir       string
		session   string
		cont      bool
		want      []string
	}{
		{
			name:      "no dir",
			serverURL: "http://localhost:4096",
			want:      []string{"--server", "http://localhost:4096"},
		},
		{
			name:      "dir is positional",
			serverURL: "http://localhost:4096",
			dir:       "/home/user/project/sub",
			want:      []string{"--server", "http://localhost:4096", "/home/user/project/sub"},
		},
		{
			name:      "with session",
			serverURL: "http://localhost:4096",
			session:   "ses_abc123",
			want:      []string{"--server", "http://localhost:4096", "--session", "ses_abc123"},
		},
		{
			name:      "with continue",
			serverURL: "http://localhost:4096",
			cont:      true,
			want:      []string{"--server", "http://localhost:4096", "--continue"},
		},
		{
			name:      "all flags",
			serverURL: "http://localhost:4096",
			dir:       "/path",
			session:   "ses_abc123",
			cont:      true,
			want:      []string{"--server", "http://localhost:4096", "--session", "ses_abc123", "--continue", "/path"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := attachHostArgs(tt.serverURL, tt.dir, tt.session, tt.cont)
			if !slicesEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			for _, arg := range got {
				if arg == "attach" || arg == "--password" || arg == "--dir" {
					t.Errorf("args contain OpenCode 1 flag %q: %v", arg, got)
				}
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
		session   string
		cont      bool
		want      []string
	}{
		{
			name:      "no dir",
			serverURL: "http://localhost:4096",
			want:      []string{"opencode", "--server", "http://localhost:4096"},
		},
		{
			name:      "with dir",
			serverURL: "http://localhost:4096",
			dir:       "/home/user/project",
			want:      []string{"opencode", "--server", "http://localhost:4096", "/home/user/project"},
		},
		{
			name:      "with session",
			serverURL: "http://localhost:4096",
			session:   "ses_abc123",
			want:      []string{"opencode", "--server", "http://localhost:4096", "--session", "ses_abc123"},
		},
		{
			name:      "with continue",
			serverURL: "http://localhost:4096",
			cont:      true,
			want:      []string{"opencode", "--server", "http://localhost:4096", "--continue"},
		},
		{
			name:      "all flags",
			serverURL: "http://localhost:4096",
			dir:       "/path",
			session:   "ses_abc123",
			cont:      true,
			want:      []string{"opencode", "--server", "http://localhost:4096", "--session", "ses_abc123", "--continue", "/path"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := attachExecArgs(tt.serverURL, tt.dir, tt.session, tt.cont)
			if !slicesEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrPTYUnsupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		isMatch bool
	}{
		{
			name:    "matches sentinel directly",
			err:     errPTYUnsupported,
			isMatch: true,
		},
		{
			name:    "matches wrapped sentinel",
			err:     fmt.Errorf("wrap: %w", errPTYUnsupported),
			isMatch: true,
		},
		{
			name:    "does not match different error",
			err:     errors.New("other error"),
			isMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := errors.Is(tt.err, errPTYUnsupported); got != tt.isMatch {
				t.Errorf("errors.Is(%v, errPTYUnsupported) = %v, want %v", tt.err, got, tt.isMatch)
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

func TestHostPluginEnv(t *testing.T) {
	tests := []struct {
		name        string
		noPluginDir bool
		envContent  string
		userFiles   map[string]string
		// wantPlugins is the expected plugins list; "$SPEC" is replaced with the
		// jailoc plugin specifier. nil means no env entry is expected.
		wantPlugins []string
		wantKeys    map[string]string
	}{
		{
			name:        "no user config injects only jailoc plugin",
			wantPlugins: []string{"$SPEC"},
		},
		{
			name:        "missing plugin dir yields nothing",
			noPluginDir: true,
		},
		{
			name:        "user cli json plugins are preserved",
			userFiles:   map[string]string{"cli.json": `{"theme":"x","plugins":["file:///user/plug", ["npm-plug", {"a": 1}]]}`},
			wantPlugins: []string{"file:///user/plug", `["npm-plug",{"a":1}]`, "$SPEC"},
		},
		{
			name:        "jailoc plugin is not duplicated",
			userFiles:   map[string]string{"cli.json": `{"plugins":["$SPEC"]}`},
			wantPlugins: []string{"$SPEC"},
		},
		{
			name:      "unparseable cli json is left alone",
			userFiles: map[string]string{"cli.json": "{\n // comment\n \"plugins\": []\n}"},
		},
		{
			name:      "legacy tui json awaiting migration is left alone",
			userFiles: map[string]string{"tui.json": `{"plugin":["file:///user/plug"]}`},
		},
		{
			name:        "explicit env content is extended",
			envContent:  `{"theme":"dark","plugins":["file:///env/plug"]}`,
			userFiles:   map[string]string{"cli.json": `{"plugins":["file:///ignored"]}`},
			wantPlugins: []string{"file:///env/plug", "$SPEC"},
			wantKeys:    map[string]string{"theme": `"dark"`},
		},
		{
			name:       "invalid explicit env content is left alone",
			envContent: `not json`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tmp)
			t.Setenv("HOME", tmp)
			t.Setenv(cliConfigContentEnv, tt.envContent)

			pluginDir := filepath.Join(tmp, "cache", "tui-plugin")
			if !tt.noPluginDir {
				if err := os.MkdirAll(pluginDir, 0o750); err != nil {
					t.Fatalf("create plugin dir: %v", err)
				}
			}
			spec := "file://" + filepath.ToSlash(pluginDir)

			ocDir := filepath.Join(tmp, "opencode")
			if err := os.MkdirAll(ocDir, 0o750); err != nil {
				t.Fatalf("create opencode dir: %v", err)
			}
			for name, content := range tt.userFiles {
				content = strings.ReplaceAll(content, "$SPEC", spec)
				if err := os.WriteFile(filepath.Join(ocDir, name), []byte(content), 0o600); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			got := hostPluginEnv(pluginDir)
			if tt.wantPlugins == nil {
				if got != nil {
					t.Fatalf("hostPluginEnv() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("hostPluginEnv() = %v, want one entry", got)
			}
			key, value, _ := strings.Cut(got[0], "=")
			if key != cliConfigContentEnv {
				t.Fatalf("env key = %q, want %q", key, cliConfigContentEnv)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(value), &obj); err != nil {
				t.Fatalf("env value is not JSON: %v (%s)", err, value)
			}
			var plugins []json.RawMessage
			if err := json.Unmarshal(obj["plugins"], &plugins); err != nil {
				t.Fatalf("plugins is not an array: %v (%s)", err, value)
			}
			if len(plugins) != len(tt.wantPlugins) {
				t.Fatalf("plugins = %s, want %v", obj["plugins"], tt.wantPlugins)
			}
			for i, want := range tt.wantPlugins {
				want = strings.ReplaceAll(want, "$SPEC", spec)
				var s string
				if json.Unmarshal(plugins[i], &s) == nil {
					if s != want {
						t.Errorf("plugins[%d] = %q, want %q", i, s, want)
					}
					continue
				}
				if string(plugins[i]) != want {
					t.Errorf("plugins[%d] = %s, want %s", i, plugins[i], want)
				}
			}
			for k, want := range tt.wantKeys {
				if string(obj[k]) != want {
					t.Errorf("key %q = %s, want %s", k, obj[k], want)
				}
			}
		})
	}
}

func TestExecPluginEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	// Host-side inline config must not leak into the container session.
	t.Setenv(cliConfigContentEnv, `{"plugins":["file:///host/only"]}`)

	ocDir := filepath.Join(tmp, "opencode")
	if err := os.MkdirAll(ocDir, 0o750); err != nil {
		t.Fatalf("create opencode dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ocDir, "cli.json"), []byte(`{"plugins":["file:///user/plug"]}`), 0o600); err != nil {
		t.Fatalf("write cli.json: %v", err)
	}

	got := execPluginEnv()
	want := []string{cliConfigContentEnv + `={"plugins":["file:///user/plug","file://` + tuiPluginContainerDir + `"]}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("execPluginEnv() = %v, want %v", got, want)
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

func TestExitRewriter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{
			name:   "no match passes through",
			writes: []string{"hello world\n"},
			want:   "hello world\n",
		},
		{
			name:   "replaces opencode -s in single write",
			writes: []string{"  Continue  opencode -s ses_abc123\n"},
			want:   "  Continue  jailoc -s ses_abc123\n",
		},
		{
			name:   "replaces when split across writes",
			writes: []string{"  Continue  open", "code -s ses_abc123\n"},
			want:   "  Continue  jailoc -s ses_abc123\n",
		},
		{
			name:   "replaces when split mid-pattern",
			writes: []string{"opencode", " -s ses_x\n"},
			want:   "jailoc -s ses_x\n",
		},
		{
			name:   "multiple replacements",
			writes: []string{"opencode -s a\nopencode -s b\n"},
			want:   "jailoc -s a\njailoc -s b\n",
		},
		{
			name:   "partial match at end flushed without replacement",
			writes: []string{"trailing opencode"},
			want:   "trailing opencode",
		},
		{
			name:   "empty write",
			writes: []string{""},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			rw := &exitRewriter{w: &buf}

			for _, w := range tt.writes {
				n, err := rw.Write([]byte(w))
				if err != nil {
					t.Fatalf("Write(%q): %v", w, err)
				}
				if n != len(w) {
					t.Fatalf("Write(%q) returned %d, want %d", w, n, len(w))
				}
			}

			if err := rw.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}

			got := buf.String()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
