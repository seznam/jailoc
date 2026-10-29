package config

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir for %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

// safeHome creates a temporary HOME directory outside forbidden mount host paths
// (e.g. /var on macOS, where t.TempDir() resolves). Mount validation tests need
// this because ~ expansion resolves to HOME, and /var is a forbidden host prefix.
func safeHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "jailoc-test-home-*")
	if err != nil {
		t.Fatalf("create safe home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	return home
}

func TestLoadFullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[base]

[workspaces.default]
paths = ["/data/workspace"]
allowed_hosts = ["foo.com", "bar.com"]
allowed_networks = ["172.20.0.0/16"]
build_context = "/tmp/context"
`)

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if cfg.Base.Dockerfile != "" {
		t.Fatalf("unexpected base dockerfile: %q", cfg.Base.Dockerfile)
	}

	ws := cfg.Workspaces["default"]
	if len(ws.Paths) != 1 || ws.Paths[0] != "/data/workspace" {
		t.Fatalf("unexpected paths: %#v", ws.Paths)
	}
	if !reflect.DeepEqual(ws.AllowedHosts, []string{"foo.com", "bar.com"}) {
		t.Fatalf("unexpected allowed hosts: %#v", ws.AllowedHosts)
	}
	if !reflect.DeepEqual(ws.AllowedNetworks, []string{"172.20.0.0/16"}) {
		t.Fatalf("unexpected allowed networks: %#v", ws.AllowedNetworks)
	}
	if ws.BuildContext != "/tmp/context" {
		t.Fatalf("unexpected build context: %q", ws.BuildContext)
	}
}

func TestLoadMinimalConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[workspaces.default]
paths = ["/data/workspace"]
`)

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if cfg.Base.Dockerfile != "" {
		t.Fatalf("expected empty base dockerfile, got %q", cfg.Base.Dockerfile)
	}

	ws := cfg.Workspaces["default"]
	if len(ws.Paths) != 1 || ws.Paths[0] != "/data/workspace" {
		t.Fatalf("unexpected paths: %#v", ws.Paths)
	}
	if len(ws.AllowedHosts) != 0 {
		t.Fatalf("expected empty allowed hosts, got %#v", ws.AllowedHosts)
	}
	if len(ws.AllowedNetworks) != 0 {
		t.Fatalf("expected empty allowed networks, got %#v", ws.AllowedNetworks)
	}
	if ws.BuildContext != "" {
		t.Fatalf("expected empty build context, got %q", ws.BuildContext)
	}
}

func TestFilteredDNSValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, upstream string
		zones          []string
		wantError      string
	}{
		{"valid", "10.20.30.53", []string{"corp.example.com", "internal"}, ""},
		{"missing upstream", "", nil, "dns_upstream"},
		{"missing blocked zones", "10.20.30.53", nil, "dns_blocked_zones"},
		{"hostname upstream", "resolver.example.com", []string{"internal"}, "dns_upstream"},
		{"loopback upstream", "127.0.0.1", []string{"internal"}, "dns_upstream"},
		{"link-local upstream", "169.254.53.53", []string{"internal"}, "dns_upstream"},
		{"multicast upstream", "224.0.0.53", []string{"internal"}, "dns_upstream"},
		{"invalid zone", "10.20.30.53", []string{"."}, "dns_blocked_zones"},
		{"injected zone", "10.20.30.53", []string{"corp.example.com\nforward . 1.1.1.1"}, "dns_blocked_zones"},
		{"dotless zone", "10.20.30.53", []string{"internal"}, ""},
		{"valid trailing dot", "10.20.30.53", []string{"internal.example."}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Workspaces: map[string]Workspace{"test": {
				Paths: []string{"/data/work"}, FilteredDNS: new(true), DNSUpstream: tc.upstream, DNSBlockedZones: tc.zones,
			}}}
			err := Validate(cfg)
			if tc.wantError == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestFilteredDNSAllowedHostConflict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		defaultHosts     []string
		workspaceHosts   []string
		defaultZones     []string
		workspaceZones   []string
		workspaceEnabled *bool
		wantHost         string
		wantZone         string
	}{
		{"exact default host", []string{"corp.example"}, nil, []string{"corp.example"}, nil, nil, "corp.example", "corp.example"},
		{"subdomain workspace host", nil, []string{"Api.Corp.Example."}, []string{"corp.example."}, nil, nil, "Api.Corp.Example.", "corp.example."},
		{"inherited host overridden zone", []string{"api.corp.example"}, nil, []string{"internal"}, []string{"corp.example"}, nil, "api.corp.example", "corp.example"},
		{"sibling host", nil, []string{"notcorp.example"}, []string{"corp.example"}, nil, nil, "", ""},
		{"workspace zones replace defaults", []string{"api.corp.example"}, nil, []string{"corp.example"}, []string{"internal"}, nil, "", ""},
		{"disabled workspace", []string{"api.corp.example"}, nil, []string{"corp.example"}, nil, new(false), "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Defaults: Defaults{FilteredDNS: new(true), DNSUpstream: "1.1.1.1", DNSBlockedZones: tc.defaultZones, AllowedHosts: tc.defaultHosts},
				Workspaces: map[string]Workspace{"test": {
					Paths: []string{"/data/work"}, AllowedHosts: tc.workspaceHosts, DNSBlockedZones: tc.workspaceZones, FilteredDNS: tc.workspaceEnabled,
				}},
			}
			err := Validate(cfg)
			if tc.wantHost == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), `workspace "test"`) ||
				!strings.Contains(err.Error(), tc.wantHost) || !strings.Contains(err.Error(), tc.wantZone) ||
				!strings.Contains(err.Error(), "allowed_hosts") || !strings.Contains(err.Error(), "dns_blocked_zones") {
				t.Fatalf("Validate error = %v, want workspace/host/zone conflict", err)
			}
		})
	}
}

func TestLoadFilteredDNSTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[defaults]
dns_upstream = "10.20.30.53"
dns_blocked_zones = ["internal"]

[workspaces.test]
paths = ["/data/work"]
filtered_dns = true
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Workspaces["test"].FilteredDNS == nil || !*cfg.Workspaces["test"].FilteredDNS ||
		cfg.Defaults.DNSUpstream != "10.20.30.53" || len(cfg.Defaults.DNSBlockedZones) != 1 {
		t.Fatalf("filtered DNS settings not loaded: defaults=%+v workspace=%+v", cfg.Defaults, cfg.Workspaces["test"])
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	envGlobal := filepath.Join(dir, "env.global")
	envLocal := filepath.Join(dir, "env.local")
	writeFile(t, envGlobal, "SOME=value\n")
	writeFile(t, envLocal, "OTHER=value\n")

	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, fmt.Sprintf(`
[defaults]
image = "ubuntu:22.04"
env = ["GLOBAL_VAR=value1"]
env_file = [%q]
allowed_hosts = ["global.example.com"]
allowed_networks = ["10.0.0.0/8"]

[base]

[workspaces.default]
image = "alpine:latest"
paths = ["/data/workspace", "/work2"]
allowed_hosts = ["foo.com"]
allowed_networks = ["10.0.0.0/8"]
env = ["LOCAL_VAR=value2"]
env_file = [%q]
`, envGlobal, envLocal))

	first, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("initial LoadFrom failed: %v", err)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(first); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	path2 := filepath.Join(dir, "config2.toml")
	writeFile(t, path2, buf.String())

	second, err := LoadFrom(path2)
	if err != nil {
		t.Fatalf("second LoadFrom failed: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("round-trip mismatch:\nfirst=%#v\nsecond=%#v", first, second)
	}

	if first.Defaults.Image != "ubuntu:22.04" {
		t.Fatalf("expected defaults.image to be 'ubuntu:22.04', got %q", first.Defaults.Image)
	}
	if second.Defaults.Image != "ubuntu:22.04" {
		t.Fatalf("expected second.defaults.image to be 'ubuntu:22.04', got %q", second.Defaults.Image)
	}

	ws := first.Workspaces["default"]
	if ws.Image != "alpine:latest" {
		t.Fatalf("expected workspace.image to be 'alpine:latest', got %q", ws.Image)
	}
	ws2 := second.Workspaces["default"]
	if ws2.Image != "alpine:latest" {
		t.Fatalf("expected second.workspace.image to be 'alpine:latest', got %q", ws2.Image)
	}
}

func TestParseDefaults(t *testing.T) {
	dir := t.TempDir()
	envGlobal := filepath.Join(dir, "env.global")
	envBackup := filepath.Join(dir, "env.backup")
	writeFile(t, envGlobal, "A=1\n")
	writeFile(t, envBackup, "B=2\n")

	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, fmt.Sprintf(`
[defaults]
env = ["GLOBAL_VAR=value1", "GLOBAL_VAR2=value2"]
env_file = [%q, %q]
allowed_hosts = ["api.example.com", "db.example.com"]
allowed_networks = ["10.0.0.0/8", "172.16.0.0/12"]

[workspaces.default]
paths = ["/data/workspace"]
`, envGlobal, envBackup))

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Env, []string{"GLOBAL_VAR=value1", "GLOBAL_VAR2=value2"}) {
		t.Fatalf("unexpected defaults env: %#v", cfg.Defaults.Env)
	}
	if !reflect.DeepEqual(cfg.Defaults.EnvFile, []string{envGlobal, envBackup}) {
		t.Fatalf("unexpected defaults env_file: %#v", cfg.Defaults.EnvFile)
	}
	if !reflect.DeepEqual(cfg.Defaults.AllowedHosts, []string{"api.example.com", "db.example.com"}) {
		t.Fatalf("unexpected defaults allowed_hosts: %#v", cfg.Defaults.AllowedHosts)
	}
	if !reflect.DeepEqual(cfg.Defaults.AllowedNetworks, []string{"10.0.0.0/8", "172.16.0.0/12"}) {
		t.Fatalf("unexpected defaults allowed_networks: %#v", cfg.Defaults.AllowedNetworks)
	}
}

