package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestValidateSecretSources(t *testing.T) {
	worldReadable := writeSecretFile(t, "world-readable", 0o644)
	ownerOnly := writeSecretFile(t, "owner-only", 0o600)
	dir := t.TempDir()

	tests := []struct {
		name    string
		env     map[string]string
		ws      *workspace.Resolved
		wantErr string
	}{
		{
			name: "no secrets",
			ws:   &workspace.Resolved{Name: "ws"},
		},
		{
			name: "nil secrets slice with env entries",
			ws:   &workspace.Resolved{Name: "ws", Env: []string{"FOO=bar"}},
		},
		{
			name: "env destination from environment source",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
			}},
		},
		{
			name: "env destination from file source need not be world readable",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "FILE_KEY", Kind: workspace.SecretKindEnv, FromFile: ownerOnly},
			}},
		},
		{
			name: "file destination from environment source",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "envkey", Kind: workspace.SecretKindFile, FromEnv: "JAILOC_TEST_TOKEN"},
			}},
		},
		{
			name: "env source unset",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_ABSENT"},
			}},
			wantErr: `secret "GH_TOKEN": host environment variable "JAILOC_TEST_ABSENT" is not set`,
		},
		{
			name: "env source set but empty",
			env:  map[string]string{"JAILOC_TEST_EMPTY": ""},
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_EMPTY"},
			}},
			wantErr: `secret "GH_TOKEN": host environment variable "JAILOC_TEST_EMPTY" is set but empty`,
		},
		{
			name: "file destination from file source world readable",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: worldReadable},
			}},
		},
		{
			name: "file source missing",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: filepath.Join(dir, "absent")},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q does not exist", "key", filepath.Join(dir, "absent")),
		},
		{
			name: "file source is a directory",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: dir},
			}},
			wantErr: fmt.Sprintf("secret %q: file %q is not a regular file", "key", dir),
		},
		{
			name: "file source not world readable",
			ws: &workspace.Resolved{Name: "ws", Secrets: []workspace.ResolvedSecret{
				{Name: "key", Kind: workspace.SecretKindFile, FromFile: ownerOnly},
			}},
		},
		{
			name: "env secret name collides with env entry",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FOO=bar", "GH_TOKEN=already-here"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "GH_TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
				},
			},
			wantErr: `env secret "GH_TOKEN" collides with an env entry`,
		},
		{
			name: "file-sourced env secret name collides with env entry",
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FILE_KEY=already-here"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "FILE_KEY", Kind: workspace.SecretKindEnv, FromFile: worldReadable},
				},
			},
			wantErr: `env secret "FILE_KEY" collides with an env entry`,
		},
		{
			// workspace.Resolve keys secrets by name, so a repeated name in
			// Resolved.Secrets is unrepresentable and needs no runtime check here.
			// TestResolveSecretsWorkspaceOverrideAcrossKinds covers the dedup.
			name: "env secret not colliding with a differently named env entry",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{Name: "ws", Env: []string{"OTHER=1"}, Secrets: []workspace.ResolvedSecret{
				{Name: "TOKEN", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
			}},
		},
		{
			name: "distinct env and file secrets pass",
			env:  map[string]string{"JAILOC_TEST_TOKEN": "value"},
			ws: &workspace.Resolved{
				Name: "ws",
				Env:  []string{"FOO=bar"},
				Secrets: []workspace.ResolvedSecret{
					{Name: "ALPHA", Kind: workspace.SecretKindEnv, FromEnv: "JAILOC_TEST_TOKEN"},
					{Name: "beta", Kind: workspace.SecretKindFile, FromFile: worldReadable},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			err := validateSecretSources(tt.ws)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSecretSources() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSecretSources() = nil, want error %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("validateSecretSources() error =\n  %q\nwant\n  %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestConfigShowsSecretReferences is not parallel: it uses t.Setenv to point
// config.Load at a scratch HOME and to export the secret value that must not
// leak into stdout.
func TestConfigShowsSecretReferences(t *testing.T) {
	home := t.TempDir()
	secretFile := writeSecretFile(t, "filevalue", 0o644)

	t.Setenv("HOME", home)
	t.Setenv("GITHUB_TOKEN", "supersecret")

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	content := fmt.Sprintf(`[workspaces.alpha]
paths = ["/data/alpha"]

[workspaces.alpha.secrets.env.GH_TOKEN]
from_env = "GITHUB_TOKEN"

[workspaces.alpha.secrets.env.FILE_KEY]
from_file = %q

[workspaces.alpha.secrets.file.envkey]
from_env = "GITHUB_TOKEN"

[workspaces.alpha.secrets.file.key]
from_file = %q

[workspaces.bare]
paths = ["/data/bare"]
`, secretFile, secretFile)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	assertContains(t, out, "  Secrets:\n    - FILE_KEY (env from_file "+secretFile+")\n    - GH_TOKEN (env from_env GITHUB_TOKEN)\n    - envkey (file from_env GITHUB_TOKEN)\n    - key (file from_file "+secretFile+")\n")
	assertContains(t, out, "Workspace: bare")

	if strings.Contains(out, "supersecret") {
		t.Fatalf("jailoc config output leaked the secret value:\n%s", out)
	}
	if strings.Contains(out, "filevalue") {
		t.Fatalf("jailoc config output leaked the secret file contents:\n%s", out)
	}

	_, bareSection, ok := strings.Cut(out, "Workspace: bare")
	if !ok {
		t.Fatalf("workspace bare section missing from output:\n%s", out)
	}
	assertContains(t, bareSection, "  Secrets:\n    (none)\n")
}

func TestConfigSecretsOutputOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOST_A", "secret-value-a")
	t.Setenv("HOST_Z", "secret-value-z")

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	content := `[workspaces.alpha]
paths = ["/data/alpha"]

[workspaces.alpha.secrets.env.Z_TOKEN]
from_env = "HOST_Z"

[workspaces.alpha.secrets.env.A_TOKEN]
from_env = "HOST_A"

[workspaces.alpha.secrets.env.F_TOKEN]
from_file = "/a/envfile"

[workspaces.alpha.secrets.file.z-file]
from_file = "/z/file"

[workspaces.alpha.secrets.file.a-file]
from_file = "/a/file"

[workspaces.alpha.secrets.file.e-file]
from_env = "HOST_A"

[workspaces.empty]
paths = []
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	want := "  Secrets:\n" +
		"    - A_TOKEN (env from_env HOST_A)\n" +
		"    - F_TOKEN (env from_file /a/envfile)\n" +
		"    - Z_TOKEN (env from_env HOST_Z)\n" +
		"    - a-file (file from_file /a/file)\n" +
		"    - e-file (file from_env HOST_A)\n" +
		"    - z-file (file from_file /z/file)\n"
	assertContains(t, out, want)
	_, emptySection, ok := strings.Cut(out, "Workspace: empty")
	if !ok {
		t.Fatalf("workspace empty section missing from output:\n%s", out)
	}
	assertContains(t, emptySection, "  Secrets:\n    (none)\n")
	for _, secretValue := range []string{"secret-value-a", "secret-value-z"} {
		if strings.Contains(out, secretValue) {
			t.Fatalf("jailoc config output leaked secret value %q:\n%s", secretValue, out)
		}
	}
}

// TestConfigShowsDefaultsSecrets is not parallel: it uses t.Setenv to point
// config.Load at a scratch HOME.
//
// A secret declared only under [defaults.secrets] applies to every workspace,
// so jailoc config must surface it. The per-workspace sections print the raw
// workspace scope and therefore never mention it.
func TestConfigShowsDefaultsSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOST_GLOBAL", "global-secret-value")

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	content := `[defaults.secrets.env.GLOBAL_TOKEN]
from_env = "HOST_GLOBAL"

[defaults.secrets.file.global-cert]
from_file = "/etc/ssl/global.pem"

[workspaces.alpha]
paths = ["/data/alpha"]
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	assertContains(t, out, "Defaults Secrets:\n"+
		"  - GLOBAL_TOKEN (env from_env HOST_GLOBAL)\n"+
		"  - global-cert (file from_file /etc/ssl/global.pem)\n")

	if strings.Contains(out, "global-secret-value") {
		t.Fatalf("jailoc config output leaked the default secret value:\n%s", out)
	}
}

// TestConfigDefaultsSecretsEmpty is not parallel: it uses t.Setenv to point
// config.Load at a scratch HOME.
func TestConfigDefaultsSecretsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[workspaces.alpha]\npaths = [\"/data/alpha\"]\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		if err := runConfig(cmd, nil); err != nil {
			t.Fatalf("runConfig() = %v", err)
		}
	})

	assertContains(t, out, "Defaults Secrets:\n  (none)\n")
}

// TestPrintSecretsOrderMatchesResolvedOrder locks jailoc config to the single
// ordering definition in workspace.FlattenSecrets: one sort by name across both
// destination sub-tables. Grouping by destination first would place ZZZ_TOKEN
// before aaa-cert, which is what this input distinguishes.
func TestPrintSecretsOrderMatchesResolvedOrder(t *testing.T) {
	t.Parallel()

	secrets := config.Secrets{
		Env:  map[string]config.Secret{"zzz_token": {FromEnv: "HOST_Z"}},
		File: map[string]config.Secret{"aaa-cert": {FromFile: "/a/cert"}},
	}

	var names []string
	for _, s := range workspace.FlattenSecrets(secrets) {
		names = append(names, s.Name)
	}
	want := []string{"aaa-cert", "zzz_token"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("FlattenSecrets order = %v, want %v", names, want)
	}

	out := captureStdout(t, func() { printSecrets(secrets, "  ") })
	wantOut := "  - aaa-cert (file from_file /a/cert)\n  - zzz_token (env from_env HOST_Z)\n"
	if out != wantOut {
		t.Fatalf("printSecrets() = %q, want %q", out, wantOut)
	}
}

func TestReadSecret(t *testing.T) {
	t.Parallel()
	pem := writeSecretFile(t, "line1\nline2\n", 0o600)
	tests := []struct {
		name    string
		secret  workspace.ResolvedSecret
		want    string
		wantErr string
	}{
		{
			name:   "env secret drops the trailing line break",
			secret: workspace.ResolvedSecret{Name: "T", Kind: workspace.SecretKindEnv, FromCommand: "printf 'tok\\r\\n'"},
			want:   "tok",
		},
		{
			name:   "file secret keeps the output",
			secret: workspace.ResolvedSecret{Name: "t", Kind: workspace.SecretKindFile, FromCommand: "echo tok"},
			want:   "tok\n",
		},
		{
			name:   "command runs in a shell",
			secret: workspace.ResolvedSecret{Name: "T", Kind: workspace.SecretKindEnv, FromCommand: "echo abc | tr a-c x-z"},
			want:   "xyz",
		},
		{
			name:    "failing command",
			secret:  workspace.ResolvedSecret{Name: "T", Kind: workspace.SecretKindEnv, FromCommand: "exit 3"},
			wantErr: "exit status 3",
		},
		{
			name:    "silent command",
			secret:  workspace.ResolvedSecret{Name: "T", Kind: workspace.SecretKindEnv, FromCommand: "true"},
			wantErr: "printed nothing",
		},
		{
			name:    "multi-line env secret",
			secret:  workspace.ResolvedSecret{Name: "T", Kind: workspace.SecretKindEnv, FromFile: pem},
			wantErr: "use a file secret",
		},
		{
			name:   "multi-line file secret",
			secret: workspace.ResolvedSecret{Name: "t", Kind: workspace.SecretKindFile, FromFile: pem},
			want:   "line1\nline2\n",
		},
		{
			name:   "command receives JAILOC_WORKSPACE",
			secret: workspace.ResolvedSecret{Name: "T", Kind: workspace.SecretKindEnv, FromCommand: `printf '%s\n' "$JAILOC_WORKSPACE"`},
			want:   "ws",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readSecret(context.Background(), "ws", tt.secret, false)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("readSecret error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readSecret: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("readSecret = %q, want %q", got, tt.want)
			}
		})
	}
}

