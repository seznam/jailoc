package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/seznam/jailoc/internal/agent"
)

// Policy is the admin-owned security policy loaded from a root-owned system
// file. User configuration cannot loosen or bypass it.
type Policy struct {
	Path       string           `toml:"-"`
	VM         PolicyVM         `toml:"vm"`
	Workspace  PolicyWorkspace  `toml:"workspace"`
	Filesystem PolicyFilesystem `toml:"filesystem"`
	Network    PolicyNetwork    `toml:"network"`
	Secrets    PolicySecrets    `toml:"secrets"`
}

// PolicyVM constrains host-side VM runtime and image builder settings.
type PolicyVM struct {
	RequireBuilder       string `toml:"require_builder"`
	RequireIsolatedBuild bool   `toml:"require_isolated_build"`
	ForbidCustomRunner   bool   `toml:"forbid_custom_runner"`
}

// PolicyWorkspace constrains workspace-level features and agent choices.
type PolicyWorkspace struct {
	RequireProtectGit       bool     `toml:"require_protect_git"`
	ForbidDocker            bool     `toml:"forbid_docker"`
	ForbidSSHAuthSock       bool     `toml:"forbid_ssh_auth_sock"`
	ForbidGPGAgent          bool     `toml:"forbid_gpg_agent"`
	ForbidPorts             bool     `toml:"forbid_ports"`
	ForbidHostPorts         bool     `toml:"forbid_host_ports"`
	ForbidGitConfig         bool     `toml:"forbid_git_config"`
	ForbidImportAgentConfig bool     `toml:"forbid_import_agent_config"`
	ForbidCustomDockerfile  bool     `toml:"forbid_custom_dockerfile"`
	AllowedAgents           []string `toml:"allowed_agents"`
}

// PolicyFilesystem constrains host directories and files shared into VMs.
type PolicyFilesystem struct {
	AllowedPaths           []string `toml:"allowed_paths"`
	AllowedMountSources    []string `toml:"allowed_mount_sources"`
	AllowedSnapshotSources []string `toml:"allowed_snapshot_sources"`
	ForbiddenMountSources  []string `toml:"forbidden_mount_sources"`
	ForbidRWMounts         bool     `toml:"forbid_rw_mounts"`
}

// PolicyNetwork constrains guest egress rules, agent endpoints, and DNS.
type PolicyNetwork struct {
	RequireRules          bool     `toml:"require_rules"`
	AllowedTargets        []string `toml:"allowed_targets"`
	AllowedAgentEndpoints []string `toml:"allowed_agent_endpoints"`
	AllowedInternalZones  []string `toml:"allowed_internal_zones"`
	ForbidIPv6            bool     `toml:"forbid_ipv6"`
	ForbidWildcardAllow   bool     `toml:"forbid_wildcard_allow"`
	ForbidPrivateNetworks bool     `toml:"forbid_private_networks"`
	Deny                  []string `toml:"deny"`
	RequireDNS            string   `toml:"require_dns"`
}

// PolicySecrets constrains how host secrets are delivered to VMs.
type PolicySecrets struct {
	ForbidFromCommand  bool     `toml:"forbid_from_command"`
	AllowedSecretFiles []string `toml:"allowed_secret_files"`
}

var (
	policyPathsOverride []string
	skipPolicyTrust     bool
)

// SetPolicyPathsForTest overrides the policy file search paths for tests and
// returns a cleanup function that restores the previous state.
func SetPolicyPathsForTest(paths []string, skipTrust bool) func() {
	prevPaths := policyPathsOverride
	prevSkip := skipPolicyTrust
	policyPathsOverride = slices.Clone(paths)
	skipPolicyTrust = skipTrust
	return func() {
		policyPathsOverride = prevPaths
		skipPolicyTrust = prevSkip
	}
}

func activePolicyPaths() []string {
	if policyPathsOverride != nil {
		return policyPathsOverride
	}
	return defaultPolicyPaths()
}

// LoadPolicy loads the first existing admin policy file from the OS-standard
// locations. Returns (nil, nil) when no policy file exists. Any ownership,
// permission, symlink, parse, or validation error fails closed.
func LoadPolicy() (*Policy, error) {
	for _, path := range activePolicyPaths() {
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat admin policy %q: %w", path, err)
		}
		return loadPolicyFile(path)
	}
	return nil, nil
}