func TestParseWorkspaceEnvFields(t *testing.T) {
	dir := t.TempDir()
	envFile1 := filepath.Join(dir, "env1")
	envFile2 := filepath.Join(dir, "env2")
	writeFile(t, envFile1, "A=1\n")
	writeFile(t, envFile2, "B=2\n")

	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, fmt.Sprintf(`
[workspaces.default]
paths = ["/data/workspace"]
env = ["LOCAL_VAR=value1", "LOCAL_VAR2=value2"]
env_file = [%q, %q]

[workspaces.other]
paths = ["/work"]
env = ["OTHER_VAR=val"]
`, envFile1, envFile2))

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	defaultWs := cfg.Workspaces["default"]
	if !reflect.DeepEqual(defaultWs.Env, []string{"LOCAL_VAR=value1", "LOCAL_VAR2=value2"}) {
		t.Fatalf("unexpected workspace default env: %#v", defaultWs.Env)
	}
	if !reflect.DeepEqual(defaultWs.EnvFile, []string{envFile1, envFile2}) {
		t.Fatalf("unexpected workspace default env_file: %#v", defaultWs.EnvFile)
	}

	otherWs := cfg.Workspaces["other"]
	if !reflect.DeepEqual(otherWs.Env, []string{"OTHER_VAR=val"}) {
		t.Fatalf("unexpected workspace other env: %#v", otherWs.Env)
	}
	if len(otherWs.EnvFile) != 0 {
		t.Fatalf("expected empty env_file for other workspace, got %#v", otherWs.EnvFile)
	}
}