type memorySecretKeyring struct {
	mu       sync.Mutex
	items    map[string][]byte
	expires  map[string]time.Time
	undelete map[string]bool
	roundSec bool
}

func newMemorySecretKeyring() *memorySecretKeyring {
	return &memorySecretKeyring{items: map[string][]byte{}, expires: map[string]time.Time{}, undelete: map[string]bool{}}
}

// get honors the TTL like the kernel keyring does.
func (m *memorySecretKeyring) get(_ context.Context, account string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[account]
	if exp, set := m.expires[account]; !ok || set && !secretCacheTime().Before(exp) {
		return nil, errSecretNotCached
	}
	return append([]byte(nil), v...), nil
}

func (m *memorySecretKeyring) set(_ context.Context, account string, data []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[account] = append([]byte(nil), data...)
	delete(m.expires, account)
	if ttl > 0 {
		if m.roundSec {
			ttl = time.Duration(math.Ceil(ttl.Seconds())) * time.Second
		}
		m.expires[account] = secretCacheTime().Add(ttl)
	}
	return nil
}

func (m *memorySecretKeyring) delete(_ context.Context, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.undelete[account] {
		return errors.New("keychain is locked")
	}
	delete(m.items, account)
	return nil
}

// cached reports whether account holds a value, expired or not.
func (m *memorySecretKeyring) cached(account string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.items[account]
	return ok
}