func loadPolicyFile(path string) (*Policy, error) {
	if !skipPolicyTrust {
		if err := verifyPolicyTrust(path); err != nil {
			return nil, fmt.Errorf("admin policy %q failed trust check: %w", path, err)
		}
	}

	var p Policy
	md, err := toml.DecodeFile(path, &p)
	if err != nil {
		return nil, fmt.Errorf("parse admin policy %q: %w", path, err)
	}
	if unk := unknownKeys(md.Undecoded()); len(unk) > 0 {
		return nil, fmt.Errorf("admin policy %q has unknown keys: %s", path, strings.Join(unk, ", "))
	}

	// Preserve distinction between omitted (nil) and explicit empty list ([]).
	normalizeEmptySlice(md, &p.Workspace.AllowedAgents, "workspace", "allowed_agents")
	normalizeEmptySlice(md, &p.Filesystem.AllowedPaths, "filesystem", "allowed_paths")
	normalizeEmptySlice(md, &p.Filesystem.AllowedMountSources, "filesystem", "allowed_mount_sources")
	normalizeEmptySlice(md, &p.Filesystem.AllowedSnapshotSources, "filesystem", "allowed_snapshot_sources")
	normalizeEmptySlice(md, &p.Network.AllowedTargets, "network", "allowed_targets")
	normalizeEmptySlice(md, &p.Network.AllowedAgentEndpoints, "network", "allowed_agent_endpoints")
	normalizeEmptySlice(md, &p.Network.AllowedInternalZones, "network", "allowed_internal_zones")
	normalizeEmptySlice(md, &p.Secrets.AllowedSecretFiles, "secrets", "allowed_secret_files")

	p.Path = path
	if err := validatePolicy(&p); err != nil {
		return nil, fmt.Errorf("validate admin policy %q: %w", path, err)
	}
	return &p, nil
}

func normalizeEmptySlice(md toml.MetaData, dst *[]string, key ...string) {
	if *dst == nil && md.IsDefined(key...) {
		*dst = []string{}
	}
}

func validatePolicy(p *Policy) error {
	switch p.VM.RequireBuilder {
	case "", BuilderVM:
	default:
		return fmt.Errorf("vm.require_builder: invalid value %q (must be %q or empty)", p.VM.RequireBuilder, BuilderVM)
	}

	for _, name := range p.Workspace.AllowedAgents {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("workspace.allowed_agents: empty agent name")
		}
		if _, ok := agent.Lookup(name); !ok {
			return fmt.Errorf("workspace.allowed_agents: unknown built-in agent %q", name)
		}
	}

	pathLists := []struct {
		name string
		list *[]string
	}{
		{"filesystem.allowed_paths", &p.Filesystem.AllowedPaths},
		{"filesystem.allowed_mount_sources", &p.Filesystem.AllowedMountSources},
		{"filesystem.allowed_snapshot_sources", &p.Filesystem.AllowedSnapshotSources},
		{"filesystem.forbidden_mount_sources", &p.Filesystem.ForbiddenMountSources},
		{"secrets.allowed_secret_files", &p.Secrets.AllowedSecretFiles},
	}
	for _, pl := range pathLists {
		if err := normalizePolicyPaths(*pl.list, pl.name); err != nil {
			return err
		}
	}

	for _, target := range p.Network.AllowedTargets {
		if _, _, err := ParseRuleTarget(target); err != nil {
			return fmt.Errorf("network.allowed_targets: %w", err)
		}
	}
	if err := validateAgentEndpoints(p.Network.AllowedAgentEndpoints, "network.allowed_agent_endpoints"); err != nil {
		return err
	}
	for i, z := range p.Network.AllowedInternalZones {
		if err := ValidateInternalZone(z); err != nil {
			return fmt.Errorf("network.allowed_internal_zones: %w", err)
		}
		p.Network.AllowedInternalZones[i] = NormalizeInternalZone(z)
	}
	for _, target := range p.Network.Deny {
		if _, _, err := ParseRuleTarget(target); err != nil {
			return fmt.Errorf("network.deny: %w", err)
		}
	}
	if p.Network.RequireDNS != "" {
		if err := validateDNS(p.Network.RequireDNS, nil); err != nil {
			return fmt.Errorf("network.require_dns: %w", err)
		}
	}

	return nil
}

func normalizePolicyPaths(paths []string, field string) error {
	for i, raw := range paths {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return fmt.Errorf("%s: empty path entry", field)
		}
		expanded, err := ExpandPath(trimmed)
		if err != nil {
			return fmt.Errorf("%s: expand %q: %w", field, raw, err)
		}
		if !filepath.IsAbs(expanded) {
			return fmt.Errorf("%s: path %q must be absolute", field, raw)
		}
		paths[i] = filepath.Clean(expanded)
	}
	return nil
}

// CheckConfigPolicy checks global (non-workspace) settings in cfg against p
// and returns a list of violation descriptions.
func CheckConfigPolicy(cfg *Config, p *Policy) []string {
	if cfg == nil || p == nil {
		return nil
	}
	var violations []string
	if p.VM.RequireBuilder == BuilderVM && cfg.VM.Builder != BuilderVM {
		violations = append(violations, fmt.Sprintf("[vm] builder must be %q (got %q)", BuilderVM, cfg.VM.Builder))
	}
	if p.VM.ForbidCustomRunner {
		if cfg.VM.Runner != "" {
			violations = append(violations, "[vm] runner override is forbidden by policy")
		}
		if cfg.VM.LibDir != "" {
			violations = append(violations, "[vm] lib_dir override is forbidden by policy")
		}
	}
	if p.Workspace.ForbidCustomDockerfile && cfg.Base.Dockerfile != "" {
		violations = append(violations, "[base] dockerfile is forbidden by policy")
	}
	return violations
}

