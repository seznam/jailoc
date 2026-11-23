package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func TestResolveEnforcesPolicyWorkspaceControls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     func() *config.Config
		policy  *config.Policy
		wantErr string
	}{
		{
			name: "require_isolated_build rejected when false",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", VM: config.PolicyVM{RequireIsolatedBuild: true}},
			wantErr: "isolated_build must be true",
		},
		{
			name: "require_protect_git rejected when false",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{RequireProtectGit: true}},
			wantErr: "protect_git must be true",
		},
		{
			name: "forbid_docker rejected when true",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {EnableDocker: boolPtr(true)}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidDocker: true}},
			wantErr: "enable_docker is forbidden",
		},
		{
			name: "forbid_ssh_auth_sock rejected",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {SSHAuthSock: boolPtr(true)}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidSSHAuthSock: true}},
			wantErr: "ssh_auth_sock is forbidden",
		},
		{
			name: "forbid_gpg_agent rejected",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {GPGAgent: boolPtr(true)}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidGPGAgent: true}},
			wantErr: "gpg_agent is forbidden",
		},
		{
			name: "forbid_ports rejected",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {Ports: []config.Port{"8080"}}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidPorts: true}},
			wantErr: "ports are forbidden",
		},
		{
			name: "forbid_host_ports rejected",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {HostPorts: []config.HostPort{"5432"}}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidHostPorts: true}},
			wantErr: "host_ports are forbidden",
		},
		{
			name: "forbid_git_config rejected when default true",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidGitConfig: true}},
			wantErr: "git_config is forbidden",
		},
		{
			name: "forbid_import_agent_config rejected when default true",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidImportAgentConfig: true}},
			wantErr: "import_agent_config is forbidden",
		},
		{
			name: "forbid_custom_dockerfile rejects ws dockerfile",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {Dockerfile: "/tmp/Dockerfile"}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidCustomDockerfile: true}},
			wantErr: "workspace dockerfile is forbidden",
		},
		{
			name: "forbid_custom_dockerfile rejects ws image",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {Image: "ubuntu:24.04"}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{ForbidCustomDockerfile: true}},
			wantErr: "workspace image override is forbidden",
		},
		{
			name: "allowed_agents rejects unlisted agent",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {Agent: "claude"}}}
			},
			policy:  &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{AllowedAgents: []string{"opencode"}}},
			wantErr: "not in allowed_agents",
		},
		{
			name: "allowed_agents accepts alias opencode for opencode2",
			cfg: func() *config.Config {
				return &config.Config{Workspaces: map[string]config.Workspace{"w": {Agent: "opencode2"}}}
			},
			policy: &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{AllowedAgents: []string{"opencode"}}},
		},
		{
			name: "allowed_agents accepts custom agent derived from allowed built-in",
			cfg: func() *config.Config {
				return &config.Config{
					Defaults: config.Defaults{
						Agents: map[string]config.AgentSettings{
							"claude-opus": {From: "claude"},
						},
					},
					Workspaces: map[string]config.Workspace{"w": {Agent: "claude-opus"}},
				}
			},
			policy: &config.Policy{Path: "/etc/jailoc/policy.toml", Workspace: config.PolicyWorkspace{AllowedAgents: []string{"claude"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.cfg()
			cfg.Policy = tt.policy
			_, err := Resolve(cfg, "w")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Resolve() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Resolve() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Resolve() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestResolveEnforcesPolicyFilesystemAndSymlinkEscape(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	allowedDir := filepath.Join(root, "work")
	secretDir := filepath.Join(root, "secret")
	if err := os.MkdirAll(allowedDir, 0o750); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	if err := os.MkdirAll(secretDir, 0o750); err != nil {
		t.Fatalf("mkdir secret: %v", err)
	}
	escapeLink := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(secretDir, escapeLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	t.Run("symlink escape under allowed_paths is rejected", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path:       "/etc/jailoc/policy.toml",
				Filesystem: config.PolicyFilesystem{AllowedPaths: []string{allowedDir}},
			},
			Workspaces: map[string]config.Workspace{
				"w": {Paths: []string{filepath.Join(escapeLink, "repo")}},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "outside filesystem.allowed_paths") {
			t.Fatalf("Resolve() = %v, want allowed_paths violation", err)
		}
	})

	t.Run("forbid_rw_mounts rejects workspace paths and rw mounts", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path:       "/etc/jailoc/policy.toml",
				Filesystem: config.PolicyFilesystem{ForbidRWMounts: true},
			},
			Workspaces: map[string]config.Workspace{
				"w": {Paths: []string{allowedDir}},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "forbid_rw_mounts") {
			t.Fatalf("Resolve() = %v, want forbid_rw_mounts violation", err)
		}
	})

	t.Run("allowed_mount_sources checks built-in agent mounts too", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path:       "/etc/jailoc/policy.toml",
				Filesystem: config.PolicyFilesystem{AllowedMountSources: []string{allowedDir}},
			},
			Workspaces: map[string]config.Workspace{
				"w": {},
			},
		}
		// Default opencode harness adds default mounts under ~/.agents which is outside allowedDir.
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "outside filesystem.allowed_mount_sources") {
			t.Fatalf("Resolve() = %v, want allowed_mount_sources violation", err)
		}
	})

	t.Run("forbidden_mount_sources blocks paths mounts and snapshots", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path:       "/etc/jailoc/policy.toml",
				Filesystem: config.PolicyFilesystem{ForbiddenMountSources: []string{secretDir}},
			},
			Workspaces: map[string]config.Workspace{
				"w": {Snapshots: []string{filepath.Join(secretDir, "file.txt") + ":/tmp/file.txt:ro"}},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "forbidden_mount_sources") {
			t.Fatalf("Resolve() = %v, want forbidden_mount_sources violation", err)
		}
	})
}