func TestValidateRejectsUppercaseName(t *testing.T) {
	cfg := &Config{
		Workspaces: map[string]Workspace{
			"My Project": {Paths: []string{"/data/workspace"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "My Project") {
		t.Fatalf("expected error to contain workspace name, got: %v", err)
	}
}

func TestValidateAllowsEmptyPaths(t *testing.T) {
	cfg := &Config{
		Workspaces: map[string]Workspace{
			"default": {Paths: []string{}},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error for empty paths, got: %v", err)
	}
}

func TestValidateRejectsInvalidCIDR(t *testing.T) {
	cfg := &Config{
		Workspaces: map[string]Workspace{
			"default": {
				Paths:           []string{"/data/workspace"},
				AllowedNetworks: []string{"999.0.0.0/99"},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "999.0.0.0/99") {
		t.Fatalf("expected error to contain CIDR, got: %v", err)
	}
}

func TestValidateAcceptsValidCIDR(t *testing.T) {
	cfg := &Config{
		Workspaces: map[string]Workspace{
			"default": {
				Paths:           []string{"/data/workspace"},
				AllowedNetworks: []string{"172.20.0.0/16"},
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestLoadMissingFileAutoCreates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if _, err := os.Stat(ConfigPath()); err != nil {
		t.Fatalf("expected config file to be created: %v", err)
	}

	ws, ok := cfg.Workspaces["default"]
	if !ok {
		t.Fatalf("expected default workspace in loaded config: %#v", cfg.Workspaces)
	}
	if len(ws.Paths) != 0 {
		t.Fatalf("expected empty seed paths, got %#v", ws.Paths)
	}
}

func TestLoadExistingEmptyPathsOk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeFile(t, ConfigPath(), `
[workspaces.default]
paths = []
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error for empty paths, got: %v", err)
	}
	ws := cfg.Workspaces["default"]
	if len(ws.Paths) != 0 {
		t.Fatalf("expected empty paths, got %#v", ws.Paths)
	}
}

func TestAddPathPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := CreateDefault(); err != nil {
		t.Fatalf("CreateDefault failed: %v", err)
	}

	if err := AddPath("default", "/data/mywork"); err != nil {
		t.Fatalf("AddPath failed: %v", err)
	}

	cfg, err := LoadFrom(ConfigPath())
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	ws := cfg.Workspaces["default"]
	if len(ws.Paths) != 1 || ws.Paths[0] != "/data/mywork" {
		t.Fatalf("expected persisted path, got %#v", ws.Paths)
	}
}

func TestTildeExpansion(t *testing.T) {
	// Use a safe temp directory outside of /var (macOS puts tmp in /var/folders)
	home := "/data/home_test_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("HOME", home)

	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {
			Paths:        []string{"~/mywork"},
			BuildContext: "~/ctx",
		},
	}}

	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	ws := cfg.Workspaces["default"]
	if ws.Paths[0] != filepath.Join(home, "mywork") {
		t.Fatalf("expected expanded path, got %q", ws.Paths[0])
	}
	if ws.BuildContext != filepath.Join(home, "ctx") {
		t.Fatalf("expected expanded build context, got %q", ws.BuildContext)
	}
}

func TestCreateDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := CreateDefault(); err != nil {
		t.Fatalf("CreateDefault failed: %v", err)
	}

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatalf("read created default config: %v", err)
	}

	if !strings.Contains(string(data), "[workspaces.default]") {
		t.Fatalf("expected default content to include workspace block, got:\n%s", string(data))
	}
}

func TestAddPathDoesNotEmitEmptySecretsHeaderAfterCreateDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := CreateDefault(); err != nil {
		t.Fatalf("CreateDefault failed: %v", err)
	}
	if err := AddPath("default", "/data/mywork"); err != nil {
		t.Fatalf("AddPath failed: %v", err)
	}

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if bytes.Contains(data, []byte("[defaults.secrets")) {
		t.Fatalf("expected no secrets table header in persisted config, got:\n%s", string(data))
	}
}

func TestAddPathDoesNotEmitEmptySecretSourceKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeFile(t, ConfigPath(), `
[defaults.secrets.env.ENV_TOKEN]
from_env = "HOST_ENV_TOKEN"

[defaults.secrets.file.FILE_TOKEN]
from_file = "/abs/token"

[workspaces.default]
paths = ["/data/workspace"]
`)

	if err := AddPath("default", "/data/mywork"); err != nil {
		t.Fatalf("AddPath failed: %v", err)
	}

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if bytes.Contains(data, []byte(`from_file = ""`)) {
		t.Fatalf("expected file secret source key to stay omitted, got:\n%s", string(data))
	}
	if bytes.Contains(data, []byte(`from_env = ""`)) {
		t.Fatalf("expected env secret source key to stay omitted, got:\n%s", string(data))
	}
}

func TestValidateErrorIncludesValue(t *testing.T) {
	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {
			Paths:           []string{"/data/workspace"},
			AllowedNetworks: []string{"999.0.0.0/99"},
		},
	}}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "999.0.0.0/99") {
		t.Fatalf("expected error to include invalid value, got: %v", err)
	}
}

func TestValidateRejectsDangerousPaths(t *testing.T) {
	dangerousPaths := []string{
		"/home/agent/foo",
		"/usr/local",
		"/etc",
		"/home/agent",
		"/var/lib",
		"/bin/sh",
		"/sbin/mount",
		"/lib",
		"/lib64",
		"/opt",
		"/opt/app",
		"/root/secrets",
		"/proc/sys",
		"/sys/kernel",
		"/dev/null",
		"/run/docker",
		"/tmp/test",
		"/certs/ca",
	}

	for _, path := range dangerousPaths {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"default": {Paths: []string{path}},
			},
		}

		err := Validate(cfg)
		if err == nil {
			t.Fatalf("expected validation error for dangerous path %q", path)
		}
		if !strings.Contains(err.Error(), "conflicts with container-internal directory") {
			t.Fatalf("expected error to mention conflict for %q, got: %v", path, err)
		}
	}
}

func TestValidateAllowsSafePaths(t *testing.T) {
	safePaths := []string{
		"/Users/josef/projects",
		"/data/workspace",
		"/home/user/work",
		"/mnt/data",
		"/mnt/storage",
	}

	for _, path := range safePaths {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"default": {Paths: []string{path}},
			},
		}

		if err := Validate(cfg); err != nil {
			t.Fatalf("expected no error for safe path %q, got: %v", path, err)
		}
	}
}

func TestValidateNilConfig(t *testing.T) {
	err := Validate(nil)
	if err == nil {
		t.Fatal("expected validation error for nil config")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("expected error to mention nil, got: %v", err)
	}
}

func TestAddPathToNonexistentWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := CreateDefault(); err != nil {
		t.Fatalf("CreateDefault failed: %v", err)
	}

	err := AddPath("nonexistent", "/data/mywork")
	if err == nil {
		t.Fatal("expected error when adding path to nonexistent workspace")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected error to mention nonexistent workspace, got: %v", err)
	}
}

func TestLoadFromInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.toml")
	writeFile(t, path, `
[invalid toml syntax {{{
paths = ["/data/workspace"
`)

	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected error when loading invalid TOML")
	}
}

func TestValidateRejectsEmptyPathString(t *testing.T) {
	cfg := &Config{
		Workspaces: map[string]Workspace{
			"default": {Paths: []string{"/data/workspace", "", "/data/other"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for empty path string")
	}
	if !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("expected error to mention empty path, got: %v", err)
	}
}

func TestValidateAcceptsDockerfileHTTP(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "http://example.com/Dockerfile",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths:      []string{"/data/workspace"},
				Dockerfile: "http://example.com/ws.Dockerfile",
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAcceptsDockerfileHTTPS(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "https://example.com/Dockerfile",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths:      []string{"/data/workspace"},
				Dockerfile: "https://example.com/ws.Dockerfile",
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAcceptsDockerfileEmpty(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths:      []string{"/data/workspace"},
				Dockerfile: "",
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateRejectsDockerfileEmptyHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		image string
	}{
		{
			name:  "global image",
			image: "http:///Dockerfile",
		},
		{
			name:  "workspace image",
			image: "https:///ws.Dockerfile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cfg *Config
			if tt.name == "global image" {
				cfg = &Config{
					Base: BaseConfig{
						Dockerfile: tt.image,
					},
					Workspaces: map[string]Workspace{
						"default": {
							Paths: []string{"/data/workspace"},
						},
					},
				}
			} else {
				cfg = &Config{
					Workspaces: map[string]Workspace{
						"default": {
							Paths:      []string{"/data/workspace"},
							Dockerfile: tt.image,
						},
					},
				}
			}

			err := Validate(cfg)
			if err == nil {
				t.Fatal("expected error for empty host")
			}
			if !strings.Contains(err.Error(), "scheme must be http or https") {
				t.Fatalf("expected scheme validation error, got: %v", err)
			}
		})
	}
}

func TestValidateRejectsDockerfileFTPScheme(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "ftp://example.com/Dockerfile",
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("expected error for unsupported scheme, got: %v", err)
	}

	cfg2 := &Config{
		Workspaces: map[string]Workspace{
			"default": {
				Paths:      []string{"/data/workspace"},
				Dockerfile: "ftp://example.com/ws.Dockerfile",
			},
		},
	}
	err2 := Validate(cfg2)
	if err2 == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err2.Error(), "must be an absolute path") {
		t.Fatalf("expected error for unsupported scheme, got: %v", err2)
	}
}

func TestValidateAcceptsDockerfileLocalAbsolute(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "/opt/custom/Dockerfile",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths: []string{"/data/workspace"},
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAcceptsDockerfileTildePath(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "~/my.Dockerfile",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths: []string{"/data/workspace"},
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAcceptsWorkspaceDockerfileLocal(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Workspaces: map[string]Workspace{
			"default": {
				Paths:      []string{"/data/workspace"},
				Dockerfile: "/opt/overlay.Dockerfile",
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateRejectsDockerfileRelativePath(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "relative/path",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths: []string{"/data/workspace"},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("expected absolute path error, got: %v", err)
	}
}

func TestValidateRejectsDockerfileBareName(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "justAName",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths: []string{"/data/workspace"},
			},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for bare name")
	}
	if !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("expected absolute path error, got: %v", err)
	}
}

func TestDockerfileNotExpandedByExpandPaths(t *testing.T) {
	home := "/tmp/home"
	t.Setenv("HOME", home)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "local path with tilde is expanded",
			input:    "~/Dockerfile",
			expected: home + "/Dockerfile",
		},
		{
			name:     "https URL is not expanded",
			input:    "https://example.com/Dockerfile",
			expected: "https://example.com/Dockerfile",
		},
		{
			name:     "http URL is not expanded",
			input:    "http://example.com/Dockerfile",
			expected: "http://example.com/Dockerfile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &Workspace{
				Paths:      []string{"~/mywork"},
				Dockerfile: tt.input,
			}

			err := expandPaths(ws)
			if err != nil {
				t.Fatalf("expandPaths failed: %v", err)
			}

			if ws.Dockerfile != tt.expected {
				t.Fatalf("expected %q, got: %q", tt.expected, ws.Dockerfile)
			}
		})
	}
}

func TestDockerfileLocalPathExpandsTilde(t *testing.T) {
	home := "/tmp/home"
	t.Setenv("HOME", home)

	ws := &Workspace{
		Paths:      []string{"/data"},
		Dockerfile: "~/my.Dockerfile",
	}

	err := expandPaths(ws)
	if err != nil {
		t.Fatalf("expandPaths failed: %v", err)
	}

	expected := home + "/my.Dockerfile"
	if ws.Dockerfile != expected {
		t.Fatalf("expected %q, got: %q", expected, ws.Dockerfile)
	}
}

func TestDockerfileHTTPURLNotExpanded(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")

	ws := &Workspace{
		Paths:      []string{"/data"},
		Dockerfile: "https://example.com/Dockerfile",
	}

	err := expandPaths(ws)
	if err != nil {
		t.Fatalf("expandPaths failed: %v", err)
	}

	if ws.Dockerfile != "https://example.com/Dockerfile" {
		t.Fatalf("expected https URL to remain unchanged, got: %q", ws.Dockerfile)
	}
}

func TestImageDockerfileLocalPathExpandsTilde(t *testing.T) {
	home := "/tmp/home"
	t.Setenv("HOME", home)

	cfg := &Config{
		Base: BaseConfig{
			Dockerfile: "~/my.Dockerfile",
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths: []string{"/data/workspace"},
			},
		},
	}

	err := Validate(cfg)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	expected := home + "/my.Dockerfile"
	if cfg.Base.Dockerfile != expected {
		t.Fatalf("expected %q, got: %q", expected, cfg.Base.Dockerfile)
	}
}

func TestValidateEnvFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     []string
		wantErr string
	}{
		{
			name: "valid KEY=VALUE",
			env:  []string{"MY_KEY=my_value"},
		},
		{
			name: "empty value is valid",
			env:  []string{"KEY="},
		},
		{
			name: "value with equals sign",
			env:  []string{"KEY=val=ue"},
		},
		{
			name:    "missing equals sign",
			env:     []string{"NOEQUALS"},
			wantErr: "must be in KEY=VALUE format",
		},
		{
			name:    "empty key",
			env:     []string{"=value"},
			wantErr: "key must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run("defaults/"+tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Defaults: Defaults{Env: tt.env},
			}
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
				}
				if !strings.Contains(err.Error(), "defaults") {
					t.Fatalf("expected error to include context 'defaults', got: %v", err)
				}
			}
		})

		t.Run("workspace/"+tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Workspaces: map[string]Workspace{
					"myws": {Env: tt.env},
				},
			}
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
				}
				if !strings.Contains(err.Error(), "myws") {
					t.Fatalf("expected error to include workspace name 'myws', got: %v", err)
				}
			}
		})
	}
}

func TestValidateEnvReservedKeys(t *testing.T) {
	t.Parallel()

	reserved := []string{
		"NODE_USE_SYSTEM_CA",
		"OPENCODE_TUI_CONFIG",
		"SSH_AUTH_SOCK",
	}

	for _, key := range reserved {
		t.Run("defaults/"+key, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Defaults: Defaults{Env: []string{key + "=anything"}},
			}
			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected error for reserved key %q", key)
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("expected 'reserved' in error, got: %v", err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("expected key %q in error, got: %v", key, err)
			}
		})

		t.Run("workspace/"+key, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Workspaces: map[string]Workspace{
					"myws": {Env: []string{key + "=anything"}},
				},
			}
			err := Validate(cfg)
			if err == nil {
				t.Fatalf("expected error for reserved key %q", key)
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("expected 'reserved' in error, got: %v", err)
			}
			if !strings.Contains(err.Error(), "myws") {
				t.Fatalf("expected workspace name in error, got: %v", err)
			}
		})

		t.Run("defaults/env_file/"+key, func(t *testing.T) {
			t.Parallel()
			f, err := os.CreateTemp(t.TempDir(), "envfile")
			if err != nil {
				t.Fatalf("create temp file: %v", err)
			}
			if _, err := fmt.Fprintf(f, "%s=anything\n", key); err != nil {
				t.Fatalf("write temp file: %v", err)
			}
			_ = f.Close()
			cfg := &Config{
				Defaults: Defaults{EnvFile: []string{f.Name()}},
			}
			err = Validate(cfg)
			if err == nil {
				t.Fatalf("expected error for reserved key %q in env_file", key)
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("expected 'reserved' in error, got: %v", err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("expected key %q in error, got: %v", key, err)
			}
		})

		t.Run("workspace/env_file/"+key, func(t *testing.T) {
			t.Parallel()
			f, err := os.CreateTemp(t.TempDir(), "envfile")
			if err != nil {
				t.Fatalf("create temp file: %v", err)
			}
			if _, err := fmt.Fprintf(f, "%s=anything\n", key); err != nil {
				t.Fatalf("write temp file: %v", err)
			}
			_ = f.Close()
			cfg := &Config{
				Workspaces: map[string]Workspace{
					"myws": {EnvFile: []string{f.Name()}},
				},
			}
			err = Validate(cfg)
			if err == nil {
				t.Fatalf("expected error for reserved key %q in env_file", key)
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("expected 'reserved' in error, got: %v", err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("expected key %q in error, got: %v", key, err)
			}
		})
	}
}

func TestValidateEnvFileRelativePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{
			name:    "relative path",
			path:    "relative/path.env",
			wantErr: "must be absolute",
		},
		{
			name:    "bare filename",
			path:    "file.env",
			wantErr: "must be absolute",
		},
	}

	for _, tt := range tests {
		t.Run("defaults/"+tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Defaults: Defaults{EnvFile: []string{tt.path}},
			}
			err := Validate(cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
			if !strings.Contains(err.Error(), "defaults") {
				t.Fatalf("expected 'defaults' context in error, got: %v", err)
			}
		})

		t.Run("workspace/"+tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Workspaces: map[string]Workspace{
					"myws": {EnvFile: []string{tt.path}},
				},
			}
			err := Validate(cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
			if !strings.Contains(err.Error(), "myws") {
				t.Fatalf("expected workspace name in error, got: %v", err)
			}
		})
	}
}

func TestValidateEnvFileNonExistent(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Defaults: Defaults{EnvFile: []string{"/nonexistent/path/file.env"}},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("expected 'does not exist' in error, got: %v", err)
		}
	})

	t.Run("workspace", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"myws": {EnvFile: []string{"/nonexistent/path/file.env"}},
			},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("expected 'does not exist' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "myws") {
			t.Fatalf("expected workspace name in error, got: %v", err)
		}
	})
}

func TestValidateEnvFileExistingPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envFile := filepath.Join(dir, "valid.env")
	writeFile(t, envFile, "KEY=val\n")

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Defaults: Defaults{EnvFile: []string{envFile}},
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("workspace", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"myws": {EnvFile: []string{envFile}},
			},
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestExpandPathsTildeEnvFile(t *testing.T) {
	home := "/data/home_test_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("HOME", home)

	t.Run("defaults", func(t *testing.T) {
		cfg := &Config{
			Defaults: Defaults{EnvFile: []string{"~/my.env"}},
		}
		for i, p := range cfg.Defaults.EnvFile {
			expanded, err := ExpandPath(p)
			if err != nil {
				t.Fatalf("ExpandPath failed: %v", err)
			}
			cfg.Defaults.EnvFile[i] = expanded
		}
		if cfg.Defaults.EnvFile[0] != home+"/my.env" {
			t.Fatalf("expected expanded path, got %q", cfg.Defaults.EnvFile[0])
		}
	})

	t.Run("workspace", func(t *testing.T) {
		ws := &Workspace{
			Paths:   []string{"/data"},
			EnvFile: []string{"~/ws.env"},
		}
		if err := expandPaths(ws); err != nil {
			t.Fatalf("expandPaths failed: %v", err)
		}
		if ws.EnvFile[0] != home+"/ws.env" {
			t.Fatalf("expected expanded path, got %q", ws.EnvFile[0])
		}
	})
}

func TestExpandPathsNormalizesContainerPath(t *testing.T) {
	home := "/data/home_test_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("HOME", home)

	tests := []struct {
		name      string
		spec      string
		wantMount string
	}{
		{
			name:      "trailing slash removed",
			spec:      "/host/dir:/container/path/:ro",
			wantMount: "/host/dir:/container/path:ro",
		},
		{
			name:      "dot segment cleaned",
			spec:      "/host/dir:/container/./path:rw",
			wantMount: "/host/dir:/container/path:rw",
		},
		{
			name:      "double slash cleaned",
			spec:      "/host/dir:/container//path:ro",
			wantMount: "/host/dir:/container/path:ro",
		},
		{
			name:      "tilde and traversal cleaned",
			spec:      "~/data:/home/agent/../agent/data:rw",
			wantMount: home + "/data:/home/agent/data:rw",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &Workspace{
				Paths:  []string{"/data"},
				Mounts: []string{tt.spec},
			}
			if err := expandPaths(ws); err != nil {
				t.Fatalf("expandPaths failed: %v", err)
			}
			if ws.Mounts[0] != tt.wantMount {
				t.Fatalf("got %q, want %q", ws.Mounts[0], tt.wantMount)
			}
		})
	}
}

func TestValidateImageAndDockerfileMutualExclusivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		image       string
		dockerfile  string
		buildCtx    string
		shouldError bool
		errorText   string
	}{
		{
			name:        "image + dockerfile conflict",
			image:       "nginx:latest",
			dockerfile:  "/Dockerfile",
			buildCtx:    "",
			shouldError: true,
			errorText:   "cannot set both \"image\" and \"dockerfile\"",
		},
		{
			name:        "image + build_context conflict",
			image:       "nginx:latest",
			dockerfile:  "",
			buildCtx:    "/build",
			shouldError: true,
			errorText:   "cannot set both \"image\" and \"build_context\"",
		},
		{
			name:        "image only is valid",
			image:       "nginx:latest",
			dockerfile:  "",
			buildCtx:    "",
			shouldError: false,
			errorText:   "",
		},
		{
			name:        "dockerfile only is valid",
			image:       "",
			dockerfile:  "/Dockerfile",
			buildCtx:    "",
			shouldError: false,
			errorText:   "",
		},
		{
			name:        "build_context only is valid",
			image:       "",
			dockerfile:  "",
			buildCtx:    "/build",
			shouldError: false,
			errorText:   "",
		},
		{
			name:        "all three set triggers dockerfile+image error first",
			image:       "nginx:latest",
			dockerfile:  "/Dockerfile",
			buildCtx:    "/build",
			shouldError: true,
			errorText:   "cannot set both \"image\" and \"dockerfile\"",
		},
		{
			name:        "dockerfile + build_context is valid",
			image:       "",
			dockerfile:  "/Dockerfile",
			buildCtx:    "/build",
			shouldError: false,
			errorText:   "",
		},
		{
			name:        "image_whitespace_only",
			image:       "   ",
			dockerfile:  "",
			buildCtx:    "",
			shouldError: true,
			errorText:   "\"image\" must not be empty or whitespace-only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Workspaces: map[string]Workspace{
					"test": {
						Paths:        []string{"/data"},
						Image:        tt.image,
						Dockerfile:   tt.dockerfile,
						BuildContext: tt.buildCtx,
					},
				},
			}

			err := Validate(cfg)

			if tt.shouldError {
				if err == nil {
					t.Fatal("expected validation error")
				}
				if !strings.Contains(err.Error(), tt.errorText) {
					t.Fatalf("expected error to contain %q, got: %v", tt.errorText, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateEnvReservedSSHAuthSock(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Defaults: Defaults{Env: []string{"SSH_AUTH_SOCK=/tmp/sock"}},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for reserved SSH_AUTH_SOCK key")
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("expected 'reserved' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
			t.Fatalf("expected 'SSH_AUTH_SOCK' in error, got: %v", err)
		}
	})

	t.Run("workspace", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"myws": {Env: []string{"SSH_AUTH_SOCK=/tmp/sock"}},
			},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for reserved SSH_AUTH_SOCK key")
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("expected 'reserved' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "myws") {
			t.Fatalf("expected workspace name in error, got: %v", err)
		}
	})
}

func TestValidateEnvReservedJailoc(t *testing.T) {
	t.Parallel()

	t.Run("JAILOC in defaults", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Defaults: Defaults{Env: []string{"JAILOC=custom"}},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for reserved JAILOC key")
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("expected 'reserved' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "JAILOC") {
			t.Fatalf("expected 'JAILOC' in error, got: %v", err)
		}
	})

	t.Run("JAILOC_WORKSPACE in defaults", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Defaults: Defaults{Env: []string{"JAILOC_WORKSPACE=foo"}},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for reserved JAILOC_WORKSPACE key")
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("expected 'reserved' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "JAILOC_WORKSPACE") {
			t.Fatalf("expected 'JAILOC_WORKSPACE' in error, got: %v", err)
		}
	})

	t.Run("JAILOC in workspace", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"myws": {Env: []string{"JAILOC=custom"}},
			},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for reserved JAILOC key")
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("expected 'reserved' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "JAILOC") {
			t.Fatalf("expected 'JAILOC' in error, got: %v", err)
		}
	})

	t.Run("JAILOC_WORKSPACE in workspace", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"myws": {Env: []string{"JAILOC_WORKSPACE=bar"}},
			},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected error for reserved JAILOC_WORKSPACE key")
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("expected 'reserved' in error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "JAILOC_WORKSPACE") {
			t.Fatalf("expected 'JAILOC_WORKSPACE' in error, got: %v", err)
		}
	})
}

func TestLoadSSHGitFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tomlContent string
		checkFn     func(*testing.T, *Config)
	}{
		{
			name: "defaults ssh_auth_sock true",
			tomlContent: `
[defaults]
ssh_auth_sock = true

[workspaces.default]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if !cfg.Defaults.SSHAuthSock {
					t.Fatal("expected defaults.ssh_auth_sock = true")
				}
			},
		},
		{
			name: "defaults git_config true",
			tomlContent: `
[defaults]
git_config = true

[workspaces.default]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.GitConfig == nil || !*cfg.Defaults.GitConfig {
					t.Fatal("expected defaults.git_config = true")
				}
			},
		},
		{
			name: "defaults git_config false",
			tomlContent: `
[defaults]
git_config = false

[workspaces.default]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.GitConfig == nil || *cfg.Defaults.GitConfig {
					t.Fatal("expected defaults.git_config = false")
				}
			},
		},
		{
			name: "defaults git_config nil when omitted",
			tomlContent: `
[workspaces.default]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.GitConfig != nil {
					t.Fatalf("expected defaults.git_config = nil, got %v", *cfg.Defaults.GitConfig)
				}
			},
		},
		{
			name: "defaults ssh_auth_sock false by default",
			tomlContent: `
[workspaces.default]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.SSHAuthSock {
					t.Fatal("expected defaults.ssh_auth_sock = false by default")
				}
			},
		},
		{
			name: "workspace ssh_auth_sock pointer set true",
			tomlContent: `
[workspaces.default]
paths = ["/data"]
ssh_auth_sock = true
`,
			checkFn: func(t *testing.T, cfg *Config) {
				ws := cfg.Workspaces["default"]
				if ws.SSHAuthSock == nil || !*ws.SSHAuthSock {
					t.Fatal("expected workspace.ssh_auth_sock = true")
				}
			},
		},
		{
			name: "workspace ssh_auth_sock pointer set false",
			tomlContent: `
[workspaces.default]
paths = ["/data"]
ssh_auth_sock = false
`,
			checkFn: func(t *testing.T, cfg *Config) {
				ws := cfg.Workspaces["default"]
				if ws.SSHAuthSock == nil {
					t.Fatal("expected workspace.ssh_auth_sock to be non-nil")
				}
				if *ws.SSHAuthSock {
					t.Fatal("expected workspace.ssh_auth_sock = false")
				}
			},
		},
		{
			name: "workspace ssh_auth_sock pointer nil when omitted",
			tomlContent: `
[workspaces.default]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				ws := cfg.Workspaces["default"]
				if ws.SSHAuthSock != nil {
					t.Fatalf("expected workspace.ssh_auth_sock = nil, got %v", *ws.SSHAuthSock)
				}
				if ws.GitConfig != nil {
					t.Fatalf("expected workspace.git_config = nil, got %v", *ws.GitConfig)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			writeFile(t, path, tt.tomlContent)

			cfg, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("LoadFrom failed: %v", err)
			}

			tt.checkFn(t, cfg)
		})
	}
}

func TestRoundTripSSHGitFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[defaults]
ssh_auth_sock = true
git_config = true

[workspaces.default]
paths = ["/data"]
ssh_auth_sock = false
git_config = true
`)

	first, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("initial LoadFrom failed: %v", err)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(first); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	path2 := filepath.Join(dir, "config2.toml")
	writeFile(t, path2, buf.String())

	second, err := LoadFrom(path2)
	if err != nil {
		t.Fatalf("second LoadFrom failed: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("round-trip mismatch:\nfirst=%#v\nsecond=%#v", first, second)
	}

	if !first.Defaults.SSHAuthSock {
		t.Fatal("expected defaults.ssh_auth_sock = true after round-trip")
	}
	if first.Defaults.GitConfig == nil || !*first.Defaults.GitConfig {
		t.Fatal("expected defaults.git_config = true after round-trip")
	}

	ws := first.Workspaces["default"]
	if ws.SSHAuthSock == nil || *ws.SSHAuthSock {
		t.Fatal("expected workspace.ssh_auth_sock = false after round-trip")
	}
	if ws.GitConfig == nil || !*ws.GitConfig {
		t.Fatal("expected workspace.git_config = true after round-trip")
	}
}

func TestLoadImageFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		tomlContent    string
		checkDefaults  func(*testing.T, *Config)
		checkWorkspace func(*testing.T, *Config)
	}{
		{
			name: "defaults.image loads correctly",
			tomlContent: `
[defaults]
image = "ubuntu:22.04"

[base]

[workspaces.test]
paths = ["/data"]
`,
			checkDefaults: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.Image != "ubuntu:22.04" {
					t.Fatalf("expected defaults.image = 'ubuntu:22.04', got %q", cfg.Defaults.Image)
				}
			},
			checkWorkspace: func(t *testing.T, cfg *Config) {
				ws := cfg.Workspaces["test"]
				if ws.Image != "" {
					t.Fatalf("expected workspace.image to be empty, got %q", ws.Image)
				}
			},
		},
		{
			name: "workspace.image loads correctly",
			tomlContent: `
[defaults]

[base]

[workspaces.test]
paths = ["/data"]
image = "alpine:latest"
`,
			checkDefaults: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.Image != "" {
					t.Fatalf("expected defaults.image to be empty, got %q", cfg.Defaults.Image)
				}
			},
			checkWorkspace: func(t *testing.T, cfg *Config) {
				ws := cfg.Workspaces["test"]
				if ws.Image != "alpine:latest" {
					t.Fatalf("expected workspace.image = 'alpine:latest', got %q", ws.Image)
				}
			},
		},
		{
			name: "defaults and workspace images are independent",
			tomlContent: `
[defaults]
image = "ubuntu:22.04"

[base]

[workspaces.test]
paths = ["/data"]
image = "alpine:latest"
`,
			checkDefaults: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.Image != "ubuntu:22.04" {
					t.Fatalf("expected defaults.image = 'ubuntu:22.04', got %q", cfg.Defaults.Image)
				}
			},
			checkWorkspace: func(t *testing.T, cfg *Config) {
				ws := cfg.Workspaces["test"]
				if ws.Image != "alpine:latest" {
					t.Fatalf("expected workspace.image = 'alpine:latest', got %q", ws.Image)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			writeFile(t, path, tt.tomlContent)

			cfg, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("LoadFrom failed: %v", err)
			}

			tt.checkDefaults(t, cfg)
			tt.checkWorkspace(t, cfg)
		})
	}
}

func TestParseMountValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		spec      string
		host      string
		container string
		mode      string
	}{
		{
			name:      "host and container with implicit rw",
			spec:      "/host/path:/container/path",
			host:      "/host/path",
			container: "/container/path",
			mode:      "rw",
		},
		{
			name:      "host container ro",
			spec:      "/host:/container:ro",
			host:      "/host",
			container: "/container",
			mode:      "ro",
		},
		{
			name:      "removal implicit rw",
			spec:      ":/container/path",
			host:      "",
			container: "/container/path",
			mode:      "rw",
		},
		{
			name:      "removal explicit ro",
			spec:      ":/container:ro",
			host:      "",
			container: "/container",
			mode:      "ro",
		},
		{
			name:      "tilde host",
			spec:      "~/.config/oc:/home/agent/.config/oc:ro",
			host:      "~/.config/oc",
			container: "/home/agent/.config/oc",
			mode:      "ro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseMount(tt.spec)
			if err != nil {
				t.Fatalf("ParseMount(%q) failed: %v", tt.spec, err)
			}

			if got.Host != tt.host || got.Container != tt.container || got.Mode != tt.mode {
				t.Fatalf("ParseMount(%q) = %#v, want host=%q container=%q mode=%q", tt.spec, got, tt.host, tt.container, tt.mode)
			}
		})
	}
}

func TestParseMountInvalid(t *testing.T) {
	t.Parallel()

	invalidSpecs := []string{
		"",
		"/only-one-part",
		"/a:/b:invalid",
		"/a:/b:ro:extra",
		"/host:relative/path",
		"relative-host:/container:rw",
	}

	for _, spec := range invalidSpecs {
		t.Run(spec, func(t *testing.T) {
			t.Parallel()

			if _, err := ParseMount(spec); err == nil {
				t.Fatalf("expected ParseMount(%q) to fail", spec)
			}
		})
	}
}

func TestParseMountWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only path parsing test")
	}
	t.Parallel()

	valid := []struct {
		name      string
		spec      string
		host      string
		container string
		mode      string
	}{
		{
			name:      "backslash path",
			spec:      `C:\Users\foo:/container:rw`,
			host:      `C:\Users\foo`,
			container: "/container",
			mode:      "rw",
		},
		{
			name:      "forward slash path",
			spec:      "D:/projects/app:/home/agent/app:ro",
			host:      "D:/projects/app",
			container: "/home/agent/app",
			mode:      "ro",
		},
		{
			name:      "implicit rw",
			spec:      `C:\Users\foo\project:/home/agent/project`,
			host:      `C:\Users\foo\project`,
			container: "/home/agent/project",
			mode:      "rw",
		},
	}

	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseMount(tt.spec)
			if err != nil {
				t.Fatalf("ParseMount(%q) failed: %v", tt.spec, err)
			}

			if got.Host != tt.host || got.Container != tt.container || got.Mode != tt.mode {
				t.Fatalf("ParseMount(%q) = %#v, want host=%q container=%q mode=%q", tt.spec, got, tt.host, tt.container, tt.mode)
			}
		})
	}

	invalid := []string{
		`C:\a:/b:invalid`,
		`C:\a:/b:ro:extra`,
	}

	for _, spec := range invalid {
		t.Run(spec, func(t *testing.T) {
			t.Parallel()

			if _, err := ParseMount(spec); err == nil {
				t.Fatalf("expected ParseMount(%q) to fail", spec)
			}
		})
	}
}

func TestMergeMounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultMounts := []string{
		"~/.config/opencode:/home/agent/.config/opencode:rw",
		"~/.opencode:/home/agent/.opencode:rw",
		"~/.agents:/home/agent/.agents:ro",
	}

	tests := []struct {
		name     string
		layers   [][]string
		expected []string
	}{
		{
			name: "default mounts only",
			layers: [][]string{
				defaultMounts,
			},
			expected: []string{
				filepath.Join(home, ".config", "opencode") + ":/home/agent/.config/opencode:rw",
				filepath.Join(home, ".opencode") + ":/home/agent/.opencode:rw",
				filepath.Join(home, ".agents") + ":/home/agent/.agents:ro",
			},
		},
		{
			name: "override default mount mode",
			layers: [][]string{
				defaultMounts,
				{"~/cfg:/home/agent/.config/opencode:rw"},
			},
			expected: []string{
				filepath.Join(home, "cfg") + ":/home/agent/.config/opencode:rw",
				filepath.Join(home, ".opencode") + ":/home/agent/.opencode:rw",
				filepath.Join(home, ".agents") + ":/home/agent/.agents:ro",
			},
		},
		{
			name: "remove default mount",
			layers: [][]string{
				defaultMounts,
				{":/home/agent/.opencode"},
			},
			expected: []string{
				filepath.Join(home, ".config", "opencode") + ":/home/agent/.config/opencode:rw",
				filepath.Join(home, ".agents") + ":/home/agent/.agents:ro",
			},
		},
		{
			name: "add new mount",
			layers: [][]string{
				defaultMounts,
				{"~/my-data:/workspace/data:rw"},
			},
			expected: []string{
				filepath.Join(home, ".config", "opencode") + ":/home/agent/.config/opencode:rw",
				filepath.Join(home, ".opencode") + ":/home/agent/.opencode:rw",
				filepath.Join(home, ".agents") + ":/home/agent/.agents:ro",
				filepath.Join(home, "my-data") + ":/workspace/data:rw",
			},
		},
		{
			name: "multiple layers overrides and removals",
			layers: [][]string{
				defaultMounts,
				{"~/cfg:/home/agent/.config/opencode:rw", "~/x:/x:rw"},
				{":/home/agent/.agents", "~/y:/x:ro"},
			},
			expected: []string{
				filepath.Join(home, "cfg") + ":/home/agent/.config/opencode:rw",
				filepath.Join(home, ".opencode") + ":/home/agent/.opencode:rw",
				filepath.Join(home, "y") + ":/x:ro",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeMounts(tt.layers...)
			if err != nil {
				t.Fatalf("MergeMounts() returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Fatalf("MergeMounts() = %#v, want %#v", got, tt.expected)
			}
		})
	}
}