// useMemorySecretCache replaces the keychain and clock for one test and
// keeps the cache lock files in a temporary home directory.
func useMemorySecretCache(t *testing.T, now *time.Time) *memorySecretKeyring {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	mem := newMemorySecretKeyring()
	origCache, origTime := secretCache, secretCacheTime
	secretCache = mem
	secretCacheTime = func() time.Time { return *now }
	t.Cleanup(func() {
		secretCache = origCache
		secretCacheTime = origTime
	})
	return mem
}

func TestReadSecretTTLCacheAndPurge(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	useMemorySecretCache(t, &now)

	counterFile := filepath.Join(t.TempDir(), "count")
	if err := os.WriteFile(counterFile, []byte("0"), 0o600); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	cmdStr := fmt.Sprintf(`n=$(cat %q); n=$((n+1)); printf '%%d' "$n" > %q; printf 'val-%%s-%%d\n' "$JAILOC_WORKSPACE" "$n"`, counterFile, counterFile)
	s := workspace.ResolvedSecret{
		Name:        "TOKEN",
		Kind:        workspace.SecretKindEnv,
		FromCommand: cmdStr,
		TTL:         10 * time.Minute,
	}

	got1, err := readSecret(context.Background(), "alpha", s, false)
	if err != nil || string(got1) != "val-alpha-1" {
		t.Fatalf("first read = %q, %v; want val-alpha-1", got1, err)
	}

	// Within TTL, cached value is returned without invoking the command again.
	now = now.Add(5 * time.Minute)
	got2, err := readSecret(context.Background(), "alpha", s, false)
	if err != nil || string(got2) != "val-alpha-1" {
		t.Fatalf("cached read = %q, %v; want val-alpha-1", got2, err)
	}

	// Another workspace has its own cache key and JAILOC_WORKSPACE value.
	gotBeta, err := readSecret(context.Background(), "beta", s, false)
	if err != nil || string(gotBeta) != "val-beta-2" {
		t.Fatalf("beta read = %q, %v; want val-beta-2", gotBeta, err)
	}

	// purgeCache=true bypasses and refreshes the cache for alpha.
	gotPurged, err := readSecret(context.Background(), "alpha", s, true)
	if err != nil || string(gotPurged) != "val-alpha-3" {
		t.Fatalf("purged read = %q, %v; want val-alpha-3", gotPurged, err)
	}

	// After TTL expiry, the command runs again.
	now = now.Add(11 * time.Minute)
	gotExpired, err := readSecret(context.Background(), "alpha", s, false)
	if err != nil || string(gotExpired) != "val-alpha-4" {
		t.Fatalf("expired read = %q, %v; want val-alpha-4", gotExpired, err)
	}

	// purgeWorkspaceSecretCache removes the workspace's cached entries even if
	// the secret was removed from ws.Secrets (via the workspace index).
	if err := purgeWorkspaceSecretCache(context.Background(), &workspace.Resolved{Name: "alpha"}); err != nil {
		t.Fatalf("purgeWorkspaceSecretCache: %v", err)
	}
	gotAfterDownPurge, err := readSecret(context.Background(), "alpha", s, false)
	if err != nil || string(gotAfterDownPurge) != "val-alpha-5" {
		t.Fatalf("post-purgeWorkspaceSecretCache read = %q, %v; want val-alpha-5", gotAfterDownPurge, err)
	}

	// Multiline env secret output is rejected and never cached.
	badEnv := workspace.ResolvedSecret{
		Name:        "BAD",
		Kind:        workspace.SecretKindEnv,
		FromCommand: "printf 'line1\\nline2\\n'",
		TTL:         10 * time.Minute,
	}
	if _, err := readSecret(context.Background(), "alpha", badEnv, false); err == nil {
		t.Fatal("expected error for multiline env secret, got nil")
	}
	if _, ok := readCachedSecret(context.Background(), "alpha", badEnv); ok {
		t.Fatal("multiline env secret output must not be cached")
	}
}

