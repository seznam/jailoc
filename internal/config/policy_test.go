package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPolicyMissingReturnsNil(t *testing.T) {
	dir := t.TempDir()
	restore := SetPolicyPathsForTest([]string{filepath.Join(dir, "missing.toml")}, true)
	t.Cleanup(restore)

	p, err := LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy() unexpected error: %v", err)
	}
	if p != nil {
		t.Fatalf("LoadPolicy() = %+v, want nil", p)
	}
}

func TestLoadPolicyFallbackOrder(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.toml")
	second := filepath.Join(dir, "second.toml")
	if err := os.WriteFile(second, []byte("[workspace]\nforbid_docker = true\n"), 0o600); err != nil {
		t.Fatalf("write second policy: %v", err)
	}

	restore := SetPolicyPathsForTest([]string{first, second}, true)
	t.Cleanup(restore)

	p, err := LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy() unexpected error: %v", err)
	}
	if p == nil || p.Path != second || !p.Workspace.ForbidDocker {
		t.Fatalf("LoadPolicy() = %+v, want second policy with forbid_docker=true", p)
	}

	// Once the first path exists, it takes precedence.
	if err := os.WriteFile(first, []byte("[workspace]\nrequire_protect_git = true\n"), 0o600); err != nil {
		t.Fatalf("write first policy: %v", err)
	}
	p, err = LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy() unexpected error: %v", err)
	}
	if p == nil || p.Path != first || !p.Workspace.RequireProtectGit || p.Workspace.ForbidDocker {
		t.Fatalf("LoadPolicy() = %+v, want first policy only", p)
	}
}

func TestLoadPolicyAllFieldsAndEmptySlices(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	policyPath := filepath.Join(dir, "policy.toml")
	content := `
[vm]
require_builder = "vm"
require_isolated_build = true
forbid_custom_runner = true

[workspace]
require_protect_git = true
forbid_docker = true
forbid_ssh_auth_sock = true
forbid_gpg_agent = true
forbid_ports = true
forbid_host_ports = true
forbid_git_config = true
forbid_import_agent_config = true
forbid_custom_dockerfile = true
allowed_agents = ["claude", "opencode"]

[filesystem]
allowed_paths = ["~/work"]
allowed_mount_sources = ["~/work"]
allowed_snapshot_sources = []
forbidden_mount_sources = ["~/private"]
forbid_rw_mounts = true

[network]
require_rules = true
allowed_targets = []
allowed_agent_endpoints = ["api.anthropic.com"]
allowed_internal_zones = ["*.Corp.Internal."]
forbid_ipv6 = true
forbid_wildcard_allow = true
forbid_private_networks = true
deny = ["pastebin.com", "*.ngrok.io"]
require_dns = "10.0.0.53"

[secrets]
forbid_from_command = true
allowed_secret_files = ["/run/corp-secrets"]
`
	if err := os.WriteFile(policyPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	restore := SetPolicyPathsForTest([]string{policyPath}, true)
	t.Cleanup(restore)

	p, err := LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy() unexpected error: %v", err)
	}
	if p.VM.RequireBuilder != BuilderVM || !p.VM.RequireIsolatedBuild || !p.VM.ForbidCustomRunner {
		t.Fatalf("unexpected VM policy: %+v", p.VM)
	}
	if p.Filesystem.AllowedPaths[0] != filepath.Join(home, "work") {
		t.Fatalf("allowed_paths[0] = %q, want expanded home path", p.Filesystem.AllowedPaths[0])
	}
	if p.Filesystem.AllowedSnapshotSources == nil || len(p.Filesystem.AllowedSnapshotSources) != 0 {
		t.Fatalf("allowed_snapshot_sources = %#v, want non-nil empty slice", p.Filesystem.AllowedSnapshotSources)
	}
	if p.Network.AllowedTargets == nil || len(p.Network.AllowedTargets) != 0 {
		t.Fatalf("allowed_targets = %#v, want non-nil empty slice", p.Network.AllowedTargets)
	}
	if p.Network.AllowedInternalZones[0] != "corp.internal" {
		t.Fatalf("allowed_internal_zones[0] = %q, want normalized corp.internal", p.Network.AllowedInternalZones[0])
	}
	if len(p.SummaryLines()) == 0 {
		t.Fatalf("SummaryLines() returned empty")
	}
}