func TestMergeMountsInvalidSpec(t *testing.T) {
	t.Parallel()

	_, err := MergeMounts([]string{"relative-host:/container:rw"})
	if err == nil {
		t.Fatal("expected MergeMounts to fail for invalid mount spec")
	}
}

func TestValidateMountsHostPathValidation(t *testing.T) {
	safeHome(t)

	tests := []struct {
		name      string
		spec      string
		wantError bool
	}{
		{
			name:      "sibling path does not match forbidden prefix",
			spec:      "~/.ssh-other:/container:ro",
			wantError: false,
		},
		{
			name:      "traversal into forbidden path is rejected",
			spec:      "~/.config/../.ssh:/container:ro",
			wantError: true,
		},
		{
			name:      "exact forbidden path is rejected",
			spec:      "~/.ssh:/container:ro",
			wantError: true,
		},
		{
			name:      "forbidden subpath is rejected",
			spec:      "~/.ssh/keys:/container:ro",
			wantError: true,
		},
		{
			name:      "root filesystem is rejected",
			spec:      "/:/container:ro",
			wantError: true,
		},
		{
			name:      "/boot is rejected",
			spec:      "/boot:/container:ro",
			wantError: true,
		},
		{
			name:      "/dev is rejected",
			spec:      "/dev:/container:ro",
			wantError: true,
		},
		{
			name:      "/proc is rejected",
			spec:      "/proc:/container:ro",
			wantError: true,
		},
		{
			name:      "/sys is rejected",
			spec:      "/sys:/container:ro",
			wantError: true,
		},
		{
			name:      "/run is rejected",
			spec:      "/run:/container:ro",
			wantError: true,
		},
		{
			name:      "/var is rejected",
			spec:      "/var:/container:ro",
			wantError: true,
		},
		{
			name:      "/etc is rejected",
			spec:      "/etc:/container:ro",
			wantError: true,
		},
		{
			name:      "/etc subpath is rejected",
			spec:      "/etc/shadow:/container:ro",
			wantError: true,
		},
		{
			name:      "/private/etc bypasses /etc via symlink on macOS",
			spec:      "/private/etc:/container:ro",
			wantError: true,
		},
		{
			name:      "/private/var bypasses /var via symlink on macOS",
			spec:      "/private/var:/container:ro",
			wantError: true,
		},
		{
			name:      "/private/var subpath is rejected",
			spec:      "/private/var/run:/container:ro",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Workspaces: map[string]Workspace{
					"default": {
						Paths:  []string{"/data/workspace"},
						Mounts: []string{tt.spec},
					},
				},
			}

			err := Validate(cfg)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected validation to fail for mount %q", tt.spec)
				}
				if !strings.Contains(err.Error(), "forbidden") {
					t.Fatalf("expected forbidden error, got: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("expected validation to pass for mount %q, got: %v", tt.spec, err)
			}
		})
	}
}