func TestResolveEnforcesPolicyNetworkAndSecrets(t *testing.T) {
	t.Parallel()

	t.Run("allowed_targets empty denies all user allow rules", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path: "/etc/jailoc/policy.toml",
				Network: config.PolicyNetwork{
					AllowedTargets: []string{},
				},
			},
			Workspaces: map[string]config.Workspace{
				"w": {
					Network: &config.Network{
						Rules: []config.NetworkRule{{Name: "gh", Allow: []string{"github.com"}}},
					},
				},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "not in network.allowed_targets") {
			t.Fatalf("Resolve() = %v, want allowed_targets violation", err)
		}
	})

	t.Run("allowed_targets wildcard covers subdomain", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path: "/etc/jailoc/policy.toml",
				Network: config.PolicyNetwork{
					AllowedTargets: []string{"*.github.com", "github.com"},
				},
			},
			Workspaces: map[string]config.Workspace{
				"w": {
					Network: &config.Network{
						Rules: []config.NetworkRule{{Name: "gh", Allow: []string{"api.github.com"}}},
					},
				},
			},
		}
		if _, err := Resolve(cfg, "w"); err != nil {
			t.Fatalf("Resolve() unexpected error: %v", err)
		}
	})

	t.Run("deny rejects overlapping user allow rule", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path: "/etc/jailoc/policy.toml",
				Network: config.PolicyNetwork{
					Deny: []string{"*.ngrok.io"},
				},
			},
			Workspaces: map[string]config.Workspace{
				"w": {
					Network: &config.Network{
						Rules: []config.NetworkRule{{Name: "tunnel", Allow: []string{"abc.ngrok.io"}}},
					},
				},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "conflicts with policy network.deny") {
			t.Fatalf("Resolve() = %v, want network.deny violation", err)
		}
	})

	t.Run("allowed_agent_endpoints caps agent endpoints", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path: "/etc/jailoc/policy.toml",
				Network: config.PolicyNetwork{
					AllowedAgentEndpoints: []string{"api.anthropic.com"},
				},
			},
			Workspaces: map[string]config.Workspace{
				"w": {
					Network: &config.Network{
						AgentEndpoints: []string{"api.anthropic.com", "api.openai.com"},
					},
				},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "not in network.allowed_agent_endpoints") {
			t.Fatalf("Resolve() = %v, want allowed_agent_endpoints violation", err)
		}
	})

	t.Run("forbid_wildcard_allow and forbid_private_networks", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path: "/etc/jailoc/policy.toml",
				Network: config.PolicyNetwork{
					ForbidWildcardAllow:   true,
					ForbidPrivateNetworks: true,
				},
			},
			Workspaces: map[string]config.Workspace{
				"w": {
					Network: &config.Network{
						Rules: []config.NetworkRule{{Name: "bad", Allow: []string{"*", "10.0.0.0/8"}}},
					},
				},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "forbid_wildcard_allow") || !strings.Contains(err.Error(), "forbid_private_networks") {
			t.Fatalf("Resolve() = %v, want wildcard and private network violations", err)
		}
	})

	t.Run("secrets forbid_from_command and allowed_secret_files", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Policy: &config.Policy{
				Path: "/etc/jailoc/policy.toml",
				Secrets: config.PolicySecrets{
					ForbidFromCommand:  true,
					AllowedSecretFiles: []string{"/run/corp-secrets"},
				},
			},
			Workspaces: map[string]config.Workspace{
				"w": {
					Secrets: config.Secrets{
						Env: map[string]config.Secret{
							"TOK": {FromCommand: "echo hi"},
						},
						File: map[string]config.Secret{
							"KEY": {FromFile: "/tmp/unapproved"},
						},
					},
				},
			},
		}
		_, err := Resolve(cfg, "w")
		if err == nil || !strings.Contains(err.Error(), "from_command") || !strings.Contains(err.Error(), "allowed_secret_files") {
			t.Fatalf("Resolve() = %v, want secrets policy violations", err)
		}
	})
}