func cachedCommandSecret(name string, ttl time.Duration) workspace.ResolvedSecret {
	return workspace.ResolvedSecret{Name: name, Kind: workspace.SecretKindEnv, FromCommand: "echo " + name, TTL: ttl}
}

func TestPurgeFindsLongLivedSecretAfterShorterOne(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mem := useMemorySecretCache(t, &now)

	long, short := cachedCommandSecret("LONG", 72*time.Hour), cachedCommandSecret("SHORT", time.Hour)
	for _, s := range []workspace.ResolvedSecret{long, short} {
		if _, err := readSecret(context.Background(), "alpha", s, false); err != nil {
			t.Fatalf("read %s: %v", s.Name, err)
		}
	}

	// LONG is no longer configured and the shorter secret expired long ago.
	now = now.Add(48 * time.Hour)
	if err := purgeWorkspaceSecretCache(context.Background(), &workspace.Resolved{Name: "alpha", Secrets: []workspace.ResolvedSecret{short}}); err != nil {
		t.Fatalf("purgeWorkspaceSecretCache: %v", err)
	}
	if mem.cached(secretCacheAccount("alpha", long)) {
		t.Fatal("purge left the 72h secret in the keychain")
	}
}

func TestPurgeKeepsUndeletedSecretsIndexed(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mem := useMemorySecretCache(t, &now)

	s := cachedCommandSecret("TOKEN", time.Hour)
	if _, err := readSecret(context.Background(), "alpha", s, false); err != nil {
		t.Fatalf("read: %v", err)
	}
	account := secretCacheAccount("alpha", s)
	ws := &workspace.Resolved{Name: "alpha"}

	// Even after the secret's TTL has elapsed, a failed deletion keeps the
	// nonempty index alive so a later purge still finds the secret.
	now = now.Add(2 * time.Hour)
	mem.undelete[account] = true
	err := purgeWorkspaceSecretCache(context.Background(), ws)
	if err == nil {
		t.Fatal("purge reported success while the secret is still cached")
	}
	assertContains(t, err.Error(), "keychain is locked")
	if _, err := readSecret(context.Background(), "alpha", s, true); err == nil {
		t.Fatal("readSecret with purgeCache ignored the failed eviction")
	}

	// The index still lists the secret, so a later purge removes it.
	mem.undelete[account] = false
	if err := purgeWorkspaceSecretCache(context.Background(), ws); err != nil {
		t.Fatalf("retried purge: %v", err)
	}
	if mem.cached(account) || mem.cached(secretCacheIndexAccount("alpha")) {
		t.Fatal("retried purge left cache entries behind")
	}
}