func TestLoadPolicyRejectsInvalidAndUnknownKeys(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "unknown key fails closed",
			content: "[workspace]\nforbid_dockr = true\n",
			wantErr: "unknown keys",
		},
		{
			name:    "invalid require_builder",
			content: "[vm]\nrequire_builder = \"docker\"\n",
			wantErr: "vm.require_builder",
		},
		{
			name:    "unknown agent in allowed_agents",
			content: "[workspace]\nallowed_agents = [\"nonexistent\"]\n",
			wantErr: "unknown built-in agent",
		},
		{
			name:    "relative filesystem path",
			content: "[filesystem]\nallowed_paths = [\"relative/dir\"]\n",
			wantErr: "must be absolute",
		},
		{
			name:    "invalid network deny target",
			content: "[network]\ndeny = [\"bad*host.com\"]\n",
			wantErr: "network.deny",
		},
		{
			name:    "invalid network require_dns",
			content: "[network]\nrequire_dns = \"127.0.0.1\"\n",
			wantErr: "network.require_dns",
		},
		{
			name:    "invalid internal zone",
			content: "[network]\nallowed_internal_zones = [\"com\"]\n",
			wantErr: "network.allowed_internal_zones",
		},
		{
			name:    "malformed toml",
			content: "[vm\nrequire_builder = \"vm\"\n",
			wantErr: "parse admin policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "policy.toml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write policy: %v", err)
			}
			restore := SetPolicyPathsForTest([]string{path}, true)
			t.Cleanup(restore)

			_, err := LoadPolicy()
			if err == nil {
				t.Fatalf("LoadPolicy() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadPolicy() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestVerifyPolicyTrust(t *testing.T) {
	uid := uint32(os.Geteuid()) //nolint:gosec // G115: POSIX uid fits in uint32

	t.Run("valid regular file and dirs", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o750); err != nil { //nolint:gosec // G302: directory requires execute bit
			t.Fatalf("chmod root: %v", err)
		}
		sub := filepath.Join(root, "jailoc")
		if err := os.Mkdir(sub, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		f := filepath.Join(sub, "policy.toml")
		if err := os.WriteFile(f, []byte(""), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := verifyPolicyTrustWithUID(f, root, uid); err != nil {
			t.Fatalf("verifyPolicyTrustWithUID() unexpected error: %v", err)
		}
	})

	t.Run("wrong uid rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o750); err != nil { //nolint:gosec // G302: directory requires execute bit
			t.Fatalf("chmod root: %v", err)
		}
		f := filepath.Join(root, "policy.toml")
		if err := os.WriteFile(f, []byte(""), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		err := verifyPolicyTrustWithUID(f, root, uid+1)
		if err == nil || !strings.Contains(err.Error(), "owned by uid") {
			t.Fatalf("verifyPolicyTrustWithUID() = %v, want uid error", err)
		}
	})

	t.Run("group writable file rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o750); err != nil { //nolint:gosec // G302: directory requires execute bit
			t.Fatalf("chmod root: %v", err)
		}
		f := filepath.Join(root, "policy.toml")
		if err := os.WriteFile(f, []byte(""), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(f, 0o664); err != nil { //nolint:gosec // G302: intentionally testing unsafe permissions
			t.Fatalf("chmod: %v", err)
		}
		err := verifyPolicyTrustWithUID(f, root, uid)
		if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
			t.Fatalf("verifyPolicyTrustWithUID() = %v, want unsafe permissions error", err)
		}
	})

	t.Run("world writable parent dir rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o750); err != nil { //nolint:gosec // G302: directory requires execute bit
			t.Fatalf("chmod root: %v", err)
		}
		sub := filepath.Join(root, "jailoc")
		if err := os.Mkdir(sub, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(sub, 0o777); err != nil { //nolint:gosec // G302: intentionally testing world-writable dir
			t.Fatalf("chmod sub: %v", err)
		}
		f := filepath.Join(sub, "policy.toml")
		if err := os.WriteFile(f, []byte(""), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		err := verifyPolicyTrustWithUID(f, root, uid)
		if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
			t.Fatalf("verifyPolicyTrustWithUID() = %v, want unsafe permissions error", err)
		}
	})

	t.Run("symlink to unsafe file rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o750); err != nil { //nolint:gosec // G302: directory requires execute bit
			t.Fatalf("chmod root: %v", err)
		}
		target := filepath.Join(root, "target.toml")
		if err := os.WriteFile(target, []byte(""), 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		if err := os.Chmod(target, 0o666); err != nil { //nolint:gosec // G302: intentionally testing world-writable symlink target
			t.Fatalf("chmod target: %v", err)
		}
		link := filepath.Join(root, "policy.toml")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		err := verifyPolicyTrustWithUID(link, root, uid)
		if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
			t.Fatalf("verifyPolicyTrustWithUID() = %v, want unsafe permissions error", err)
		}
	})

	t.Run("directory instead of regular file rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o750); err != nil { //nolint:gosec // G302: directory requires execute bit
			t.Fatalf("chmod root: %v", err)
		}
		d := filepath.Join(root, "policy.toml")
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		err := verifyPolicyTrustWithUID(d, root, uid)
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("verifyPolicyTrustWithUID() = %v, want regular file error", err)
		}
	})
}

func TestLoadEnforcesConfigPolicyEvenWithCustomConfigPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(cfgPath, []byte("[vm]\nbuilder = \"docker\"\n[workspaces.default]\npaths = []\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	policyPath := filepath.Join(dir, "policy.toml")
	if err := os.WriteFile(policyPath, []byte("[vm]\nrequire_builder = \"vm\"\n"), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	t.Setenv(PathEnv, cfgPath)
	restore := SetPolicyPathsForTest([]string{policyPath}, true)
	t.Cleanup(restore)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "[vm] builder") {
		t.Fatalf("Load() = %v, want [vm] builder policy violation", err)
	}

	// LoadUnchecked still attaches the policy without failing.
	cfg, err := LoadUnchecked()
	if err != nil {
		t.Fatalf("LoadUnchecked() unexpected error: %v", err)
	}
	if cfg.Policy == nil || cfg.Policy.Path != policyPath {
		t.Fatalf("cfg.Policy = %+v, want loaded policy", cfg.Policy)
	}
}
