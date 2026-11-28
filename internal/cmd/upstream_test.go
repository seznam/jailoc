package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/netbroker"
)

func TestResolveUpstreamAuthSources(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	useMemorySecretCache(t, &now)
	t.Setenv("JAILOC_TEST_PROXY_PASSWORD", "env-pass")
	file := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(file, []byte("file-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		auth netbroker.UpstreamAuth
		want string
	}{
		{"env", netbroker.UpstreamAuth{Username: "u", FromEnv: "JAILOC_TEST_PROXY_PASSWORD"}, "env-pass"},
		{"file keeps raw bytes for the broker to trim", netbroker.UpstreamAuth{Username: "u", FromFile: file}, "file-pass\n"},
		{"command sees the workspace", netbroker.UpstreamAuth{Username: "u", FromCommand: `printf 'cmd-%s' "$JAILOC_WORKSPACE"`}, "cmd-api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveUpstreamAuth(context.Background(), "api", tt.auth)
			if err != nil {
				t.Fatalf("resolveUpstreamAuth: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("resolveUpstreamAuth = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveUpstreamAuthCachesCommandWithTTL(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mem := useMemorySecretCache(t, &now)
	counter := filepath.Join(t.TempDir(), "count")
	auth := netbroker.UpstreamAuth{Username: "u", FromCommand: `echo x >> ` + counter + `; printf pass`, TTL: "10m"}
	for range 2 {
		got, err := resolveUpstreamAuth(context.Background(), "api", auth)
		if err != nil || string(got) != "pass" {
			t.Fatalf("resolveUpstreamAuth = %q, %v", got, err)
		}
	}
	runs, err := os.ReadFile(counter) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(runs), "x"); n != 1 {
		t.Fatalf("command ran %d times, want 1 (cached within ttl)", n)
	}
	s, err := upstreamAuthSecret(auth)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != upstreamAuthSecretName || s.TTL != 10*time.Minute {
		t.Fatalf("upstreamAuthSecret = %+v", s)
	}
	if _, err := mem.get(context.Background(), secretCacheAccount("api", s)); err != nil {
		t.Fatalf("cached account missing: %v", err)
	}
	idx, err := readSecretCacheIndex(context.Background(), "api")
	if err != nil || idx[secretCacheAccount("api", s)].IsZero() {
		t.Fatalf("index does not track the auth account: %v %v", idx, err)
	}
}

func TestResolveUpstreamAuthInvalidTTL(t *testing.T) {
	t.Parallel()
	_, err := resolveUpstreamAuth(context.Background(), "api", netbroker.UpstreamAuth{Username: "u", FromCommand: "printf p", TTL: "soon"})
	if err == nil || !strings.Contains(err.Error(), "ttl") {
		t.Fatalf("resolveUpstreamAuth error = %v, want ttl error", err)
	}
}

// The broker is detached: the command gets neither the terminal nor the
// broker's stderr. Swaps os.Stdin and os.Stderr, so not parallel.
func TestResolveUpstreamAuthCommandIsNonInteractive(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdinW.WriteString("from-terminal\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdinW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origIn, origErr, origNotice := os.Stdin, os.Stderr, secretCommandNotice
	os.Stdin, os.Stderr, secretCommandNotice = stdinR, stderrW, time.Millisecond
	t.Cleanup(func() { os.Stdin, os.Stderr, secretCommandNotice = origIn, origErr, origNotice })

	got, err := resolveUpstreamAuth(context.Background(), "api", netbroker.UpstreamAuth{
		Username:    "u",
		FromCommand: `sleep 0.05; echo helper-diagnostic >&2; if read -r x; then printf 'stdin:%s' "$x"; else printf eof; fi`,
	})
	os.Stdin, os.Stderr = origIn, origErr
	_ = stderrW.Close()
	leaked, _ := io.ReadAll(stderrR)
	if err != nil {
		t.Fatalf("resolveUpstreamAuth: %v", err)
	}
	if string(got) != "eof" {
		t.Fatalf("command read %q from stdin, want eof", got)
	}
	if len(leaked) > 0 {
		t.Fatalf("command or notice wrote to stderr: %q", leaked)
	}
}

func TestResolveUpstreamAuthCommandTimeout(t *testing.T) {
	orig := upstreamAuthTimeout
	upstreamAuthTimeout = 100 * time.Millisecond
	t.Cleanup(func() { upstreamAuthTimeout = orig })
	start := time.Now()
	_, err := resolveUpstreamAuth(context.Background(), "api", netbroker.UpstreamAuth{Username: "u", FromCommand: "exec sleep 5"})
	if err == nil {
		t.Fatal("resolveUpstreamAuth succeeded, want timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("resolveUpstreamAuth took %s, want bounded", time.Since(start))
	}
}

func TestUpstreamPolicyProxy(t *testing.T) {
	t.Parallel()
	if got := upstreamPolicyProxy("api", nil); got != nil {
		t.Fatalf("nil proxy = %+v", got)
	}
	off := false
	if got := upstreamPolicyProxy("api", &config.UpstreamProxy{Enabled: &off, URL: "http://p"}); got != nil {
		t.Fatalf("disabled proxy = %+v", got)
	}
	p := &config.UpstreamProxy{
		URL:    "http://proxy.example:3128",
		CA:     "/etc/proxy-ca.pem",
		Direct: []string{"internal.example"},
		Auth:   &config.UpstreamAuth{Username: "u", FromCommand: "pass show proxy", TTL: "1h"},
	}
	got := upstreamPolicyProxy("api", p)
	want := netbroker.UpstreamProxy{
		Workspace: "api", URL: "http://proxy.example:3128", CA: "/etc/proxy-ca.pem", Direct: []string{"internal.example"},
		Auth: &netbroker.UpstreamAuth{Username: "u", FromCommand: "pass show proxy", TTL: "1h"},
	}
	if got == nil || got.Workspace != want.Workspace || got.URL != want.URL || got.CA != want.CA || strings.Join(got.Direct, ",") != "internal.example" || *got.Auth != *want.Auth {
		t.Fatalf("upstreamPolicyProxy = %+v, want %+v", got, want)
	}
	got.Direct[0] = "changed"
	if p.Direct[0] != "internal.example" {
		t.Fatal("upstreamPolicyProxy aliases the configuration")
	}
}

func TestBuilderUpstreamPolicyProxy(t *testing.T) {
	t.Parallel()
	static := &config.UpstreamProxy{URL: "http://proxy.example:3128", Auth: &config.UpstreamAuth{Username: "u", FromEnv: "P"}, Direct: []string{"10.0.0.0/8"}, CA: "/ca.pem"}
	cfg := &config.Config{Workspaces: map[string]config.Workspace{}}
	cfg.Defaults.Network = &config.Network{UpstreamProxy: static}
	got := builderUpstreamPolicyProxy(cfg)
	if got == nil || got.Workspace != "builder" || got.URL != static.URL || got.Auth == nil || got.Auth.FromEnv != "P" || got.CA != "/ca.pem" || len(got.Direct) != 1 {
		t.Fatalf("builderUpstreamPolicyProxy = %+v", got)
	}
	cfg.Defaults.Network = &config.Network{UpstreamProxy: &config.UpstreamProxy{Command: "proxy-helper"}}
	if got := builderUpstreamPolicyProxy(cfg); got != nil {
		t.Fatalf("command proxy reached the builder: %+v", got)
	}
}

func TestPrintUpstreamProxyReferences(t *testing.T) {
	// captureStdout swaps os.Stdout: do not run in parallel.
	out := captureStdout(t, func() {
		printUpstreamProxy(&config.UpstreamProxy{
			URL:    "http://proxy.example:3128",
			CA:     "/ca.pem",
			Direct: []string{"internal.example"},
			Auth:   &config.UpstreamAuth{Username: "u", FromEnv: "PROXY_PASS"},
		}, "    ")
	})
	for _, want := range []string{"http://proxy.example:3128", "/ca.pem", "internal.example", "u", "from_env PROXY_PASS"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
	none := captureStdout(t, func() { printUpstreamProxy(nil, "    ") })
	if !strings.Contains(none, "(none)") {
		t.Errorf("output %q lacks (none)", none)
	}
}
func TestPrintConfigWarningsShowsUnmatchedUpstreamDirect(t *testing.T) {
	// captureStderr swaps os.Stderr: do not run in parallel.
	cfg := &config.Config{
		Defaults: config.Defaults{Agent: "claude"},
		Workspaces: map[string]config.Workspace{
			"api": {
				Paths: []string{"/srv/api"},
				Network: &config.Network{
					Rules:         []config.NetworkRule{{Allow: []string{"allowed.example"}}},
					UpstreamProxy: &config.UpstreamProxy{URL: "http://proxy.example:3128", Direct: []string{"unmatched.example"}},
				},
			},
		},
	}
	out := captureStderr(t, func() { printConfigWarnings(cfg) })
	if !strings.Contains(out, `"unmatched.example"`) || strings.Contains(out, "jailoc migrate") {
		t.Fatalf("stderr = %q, want unmatched direct warning without migrate footer", out)
	}
}
