package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestLoadFullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[image]
repository = "ghcr.io/seznam/jailoc-custom"

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

	if cfg.Image.Repository != "ghcr.io/seznam/jailoc-custom" {
		t.Fatalf("unexpected image repository: %q", cfg.Image.Repository)
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

	if cfg.Image.Repository != defaultImageRepository {
		t.Fatalf("expected default image repository %q, got %q", defaultImageRepository, cfg.Image.Repository)
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

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[defaults]
env = ["GLOBAL_VAR=value1"]
env_file = ["/etc/env.global"]
allowed_hosts = ["global.example.com"]
allowed_networks = ["10.0.0.0/8"]

[image]
repository = "ghcr.io/seznam/jailoc"

[workspaces.default]
paths = ["/data/workspace", "/work2"]
allowed_hosts = ["foo.com"]
allowed_networks = ["10.0.0.0/8"]
env = ["LOCAL_VAR=value2"]
env_file = ["/etc/env.local"]
build_context = "/tmp/context"
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
}

func TestParseDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[defaults]
env = ["GLOBAL_VAR=value1", "GLOBAL_VAR2=value2"]
env_file = ["/etc/env.global", "/etc/env.backup"]
allowed_hosts = ["api.example.com", "db.example.com"]
allowed_networks = ["10.0.0.0/8", "172.16.0.0/12"]

[workspaces.default]
paths = ["/data/workspace"]
`)

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if !reflect.DeepEqual(cfg.Defaults.Env, []string{"GLOBAL_VAR=value1", "GLOBAL_VAR2=value2"}) {
		t.Fatalf("unexpected defaults env: %#v", cfg.Defaults.Env)
	}
	if !reflect.DeepEqual(cfg.Defaults.EnvFile, []string{"/etc/env.global", "/etc/env.backup"}) {
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
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, `
[workspaces.default]
paths = ["/data/workspace"]
env = ["LOCAL_VAR=value1", "LOCAL_VAR2=value2"]
env_file = ["/home/user/.env", "/home/user/.env.local"]

[workspaces.other]
paths = ["/work"]
env = ["OTHER_VAR=val"]
`)

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	defaultWs := cfg.Workspaces["default"]
	if !reflect.DeepEqual(defaultWs.Env, []string{"LOCAL_VAR=value1", "LOCAL_VAR2=value2"}) {
		t.Fatalf("unexpected workspace default env: %#v", defaultWs.Env)
	}
	if !reflect.DeepEqual(defaultWs.EnvFile, []string{"/home/user/.env", "/home/user/.env.local"}) {
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

func TestAllowedHostsContent(t *testing.T) {
	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {AllowedHosts: []string{"foo.com", "bar.com"}},
	}}

	got := AllowedHostsFileContent("default", cfg)
	if got != "foo.com\nbar.com\n" {
		t.Fatalf("unexpected allowed hosts content: %q", got)
	}
}

func TestAllowedNetworksContent(t *testing.T) {
	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {AllowedNetworks: []string{"10.0.0.0/8"}},
	}}

	got := AllowedNetworksFileContent("default", cfg)
	if got != "10.0.0.0/8\n" {
		t.Fatalf("unexpected allowed networks content: %q", got)
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

func TestValidateAcceptsValidModes(t *testing.T) {
	for _, mode := range []string{"", ModeRemote, ModeExec} {
		cfg := &Config{Mode: mode, Workspaces: map[string]Workspace{"default": {Paths: []string{"/data/workspace"}}}}
		if err := Validate(cfg); err != nil {
			t.Errorf("mode %q: unexpected error: %v", mode, err)
		}
	}
}

func TestValidateRejectsInvalidMode(t *testing.T) {
	cfg := &Config{Mode: "banana", Workspaces: map[string]Workspace{"default": {Paths: []string{"/data/workspace"}}}}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("error %q does not contain 'invalid mode'", err.Error())
	}
}

func TestResolveModeWithOpenCodeOnPath(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) { return "/usr/local/bin/opencode", nil }
	if got := ResolveMode(""); got != ModeRemote {
		t.Errorf("got %q, want %q", got, ModeRemote)
	}
}

func TestResolveModeWithoutOpenCode(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) { return "", fmt.Errorf("not found") }
	if got := ResolveMode(""); got != ModeExec {
		t.Errorf("got %q, want %q", got, ModeExec)
	}
}

func TestResolveModeExplicitOverridesDetection(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(file string) (string, error) { return "", fmt.Errorf("not found") }
	if got := ResolveMode(ModeRemote); got != ModeRemote {
		t.Errorf("explicit remote: got %q, want %q", got, ModeRemote)
	}
	if got := ResolveMode(ModeExec); got != ModeExec {
		t.Errorf("explicit exec: got %q, want %q", got, ModeExec)
	}
}

func TestConfigRoundTripWithMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeFile(t, ConfigPath(), `
mode = "exec"

[workspaces.default]
paths = ["/data/workspace"]
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Mode != ModeExec {
		t.Fatalf("expected mode %q, got %q", ModeExec, cfg.Mode)
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

func TestAllowedHostsFileContentNilConfig(t *testing.T) {
	got := AllowedHostsFileContent("default", nil)
	if got != "" {
		t.Fatalf("expected empty string for nil config, got %q", got)
	}
}

func TestAllowedHostsFileContentMissingWorkspace(t *testing.T) {
	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {AllowedHosts: []string{"foo.com"}},
	}}

	got := AllowedHostsFileContent("nonexistent", cfg)
	if got != "" {
		t.Fatalf("expected empty string for missing workspace, got %q", got)
	}
}