func TestWriteCachedSecretConcurrentIndex(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mem := useMemorySecretCache(t, &now)

	var wg sync.WaitGroup
	secrets := make([]workspace.ResolvedSecret, 32)
	for i := range secrets {
		secrets[i] = cachedCommandSecret(fmt.Sprintf("S%d", i), time.Hour)
		wg.Go(func() { writeCachedSecret("alpha", secrets[i], []byte("v")) })
	}
	wg.Wait()

	idx, err := readSecretCacheIndex(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("readSecretCacheIndex: %v", err)
	}
	if len(idx) != len(secrets) {
		t.Fatalf("index lists %d secrets, want %d", len(idx), len(secrets))
	}
	if err := purgeWorkspaceSecretCache(context.Background(), &workspace.Resolved{Name: "alpha"}); err != nil {
		t.Fatalf("purgeWorkspaceSecretCache: %v", err)
	}
	for _, s := range secrets {
		if mem.cached(secretCacheAccount("alpha", s)) {
			t.Fatalf("purge left %s cached", s.Name)
		}
	}
}

func TestWriteCachedSecretDelayedAtLock(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mem := useMemorySecretCache(t, &now)
	mem.roundSec = true

	s := cachedCommandSecret("DELAYED", 1500*time.Millisecond)
	ready := make(chan struct{})
	done := make(chan struct{})
	if err := withSecretCacheLock(context.Background(), "alpha", func() error {
		// A waiter whose context is canceled while the lock is held returns
		// promptly instead of blocking indefinitely.
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := withSecretCacheLock(cctx, "alpha", func() error { return nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled lock wait = %v, want context.Canceled", err)
		}

		go func() {
			close(ready)
			writeCachedSecret("alpha", s, []byte("v"))
			close(done)
		}()
		<-ready
		time.Sleep(20 * time.Millisecond)
		// The writer waited 10s on the lock before writing a 1.5s secret.
		now = now.Add(10 * time.Second)
		return nil
	}); err != nil {
		t.Fatalf("withSecretCacheLock: %v", err)
	}
	<-done

	// Advance 1.4s into the 1.5s TTL: both the secret and the index must
	// still be alive so a purge after config removal deletes the secret.
	now = now.Add(1400 * time.Millisecond)
	if err := purgeWorkspaceSecretCache(context.Background(), &workspace.Resolved{Name: "alpha"}); err != nil {
		t.Fatalf("purgeWorkspaceSecretCache: %v", err)
	}
	if mem.cached(secretCacheAccount("alpha", s)) {
		t.Fatal("purge missed the delayed secret")
	}
}

func TestDefaultSecretKeyringRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel keyring test runs on Linux only")
	}
	ctx := context.Background()
	account := fmt.Sprintf("jailoc:test:%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = defaultSecretKeyring.delete(ctx, account) })

	if _, err := defaultSecretKeyring.get(ctx, account); errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOSYS) {
		t.Skipf("kernel keyring is unavailable: %v", err)
	} else if !errors.Is(err, errSecretNotCached) {
		t.Fatalf("get missing = %v, want errSecretNotCached", err)
	}
	if err := defaultSecretKeyring.set(ctx, account, []byte("secret-val"), 10*time.Second); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := defaultSecretKeyring.get(ctx, account)
	if err != nil || string(got) != "secret-val" {
		t.Fatalf("get = %q, %v; want secret-val", got, err)
	}
	if err := defaultSecretKeyring.delete(ctx, account); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := defaultSecretKeyring.get(ctx, account); !errors.Is(err, errSecretNotCached) {
		t.Fatalf("get after delete = %v, want errSecretNotCached", err)
	}
}