// ValidateConfigPolicy enforces global settings in cfg against p.
func ValidateConfigPolicy(cfg *Config, p *Policy) error {
	violations := CheckConfigPolicy(cfg, p)
	if len(violations) == 0 {
		return nil
	}
	return fmt.Errorf("admin policy (%s) violation: %s", p.Path, strings.Join(violations, "; "))
}

// SummaryLines returns human-readable lines describing active constraints in p.
func (p *Policy) SummaryLines() []string {
	if p == nil {
		return nil
	}
	var lines []string
	if p.VM.RequireBuilder != "" {
		lines = append(lines, fmt.Sprintf("vm.require_builder = %q", p.VM.RequireBuilder))
	}
	if p.VM.RequireIsolatedBuild {
		lines = append(lines, "vm.require_isolated_build = true")
	}
	if p.VM.ForbidCustomRunner {
		lines = append(lines, "vm.forbid_custom_runner = true")
	}
	if p.Workspace.RequireProtectGit {
		lines = append(lines, "workspace.require_protect_git = true")
	}
	if p.Workspace.ForbidDocker {
		lines = append(lines, "workspace.forbid_docker = true")
	}
	if p.Workspace.ForbidSSHAuthSock {
		lines = append(lines, "workspace.forbid_ssh_auth_sock = true")
	}
	if p.Workspace.ForbidGPGAgent {
		lines = append(lines, "workspace.forbid_gpg_agent = true")
	}
	if p.Workspace.ForbidPorts {
		lines = append(lines, "workspace.forbid_ports = true")
	}
	if p.Workspace.ForbidHostPorts {
		lines = append(lines, "workspace.forbid_host_ports = true")
	}
	if p.Workspace.ForbidGitConfig {
		lines = append(lines, "workspace.forbid_git_config = true")
	}
	if p.Workspace.ForbidImportAgentConfig {
		lines = append(lines, "workspace.forbid_import_agent_config = true")
	}
	if p.Workspace.ForbidCustomDockerfile {
		lines = append(lines, "workspace.forbid_custom_dockerfile = true")
	}
	if p.Workspace.AllowedAgents != nil {
		lines = append(lines, fmt.Sprintf("workspace.allowed_agents = [%s]", strings.Join(p.Workspace.AllowedAgents, ", ")))
	}
	if p.Filesystem.AllowedPaths != nil {
		lines = append(lines, fmt.Sprintf("filesystem.allowed_paths = [%s]", strings.Join(p.Filesystem.AllowedPaths, ", ")))
	}
	if p.Filesystem.AllowedMountSources != nil {
		lines = append(lines, fmt.Sprintf("filesystem.allowed_mount_sources = [%s]", strings.Join(p.Filesystem.AllowedMountSources, ", ")))
	}
	if p.Filesystem.AllowedSnapshotSources != nil {
		lines = append(lines, fmt.Sprintf("filesystem.allowed_snapshot_sources = [%s]", strings.Join(p.Filesystem.AllowedSnapshotSources, ", ")))
	}
	if len(p.Filesystem.ForbiddenMountSources) > 0 {
		lines = append(lines, fmt.Sprintf("filesystem.forbidden_mount_sources = [%s]", strings.Join(p.Filesystem.ForbiddenMountSources, ", ")))
	}
	if p.Filesystem.ForbidRWMounts {
		lines = append(lines, "filesystem.forbid_rw_mounts = true")
	}
	if p.Network.RequireRules {
		lines = append(lines, "network.require_rules = true")
	}
	if p.Network.AllowedTargets != nil {
		lines = append(lines, fmt.Sprintf("network.allowed_targets = [%s]", strings.Join(p.Network.AllowedTargets, ", ")))
	}
	if p.Network.AllowedAgentEndpoints != nil {
		lines = append(lines, fmt.Sprintf("network.allowed_agent_endpoints = [%s]", strings.Join(p.Network.AllowedAgentEndpoints, ", ")))
	}
	if p.Network.AllowedInternalZones != nil {
		lines = append(lines, fmt.Sprintf("network.allowed_internal_zones = [%s]", strings.Join(p.Network.AllowedInternalZones, ", ")))
	}
	if p.Network.ForbidIPv6 {
		lines = append(lines, "network.forbid_ipv6 = true")
	}
	if p.Network.ForbidWildcardAllow {
		lines = append(lines, "network.forbid_wildcard_allow = true")
	}
	if p.Network.ForbidPrivateNetworks {
		lines = append(lines, "network.forbid_private_networks = true")
	}
	if len(p.Network.Deny) > 0 {
		lines = append(lines, fmt.Sprintf("network.deny = [%s]", strings.Join(p.Network.Deny, ", ")))
	}
	if p.Network.RequireDNS != "" {
		lines = append(lines, fmt.Sprintf("network.require_dns = %q", p.Network.RequireDNS))
	}
	if p.Secrets.ForbidFromCommand {
		lines = append(lines, "secrets.forbid_from_command = true")
	}
	if p.Secrets.AllowedSecretFiles != nil {
		lines = append(lines, fmt.Sprintf("secrets.allowed_secret_files = [%s]", strings.Join(p.Secrets.AllowedSecretFiles, ", ")))
	}
	return lines
}