func TestAllowedNetworksFileContentNilConfig(t *testing.T) {
	got := AllowedNetworksFileContent("default", nil)
	if got != "" {
		t.Fatalf("expected empty string for nil config, got %q", got)
	}
}

func TestAllowedNetworksFileContentMissingWorkspace(t *testing.T) {
	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {AllowedNetworks: []string{"10.0.0.0/8"}},
	}}

	got := AllowedNetworksFileContent("nonexistent", cfg)
	if got != "" {
		t.Fatalf("expected empty string for missing workspace, got %q", got)
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

func TestAllowedHostsFileContentEmptyList(t *testing.T) {
	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {AllowedHosts: []string{}},
	}}

	got := AllowedHostsFileContent("default", cfg)
	if got != "" {
		t.Fatalf("expected empty string for empty hosts list, got %q", got)
	}
}

func TestAllowedNetworksFileContentEmptyList(t *testing.T) {
	cfg := &Config{Workspaces: map[string]Workspace{
		"default": {AllowedNetworks: []string{}},
	}}

	got := AllowedNetworksFileContent("default", cfg)
	if got != "" {
		t.Fatalf("expected empty string for empty networks list, got %q", got)
	}
}

func TestWriteAllowedFilesWritesBothFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{Workspaces: map[string]Workspace{
		"myws": {
			AllowedHosts:    []string{"foo.com", "bar.com"},
			AllowedNetworks: []string{"10.0.0.0/8", "172.16.0.0/12"},
		},
	}}

	if err := WriteAllowedFiles("myws", cfg); err != nil {
		t.Fatalf("WriteAllowedFiles returned error: %v", err)
	}

	dir := ConfigDir()

	hostsData, err := os.ReadFile(filepath.Join(dir, "allowed-hosts")) //nolint:gosec // test reads from t.TempDir()
	if err != nil {
		t.Fatalf("read allowed-hosts: %v", err)
	}
	if string(hostsData) != "foo.com\nbar.com\n" {
		t.Fatalf("unexpected allowed-hosts content: %q", string(hostsData))
	}

	networksData, err := os.ReadFile(filepath.Join(dir, "allowed-networks")) //nolint:gosec // test reads from t.TempDir()
	if err != nil {
		t.Fatalf("read allowed-networks: %v", err)
	}
	if string(networksData) != "10.0.0.0/8\n172.16.0.0/12\n" {
		t.Fatalf("unexpected allowed-networks content: %q", string(networksData))
	}
}

func TestWriteAllowedFilesRemovesStaleFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "allowed-hosts"), []byte("old.com\n"), 0o600); err != nil {
		t.Fatalf("write stale allowed-hosts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "allowed-networks"), []byte("192.168.0.0/16\n"), 0o600); err != nil {
		t.Fatalf("write stale allowed-networks: %v", err)
	}

	cfg := &Config{Workspaces: map[string]Workspace{
		"myws": {AllowedHosts: []string{}, AllowedNetworks: []string{}},
	}}

	if err := WriteAllowedFiles("myws", cfg); err != nil {
		t.Fatalf("WriteAllowedFiles returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "allowed-hosts")); !os.IsNotExist(err) {
		t.Fatal("expected allowed-hosts to be removed for empty list")
	}
	if _, err := os.Stat(filepath.Join(dir, "allowed-networks")); !os.IsNotExist(err) {
		t.Fatal("expected allowed-networks to be removed for empty list")
	}
}

func TestWriteAllowedFilesNilConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := WriteAllowedFiles("whatever", nil); err != nil {
		t.Fatalf("WriteAllowedFiles with nil config returned error: %v", err)
	}

	dir := ConfigDir()
	if _, err := os.Stat(filepath.Join(dir, "allowed-hosts")); !os.IsNotExist(err) {
		t.Fatal("expected no allowed-hosts file for nil config")
	}
	if _, err := os.Stat(filepath.Join(dir, "allowed-networks")); !os.IsNotExist(err) {
		t.Fatal("expected no allowed-networks file for nil config")
	}
}

func TestWriteAllowedFilesMissingWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{Workspaces: map[string]Workspace{
		"other": {AllowedHosts: []string{"foo.com"}},
	}}

	if err := WriteAllowedFiles("nonexistent", cfg); err != nil {
		t.Fatalf("WriteAllowedFiles with missing workspace returned error: %v", err)
	}

	dir := ConfigDir()
	if _, err := os.Stat(filepath.Join(dir, "allowed-hosts")); !os.IsNotExist(err) {
		t.Fatal("expected no allowed-hosts file for missing workspace")
	}
}

func TestValidateAcceptsDockerfileHTTP(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Image: ImageConfig{
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
		Image: ImageConfig{
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
		Image: ImageConfig{
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
					Image: ImageConfig{
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
		Image: ImageConfig{
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
		Image: ImageConfig{
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
		Image: ImageConfig{
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
		Image: ImageConfig{
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
		Image: ImageConfig{
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
		Image: ImageConfig{
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
	if cfg.Image.Dockerfile != expected {
		t.Fatalf("expected %q, got: %q", expected, cfg.Image.Dockerfile)
	}
}