func TestRunSecretCommandCancelClosesHeldStdoutPipe(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil { //nolint:gosec // test path
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// A background child inherits stdout (while redirecting stderr so the test
	// runner's stderr pipe is not held) and outlives the shell when ctx is
	// canceled; runSecretCommand closes the pipe after secretCommandWaitDelay.
	s := workspace.ResolvedSecret{
		Name:        "HANGING",
		Kind:        workspace.SecretKindEnv,
		FromCommand: fmt.Sprintf("sleep 30 2>/dev/null & printf '%%d' $! > %q; wait", pidFile),
	}
	start := time.Now()
	_, err := runSecretCommand(ctx, "ws", s)
	if err == nil {
		t.Fatal("expected error from canceled command, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runSecretCommand took %s after cancellation, want < 5s", elapsed)
	}
}

func TestRunSecretCommandUncanceledWithBackgroundHelper(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil { //nolint:gosec // test path
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	// An uncanceled command that spawns a background helper inheriting stdout
	// longer than secretCommandWaitDelay still succeeds once the helper exits.
	s := workspace.ResolvedSecret{
		Name:        "TOKEN",
		Kind:        workspace.SecretKindEnv,
		FromCommand: fmt.Sprintf("printf 'tok\\n'; sleep 0.4 2>/dev/null & printf '%%d' $! > %q", pidFile),
	}
	got, err := readSecret(context.Background(), "ws", s, false)
	if err != nil || string(got) != "tok" {
		t.Fatalf("readSecret = %q, %v; want tok", got, err)
	}
}

func TestRunSecretCommandSlowNotice(t *testing.T) {
	origNotice := secretCommandNotice
	secretCommandNotice = 20 * time.Millisecond
	t.Cleanup(func() { secretCommandNotice = origNotice })

	s := workspace.ResolvedSecret{
		Name:        "SLOW_SECRET",
		Kind:        workspace.SecretKindEnv,
		FromCommand: "sleep 0.08; printf 'tok\\n'",
	}
	var got []byte
	var err error
	stderr := captureStderr(t, func() {
		got, err = readSecret(context.Background(), "alpha", s, false)
	})
	if err != nil || string(got) != "tok" {
		t.Fatalf("readSecret = %q, %v; want tok", got, err)
	}
	assertContains(t, stderr, "Waiting for secret SLOW_SECRET from_command (workspace alpha)...")
}
func TestSecretReferenceShowsTTL(t *testing.T) {
	t.Parallel()
	withTTL := workspace.ResolvedSecret{
		Name:        "NPM_TOKEN",
		Kind:        workspace.SecretKindEnv,
		FromCommand: "op read op://dev/npm/token",
		TTL:         time.Hour,
	}
	if got, want := secretReference(withTTL), `NPM_TOKEN (env from_command "op read op://dev/npm/token", ttl 1h0m0s)`; got != want {
		t.Fatalf("secretReference(withTTL) = %q, want %q", got, want)
	}

	withoutTTL := workspace.ResolvedSecret{
		Name:        "NPM_TOKEN",
		Kind:        workspace.SecretKindEnv,
		FromCommand: "op read op://dev/npm/token",
	}
	if got, want := secretReference(withoutTTL), `NPM_TOKEN (env from_command "op read op://dev/npm/token")`; got != want {
		t.Fatalf("secretReference(withoutTTL) = %q, want %q", got, want)
	}
}

func writeSecretFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod secret file: %v", err)
	}
	return path
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	origNoColor := color.NoColor
	color.NoColor = true
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = origStdout
		color.NoColor = origNoColor
	})

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return out
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	origNoColor := color.NoColor
	color.NoColor = true
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
		color.NoColor = origNoColor
	})

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return out
}