func TestValidateMountsAcceptsSafe(t *testing.T) {
	safeHome(t)

	cfg := &Config{
		Defaults: Defaults{
			Mounts: []string{"~/safe:/workspace/safe:rw"},
		},
		Workspaces: map[string]Workspace{
			"default": {
				Paths:  []string{"/data/workspace"},
				Mounts: []string{"~/other:/workspace/other:ro"},
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected safe mounts to pass validation, got: %v", err)
	}
}

func TestValidateMountsInDefaults(t *testing.T) {
	home := safeHome(t)

	cfg := &Config{
		Defaults: Defaults{
			Mounts: []string{"~/cfg:/workspace/cfg:ro"},
		},
		Workspaces: map[string]Workspace{
			"default": {Paths: []string{"/data/workspace"}},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected defaults.mounts validation to pass, got: %v", err)
	}

	if got := cfg.Defaults.Mounts[0]; !strings.HasPrefix(got, home+"/") {
		t.Fatalf("expected defaults.mounts host to be expanded, got: %q", got)
	}
}

func TestValidateMountsInDefaultsRejectsForbiddenContainerPath(t *testing.T) {
	safeHome(t)

	cfg := &Config{
		Defaults: Defaults{
			Mounts: []string{"~/safe:/etc/inside:ro"},
		},
		Workspaces: map[string]Workspace{
			"default": {Paths: []string{"/data/workspace"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected defaults mount container path to fail")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected not allowed error, got: %v", err)
	}
}

func TestValidateMountsInDefaultsRejectsContainerTraversal(t *testing.T) {
	safeHome(t)

	cfg := &Config{
		Defaults: Defaults{
			Mounts: []string{"~/safe:/home/agent/../../etc:ro"},
		},
		Workspaces: map[string]Workspace{
			"default": {Paths: []string{"/data/workspace"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected defaults container path traversal to fail")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected not allowed error, got: %v", err)
	}
}

func TestValidateMountsInDefaultsRejectsRootContainerPath(t *testing.T) {
	safeHome(t)

	cfg := &Config{
		Defaults: Defaults{
			Mounts: []string{"~/safe:/:rw"},
		},
		Workspaces: map[string]Workspace{
			"default": {Paths: []string{"/data/workspace"}},
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected defaults root container path to fail")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected not allowed error, got: %v", err)
	}
}

func TestValidateMountsInWorkspace(t *testing.T) {
	safeHome(t)

	t.Run("valid workspace mounts", func(t *testing.T) {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"default": {
					Paths:  []string{"/data/workspace"},
					Mounts: []string{"~/safe:/workspace/safe:rw"},
				},
			},
		}

		if err := Validate(cfg); err != nil {
			t.Fatalf("expected workspace.mounts validation to pass, got: %v", err)
		}
	})

	t.Run("forbidden container prefix", func(t *testing.T) {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"default": {
					Paths:  []string{"/data/workspace"},
					Mounts: []string{"~/safe:/etc/inside:ro"},
				},
			},
		}

		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected forbidden workspace mount container path to fail")
		}
		if !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("home agent config path is allowed", func(t *testing.T) {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"default": {
					Paths:  []string{"/data/workspace"},
					Mounts: []string{"~/safe:/home/agent/.config/opencode:rw"},
				},
			},
		}

		if err := Validate(cfg); err != nil {
			t.Fatalf("expected /home/agent mount override to be allowed, got: %v", err)
		}
	})

	t.Run("container path traversal blocked", func(t *testing.T) {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"default": {
					Paths:  []string{"/data/workspace"},
					Mounts: []string{"~/safe:/home/agent/../../etc:ro"},
				},
			},
		}

		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected container path traversal to be blocked")
		}
		if !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("root container path blocked", func(t *testing.T) {
		cfg := &Config{
			Workspaces: map[string]Workspace{
				"default": {
					Paths:  []string{"/data/workspace"},
					Mounts: []string{"~/safe:/:rw"},
				},
			},
		}

		err := Validate(cfg)
		if err == nil {
			t.Fatal("expected root container path to be blocked")
		}
		if !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestValidateErrorMessages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envFile := filepath.Join(dir, "valid.env")
	writeFile(t, envFile, "KEY=val\n")

	tests := []struct {
		name        string
		cfg         *Config
		wantSubstrs []string // all substrings must appear in the error message
	}{
		{
			name: "invalid workspace name includes the name",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"My Project": {Paths: []string{"/data"}},
				},
			},
			wantSubstrs: []string{"My Project", "must match"},
		},
		{
			name: "invalid CIDR includes workspace name and value",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Paths: []string{"/data"}, AllowedNetworks: []string{"not-a-cidr"}},
				},
			},
			wantSubstrs: []string{"myws", "not-a-cidr"},
		},
		{
			name: "forbidden path includes workspace name, path, and prefix",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Paths: []string{"/usr/local/bin"}},
				},
			},
			wantSubstrs: []string{"myws", "/usr/local/bin", "conflicts with container-internal directory"},
		},
		{
			name: "empty path string includes workspace name",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Paths: []string{"/data", ""}},
				},
			},
			wantSubstrs: []string{"myws", "empty path"},
		},
		{
			name: "invalid mode includes the value",
			cfg: &Config{
				Mode:       "banana",
				Workspaces: map[string]Workspace{},
			},
			wantSubstrs: []string{"mode", "jailoc migrate"},
		},
		{
			name: "env missing equals includes workspace and entry",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Env: []string{"NO_EQUALS"}},
				},
			},
			wantSubstrs: []string{"myws", "NO_EQUALS", "KEY=VALUE"},
		},
		{
			name: "env empty key includes workspace and entry",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Env: []string{"=value"}},
				},
			},
			wantSubstrs: []string{"myws", "=value", "key must not be empty"},
		},
		{
			name: "env reserved key includes workspace and key name",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Env: []string{"SSH_AUTH_SOCK=/tmp/agent.sock"}},
				},
			},
			wantSubstrs: []string{"myws", "SSH_AUTH_SOCK", "reserved"},
		},
		{
			name: "env_file relative path includes workspace and path",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {EnvFile: []string{"relative/path.env"}},
				},
			},
			wantSubstrs: []string{"myws", "relative/path.env", "must be absolute"},
		},
		{
			name: "env_file nonexistent includes workspace and path",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {EnvFile: []string{"/nonexistent/file.env"}},
				},
			},
			wantSubstrs: []string{"myws", "/nonexistent/file.env", "does not exist"},
		},
		{
			name: "defaults env missing equals includes context",
			cfg: &Config{
				Defaults: Defaults{Env: []string{"NOEQUALS"}},
			},
			wantSubstrs: []string{"defaults", "NOEQUALS", "KEY=VALUE"},
		},
		{
			name: "defaults env reserved key includes context and key",
			cfg: &Config{
				Defaults: Defaults{Env: []string{"NODE_USE_SYSTEM_CA=0"}},
			},
			wantSubstrs: []string{"defaults", "NODE_USE_SYSTEM_CA", "reserved"},
		},
		{
			name: "defaults env_file relative path includes context",
			cfg: &Config{
				Defaults: Defaults{EnvFile: []string{"relative.env"}},
			},
			wantSubstrs: []string{"defaults", "relative.env", "must be absolute"},
		},
		{
			name: "image and dockerfile conflict includes workspace",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Paths: []string{"/data"}, Image: "nginx:latest", Dockerfile: "/Dockerfile"},
				},
			},
			wantSubstrs: []string{"myws", "cannot set both", "image", "dockerfile"},
		},
		{
			name: "image and build_context conflict includes workspace",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Paths: []string{"/data"}, Image: "nginx:latest", BuildContext: "/build"},
				},
			},
			wantSubstrs: []string{"myws", "cannot set both", "image", "build_context"},
		},
		{
			name: "whitespace-only image includes workspace",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Paths: []string{"/data"}, Image: "   "},
				},
			},
			wantSubstrs: []string{"myws", "image", "must not be empty or whitespace-only"},
		},
		{
			name: "dockerfile relative path includes workspace",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"myws": {Paths: []string{"/data"}, Dockerfile: "relative/Dockerfile"},
				},
			},
			wantSubstrs: []string{"myws", "must be an absolute path"},
		},
		{
			name: "base dockerfile invalid URL includes field",
			cfg: &Config{
				Base:       BaseConfig{Dockerfile: "http:///no-host"},
				Workspaces: map[string]Workspace{},
			},
			wantSubstrs: []string{"base dockerfile", "scheme must be http or https"},
		},
		{
			name:        "nil config",
			cfg:         nil,
			wantSubstrs: []string{"nil"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := Validate(tt.cfg)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}

			msg := err.Error()
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(msg, sub) {
					t.Errorf("error message %q missing expected substring %q", msg, sub)
				}
			}
		})
	}
}

func TestConfigCPUMemoryParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		checkFn func(t *testing.T, cfg *Config)
	}{
		{
			name: "defaults cpu and memory",
			content: `
[defaults]
cpu = 1.5
memory = "8g"

[workspaces.x]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.CPU == nil {
					t.Fatal("expected Defaults.CPU to be non-nil")
				}
				if *cfg.Defaults.CPU != 1.5 {
					t.Errorf("expected CPU 1.5, got %v", *cfg.Defaults.CPU)
				}
				if cfg.Defaults.Memory == nil {
					t.Fatal("expected Defaults.Memory to be non-nil")
				}
				if *cfg.Defaults.Memory != "8g" {
					t.Errorf("expected Memory \"8g\", got %q", *cfg.Defaults.Memory)
				}
			},
		},
		{
			name: "workspace cpu and memory",
			content: `
[workspaces.test]
paths = ["/data"]
cpu = 4.0
memory = "16g"
`,
			checkFn: func(t *testing.T, cfg *Config) {
				ws := cfg.Workspaces["test"]
				if ws.CPU == nil {
					t.Fatal("expected Workspace.CPU to be non-nil")
				}
				if *ws.CPU != 4.0 {
					t.Errorf("expected CPU 4.0, got %v", *ws.CPU)
				}
				if ws.Memory == nil {
					t.Fatal("expected Workspace.Memory to be non-nil")
				}
				if *ws.Memory != "16g" {
					t.Errorf("expected Memory \"16g\", got %q", *ws.Memory)
				}
			},
		},
		{
			name: "no cpu and memory",
			content: `
[workspaces.x]
paths = ["/data"]
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.CPU != nil {
					t.Errorf("expected Defaults.CPU to be nil, got %v", cfg.Defaults.CPU)
				}
				if cfg.Defaults.Memory != nil {
					t.Errorf("expected Defaults.Memory to be nil, got %q", *cfg.Defaults.Memory)
				}
				ws := cfg.Workspaces["x"]
				if ws.CPU != nil {
					t.Errorf("expected Workspace.CPU to be nil, got %v", ws.CPU)
				}
				if ws.Memory != nil {
					t.Errorf("expected Workspace.Memory to be nil, got %q", *ws.Memory)
				}
			},
		},
		{
			name: "defaults and workspace independently",
			content: `
[defaults]
cpu = 2.0
memory = "4g"

[workspaces.prod]
paths = ["/data"]
cpu = 8.0
memory = "32g"
`,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Defaults.CPU == nil || *cfg.Defaults.CPU != 2.0 {
					t.Errorf("expected Defaults.CPU 2.0, got %v", cfg.Defaults.CPU)
				}
				if cfg.Defaults.Memory == nil || *cfg.Defaults.Memory != "4g" {
					t.Errorf("expected Defaults.Memory \"4g\", got %v", cfg.Defaults.Memory)
				}
				ws := cfg.Workspaces["prod"]
				if ws.CPU == nil || *ws.CPU != 8.0 {
					t.Errorf("expected Workspace.CPU 8.0, got %v", ws.CPU)
				}
				if ws.Memory == nil || *ws.Memory != "32g" {
					t.Errorf("expected Workspace.Memory \"32g\", got %v", ws.Memory)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			writeFile(t, path, tt.content)
			cfg, err := LoadFrom(path)
			if err != nil {
				t.Fatalf("LoadFrom failed: %v", err)
			}
			tt.checkFn(t, cfg)
		})
	}
}

func TestConfigCPUMemoryValidation(t *testing.T) {
	t.Parallel()

	invalidTests := []struct {
		name       string
		cfg        *Config
		errSubstrs []string
	}{
		{
			name: "defaults cpu zero",
			cfg: &Config{
				Defaults: Defaults{
					CPU: ptrFloat64(0.0),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "cpu must be a finite number greater than 0"},
		},
		{
			name: "defaults cpu negative",
			cfg: &Config{
				Defaults: Defaults{
					CPU: ptrFloat64(-1.0),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "cpu must be a finite number greater than 0"},
		},
		{
			name: "workspace cpu zero",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"test": {
						Paths: []string{"/data"},
						CPU:   ptrFloat64(0.0),
					},
				},
			},
			errSubstrs: []string{"test", "cpu must be a finite number greater than 0"},
		},
		{
			name: "workspace cpu negative",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"test": {
						Paths: []string{"/data"},
						CPU:   ptrFloat64(-1.0),
					},
				},
			},
			errSubstrs: []string{"test", "cpu must be a finite number greater than 0"},
		},
		{
			name: "defaults cpu NaN",
			cfg: &Config{
				Defaults: Defaults{
					CPU: ptrFloat64(math.NaN()),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "cpu must be a finite number greater than 0"},
		},
		{
			name: "defaults cpu positive infinity",
			cfg: &Config{
				Defaults: Defaults{
					CPU: ptrFloat64(math.Inf(1)),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "cpu must be a finite number greater than 0"},
		},
		{
			name: "defaults cpu negative infinity",
			cfg: &Config{
				Defaults: Defaults{
					CPU: ptrFloat64(math.Inf(-1)),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "cpu must be a finite number greater than 0"},
		},
		{
			name: "workspace cpu NaN",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"test": {
						Paths: []string{"/data"},
						CPU:   ptrFloat64(math.NaN()),
					},
				},
			},
			errSubstrs: []string{"test", "cpu must be a finite number greater than 0"},
		},
		{
			name: "defaults memory zero",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("0g"),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "invalid memory format"},
		},
		{
			name: "defaults memory fractional",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("4.5g"),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "invalid memory format"},
		},
		{
			name: "defaults memory empty",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString(""),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "invalid memory format"},
		},
		{
			name: "defaults memory invalid suffix",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("4gb"),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "invalid memory format"},
		},
		{
			name: "defaults memory negative",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("-1g"),
				},
				Workspaces: map[string]Workspace{},
			},
			errSubstrs: []string{"defaults", "invalid memory format"},
		},
		{
			name: "workspace memory invalid",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"test": {
						Paths:  []string{"/data"},
						Memory: ptrString("0m"),
					},
				},
			},
			errSubstrs: []string{"test", "invalid memory format"},
		},
	}

	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tt.cfg)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			msg := err.Error()
			for _, substr := range tt.errSubstrs {
				if !strings.Contains(msg, substr) {
					t.Errorf("error message %q missing expected substring %q", msg, substr)
				}
			}
		})
	}

	validTests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "defaults memory lowercase m",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("512m"),
				},
				Workspaces: map[string]Workspace{},
			},
		},
		{
			name: "defaults memory uppercase G",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("4G"),
				},
				Workspaces: map[string]Workspace{},
			},
		},
		{
			name: "defaults memory bare integer",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("1024"),
				},
				Workspaces: map[string]Workspace{},
			},
		},
		{
			name: "defaults memory 4g",
			cfg: &Config{
				Defaults: Defaults{
					Memory: ptrString("4g"),
				},
				Workspaces: map[string]Workspace{},
			},
		},
		{
			name: "defaults cpu fractional",
			cfg: &Config{
				Defaults: Defaults{
					CPU: ptrFloat64(0.5),
				},
				Workspaces: map[string]Workspace{},
			},
		},
		{
			name: "defaults cpu high",
			cfg: &Config{
				Defaults: Defaults{
					CPU: ptrFloat64(8.0),
				},
				Workspaces: map[string]Workspace{},
			},
		},
		{
			name: "workspace memory 512m",
			cfg: &Config{
				Workspaces: map[string]Workspace{
					"test": {
						Paths:  []string{"/data"},
						Memory: ptrString("512m"),
					},
				},
			},
		},
	}

	for _, tt := range validTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tt.cfg)
			if err != nil {
				t.Errorf("expected nil error, got: %v", err)
			}
		})
	}
}

func ptrFloat64(v float64) *float64 {
	return &v
}

func ptrString(v string) *string {
	return &v
}

func TestBackendValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		defaults  Defaults
		ws        Workspace
		vm        VMConfig
		wantError string
	}{
		{name: "default backend", ws: Workspace{}},
		{name: "docker backend", ws: Workspace{Backend: BackendDocker}, wantError: "run `jailoc migrate`"},
		{name: "vm backend", ws: Workspace{Backend: BackendVM}},
		{name: "vm from defaults", defaults: Defaults{Backend: BackendVM}},
		{name: "unknown workspace backend", ws: Workspace{Backend: "qemu"}, wantError: `workspace "test": invalid backend "qemu"`},
		{name: "unknown defaults backend", defaults: Defaults{Backend: "qemu"}, wantError: `defaults: invalid backend "qemu"`},
		{name: "vm accepts ssh_auth_sock", ws: Workspace{Backend: BackendVM, SSHAuthSock: new(true)}},
		{name: "vm accepts inherited ssh_auth_sock", defaults: Defaults{SSHAuthSock: true}, ws: Workspace{Backend: BackendVM}},
		{name: "vm accepts ssh_auth_sock override off", defaults: Defaults{SSHAuthSock: true}, ws: Workspace{Backend: BackendVM, SSHAuthSock: new(false)}},
		{name: "docker workspace overrides vm defaults", defaults: Defaults{Backend: BackendVM, SSHAuthSock: true}, ws: Workspace{Backend: BackendDocker}, wantError: "run `jailoc migrate`"},
		{name: "vm accepts filtered_dns", ws: Workspace{Backend: BackendVM, FilteredDNS: new(true), DNSUpstream: "10.20.30.53", DNSBlockedZones: []string{"internal"}}},
		{name: "vm accepts enable_docker", ws: Workspace{Backend: BackendVM, EnableDocker: new(true)}},
		{name: "vm rejects enable_docker with protect_git", defaults: Defaults{EnableDocker: new(true)}, ws: Workspace{Backend: BackendVM, ProtectGit: new(true)}, wantError: "cannot be combined with protect_git"},
		{name: "vm accepts enable_docker override off", defaults: Defaults{EnableDocker: new(true)}, ws: Workspace{Backend: BackendVM, EnableDocker: new(false)}},
		{name: "vm rejects expose_port", ws: Workspace{Backend: BackendVM, ExposePort: new(true)}, wantError: "expose_port is a jailoc v1 setting"},
		{name: "defaults reject expose_port", defaults: Defaults{ExposePort: new(true)}, wantError: "run `jailoc migrate`"},
		{name: "vm accepts isolated_build", ws: Workspace{Backend: BackendVM, IsolatedBuild: new(true)}},
		{name: "relative runner", ws: Workspace{}, vm: VMConfig{Runner: "bin/runner"}, wantError: "vm: path"},
		{name: "absolute runner", ws: Workspace{}, vm: VMConfig{Runner: "/opt/jailoc/go-microvm-runner", LibDir: "/opt/homebrew/lib"}},
		{name: "builder auto", ws: Workspace{}, vm: VMConfig{Builder: BuilderAuto}},
		{name: "builder docker", ws: Workspace{}, vm: VMConfig{Builder: BuilderDocker}},
		{name: "builder vm", ws: Workspace{}, vm: VMConfig{Builder: BuilderVM}},
		{name: "builder unknown", ws: Workspace{}, vm: VMConfig{Builder: "podman"}, wantError: `vm: invalid builder "podman"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := tc.ws
			ws.Paths = []string{"/data/work"}
			cfg := &Config{VM: tc.vm, Defaults: tc.defaults, Workspaces: map[string]Workspace{"test": ws}}
			err := Validate(cfg)
			if tc.wantError == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestLoadBackendTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "[vm]\nrunner = \"/opt/jailoc/go-microvm-runner\"\n\n[defaults]\nbackend = \"vm\"\n\n[workspaces.test]\npaths = [\"/data/work\"]\nbackend = \"vm\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.VM.Runner != "/opt/jailoc/go-microvm-runner" || cfg.Defaults.Backend != BackendVM || cfg.Workspaces["test"].Backend != BackendVM {
		t.Fatalf("unexpected config: vm=%+v defaults.backend=%q ws.backend=%q", cfg.VM, cfg.Defaults.Backend, cfg.Workspaces["test"].Backend)
	}
}

func TestCopiesValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		defaults  Defaults
		ws        Workspace
		want      []string
		wantError string
	}{
		{name: "vm snapshot normalized read-only", ws: Workspace{Backend: BackendVM, Snapshots: []string{"/data/npmrc:/home/agent/.npmrc"}}, want: []string{"/data/npmrc:/home/agent/.npmrc:ro"}},
		{name: "explicit ro accepted", ws: Workspace{Backend: BackendVM, Snapshots: []string{"/data/gh:/home/agent/.config/gh:ro"}}, want: []string{"/data/gh:/home/agent/.config/gh:ro"}},
		{name: "removal entry", ws: Workspace{Backend: BackendVM, Snapshots: []string{":/home/agent/.gitconfig"}}, want: []string{":/home/agent/.gitconfig:ro"}},
		{name: "rw rejected", ws: Workspace{Backend: BackendVM, Snapshots: []string{"/data/npmrc:/home/agent/.npmrc:rw"}}, wantError: "snapshots are read-only"},
		{name: "relative destination rejected", ws: Workspace{Backend: BackendVM, Snapshots: []string{"/data/npmrc:npmrc"}}, wantError: "must be absolute"},
		{name: "system destination rejected", ws: Workspace{Backend: BackendVM, Snapshots: []string{"/data/hosts:/etc/hosts"}}, wantError: "is not allowed"},
		{name: "forbidden source rejected", ws: Workspace{Backend: BackendVM, Snapshots: []string{"/etc/passwd:/home/agent/passwd"}}, wantError: "is forbidden"},
		{name: "default workspace accepts snapshots", ws: Workspace{Snapshots: []string{"/data/npmrc:/home/agent/.npmrc"}}, want: []string{"/data/npmrc:/home/agent/.npmrc:ro"}},
		{name: "default workspace accepts protect_git", ws: Workspace{ProtectGit: new(true)}},
		{name: "vm workspace accepts protect_git", ws: Workspace{Backend: BackendVM, ProtectGit: new(true)}},
		{name: "docker defaults ignore protect_git", defaults: Defaults{ProtectGit: new(true)}},
		{name: "defaults snapshots allowed", defaults: Defaults{Snapshots: []string{"/data/npmrc:/home/agent/.npmrc"}}, ws: Workspace{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := tc.ws
			ws.Paths = []string{"/data/work"}
			cfg := &Config{Defaults: tc.defaults, Workspaces: map[string]Workspace{"test": ws}}
			err := Validate(cfg)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Validate error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := cfg.Workspaces["test"].Snapshots; tc.want != nil && strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("snapshots = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgentValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		defaults  Defaults
		ws        Workspace
		wantError string
	}{
		{name: "unset agent", ws: Workspace{}},
		{name: "workspace agent", ws: Workspace{Agent: "claude"}},
		{name: "defaults agent", defaults: Defaults{Agent: "omo"}},
		{name: "unknown workspace agent", ws: Workspace{Agent: "vim"}, wantError: `workspace "test": invalid agent "vim": must be one of opencode, claude, copilot, omo or a custom agent defined under agents`},
		{name: "unknown defaults agent", defaults: Defaults{Agent: "vim"}, wantError: `defaults: invalid agent "vim"`},
		{name: "settings of another agent", ws: Workspace{Agent: "claude", Agents: map[string]AgentSettings{"copilot": {Args: []string{"--banner"}}}}},
		{name: "custom agent without from", ws: Workspace{Agents: map[string]AgentSettings{"vim": {}}}, wantError: `workspace "test": agents.vim: custom agent needs from`},
		{name: "defaults custom agent without from", defaults: Defaults{Agents: map[string]AgentSettings{"vim": {}}}, wantError: `defaults: agents.vim: custom agent needs from`},
		{name: "custom agent", ws: Workspace{Agent: "claude-opus", Agents: map[string]AgentSettings{"claude-opus": {From: "claude", Version: "2.1.400"}}}},
		{name: "custom agent from defaults", defaults: Defaults{Agents: map[string]AgentSettings{"claude-opus": {From: "claude"}}}, ws: Workspace{Agent: "claude-opus"}},
		{name: "defaults agent is custom", defaults: Defaults{Agent: "claude-opus", Agents: map[string]AgentSettings{"claude-opus": {From: "claude"}}}},
		{name: "workspace settings of defaults custom agent", defaults: Defaults{Agents: map[string]AgentSettings{"claude-opus": {From: "claude"}}}, ws: Workspace{Agents: map[string]AgentSettings{"claude-opus": {Args: []string{"-v"}}}}},
		{name: "custom agent from custom agent", ws: Workspace{Agents: map[string]AgentSettings{"a": {From: "claude"}, "b": {From: "a"}}}, wantError: `agents.b: from "a" is not a built-in agent`},
		{name: "from on built-in agent", ws: Workspace{Agents: map[string]AgentSettings{"claude": {From: "omo"}}}, wantError: `agents.claude: from is only for custom agents`},
		{name: "invalid custom agent name", ws: Workspace{Agents: map[string]AgentSettings{"Claude_X": {From: "claude"}}}, wantError: `invalid agent name "Claude_X"`},
		{name: "version override", defaults: Defaults{Agents: map[string]AgentSettings{"claude": {Version: "2.1.400-beta.1"}}}},
		{name: "dist-tag version", defaults: Defaults{Agents: map[string]AgentSettings{"claude": {Version: "latest"}}}, wantError: `defaults: agents.claude: invalid version "latest"`},
		{name: "version with shell characters", ws: Workspace{Agents: map[string]AgentSettings{"omo": {Version: "1.0.0' && id"}}}, wantError: `invalid version`},
		{name: "agent env reserved", ws: Workspace{Env: []string{"CLAUDE_CONFIG_DIR=/tmp"}}, wantError: `env key "CLAUDE_CONFIG_DIR" is reserved`},
		{name: "TUI config env reserved", defaults: Defaults{Env: []string{"OPENCODE_TUI_CONFIG=/tmp/x.json"}}, wantError: `env key "OPENCODE_TUI_CONFIG" is reserved`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := tc.ws
			ws.Paths = []string{"/data/work"}
			cfg := &Config{Defaults: tc.defaults, Workspaces: map[string]Workspace{"test": ws}}
			err := Validate(cfg)
			if tc.wantError == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestLoadAgentTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `[defaults]
agent = "claude"

[defaults.agents.claude]
args = ["--model", "opus"]

[workspaces.test]
paths = ["/data/work"]
agent = "omo"

[workspaces.test.agents.omo]
install = false
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Defaults.Agent != "claude" || !slices.Equal(cfg.Defaults.Agents["claude"].Args, []string{"--model", "opus"}) {
		t.Fatalf("unexpected defaults: %+v", cfg.Defaults)
	}
	ws := cfg.Workspaces["test"]
	if ws.Agent != "omo" || ws.Agents["omo"].Install == nil || *ws.Agents["omo"].Install {
		t.Fatalf("unexpected workspace: %+v", ws)
	}
}

func TestAgentArgsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `[defaults.agents.claude]
args = ["--model", "opus"]

[workspaces.cleared]
paths = ["/data/cleared"]

[workspaces.cleared.agents.claude]
args = []

[workspaces.inherited]
paths = ["/data/inherited"]

[workspaces.inherited.agents.claude]
install = false
`)
	first, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	data, err := Encode(first)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	path2 := filepath.Join(dir, "config2.toml")
	writeFile(t, path2, string(data))
	second, err := LoadFrom(path2)
	if err != nil {
		t.Fatalf("second LoadFrom: %v", err)
	}
	if args := second.Workspaces["cleared"].Agents["claude"].Args; args == nil || len(args) != 0 {
		t.Errorf("cleared args = %#v, want an explicitly empty list", args)
	}
	if args := second.Workspaces["inherited"].Agents["claude"].Args; args != nil {
		t.Errorf("inherited args = %#v, want nil (inherit defaults)", args)
	}
	if args := second.Defaults.Agents["claude"].Args; !slices.Equal(args, []string{"--model", "opus"}) {
		t.Errorf("defaults args = %#v", args)
	}
}
