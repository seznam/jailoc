package workspace

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seznam/jailoc/internal/config"
)

var privatePolicyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// EnforcePolicy validates a resolved workspace (and global config) against the
// active admin policy, returning a descriptive error on any violation.
func EnforcePolicy(cfg *config.Config, ws *Resolved, p *config.Policy) error {
	if p == nil {
		return nil
	}
	violations := CheckPolicy(cfg, ws, p)
	if len(violations) == 0 {
		return nil
	}
	return fmt.Errorf("workspace %q violates admin policy (%s): %s", ws.Name, p.Path, strings.Join(violations, "; "))
}

// CheckPolicy returns all admin policy violations for ws (including global
// config violations when cfg is non-nil).
func CheckPolicy(cfg *config.Config, ws *Resolved, p *config.Policy) []string {
	if p == nil || ws == nil {
		return nil
	}
	var violations []string
	if cfg != nil {
		violations = append(violations, config.CheckConfigPolicy(cfg, p)...)
	}
	violations = append(violations, checkWorkspacePolicy(ws, p)...)
	violations = append(violations, checkFilesystemPolicy(ws, p)...)
	violations = append(violations, checkNetworkPolicy(ws, p)...)
	violations = append(violations, checkSecretsPolicy(ws, p)...)
	return violations
}

func checkWorkspacePolicy(ws *Resolved, p *config.Policy) []string {
	var violations []string
	if p.VM.RequireIsolatedBuild && !ws.IsolatedBuild {
		violations = append(violations, "isolated_build must be true")
	}
	if p.Workspace.RequireProtectGit && !ws.ProtectGit {
		violations = append(violations, "protect_git must be true")
	}
	if p.Workspace.ForbidDocker && ws.EnableDocker {
		violations = append(violations, "enable_docker is forbidden by policy")
	}
	if p.Workspace.ForbidSSHAuthSock && ws.SSHAuthSock {
		violations = append(violations, "ssh_auth_sock is forbidden by policy")
	}
	if p.Workspace.ForbidGPGAgent && ws.GPGAgent {
		violations = append(violations, "gpg_agent is forbidden by policy")
	}
	if p.Workspace.ForbidPorts && len(ws.Ports) > 0 {
		violations = append(violations, "ports are forbidden by policy")
	}
	if p.Workspace.ForbidHostPorts && len(ws.HostPorts) > 0 {
		violations = append(violations, "host_ports are forbidden by policy")
	}
	if p.Workspace.ForbidGitConfig && ws.GitConfig {
		violations = append(violations, "git_config is forbidden by policy")
	}
	if p.Workspace.ForbidImportAgentConfig && ws.ImportAgentConfig {
		violations = append(violations, "import_agent_config is forbidden by policy")
	}
	if p.Workspace.ForbidCustomDockerfile {
		if ws.Dockerfile != "" {
			violations = append(violations, "workspace dockerfile is forbidden by policy")
		}
		if ws.Image != "" {
			violations = append(violations, "workspace image override is forbidden by policy")
		}
	}
	if p.Workspace.AllowedAgents != nil {
		if !agentAllowed(ws, p.Workspace.AllowedAgents) {
			violations = append(violations, fmt.Sprintf("agent %q is not in allowed_agents [%s]", ws.Agent.Name, strings.Join(p.Workspace.AllowedAgents, ", ")))
		}
	}
	return violations
}

func canonicalAgent(name string) string {
	if name == "opencode" {
		return "opencode2"
	}
	return name
}

func agentAllowed(ws *Resolved, allowed []string) bool {
	base := canonicalAgent(ws.AgentBase)
	if base == "" {
		base = canonicalAgent(ws.Agent.Name)
	}
	for _, a := range allowed {
		if canonicalAgent(a) == base || a == ws.Agent.Name {
			return true
		}
	}
	return false
}

func checkFilesystemPolicy(ws *Resolved, p *config.Policy) []string {
	var violations []string

	if p.Filesystem.ForbidRWMounts {
		if len(ws.Paths) > 0 {
			violations = append(violations, "workspace paths are read-write and forbidden by forbid_rw_mounts")
		}
	}

	for _, hostPath := range ws.Paths {
		if p.Filesystem.AllowedPaths != nil && !pathMatchesAnyPrefix(hostPath, p.Filesystem.AllowedPaths) {
			violations = append(violations, fmt.Sprintf("path %q is outside filesystem.allowed_paths", hostPath))
		}
		if pathMatchesAnyPrefix(hostPath, p.Filesystem.ForbiddenMountSources) {
			violations = append(violations, fmt.Sprintf("path %q is forbidden by filesystem.forbidden_mount_sources", hostPath))
		}
	}

	for _, spec := range ws.Mounts {
		m, err := config.ParseMount(spec)
		if err != nil {
			continue
		}
		hostPath, err := config.ExpandPath(m.Host)
		if err != nil {
			hostPath = m.Host
		}
		if p.Filesystem.ForbidRWMounts && m.Mode != "ro" {
			violations = append(violations, fmt.Sprintf("read-write mount %q is forbidden by forbid_rw_mounts", spec))
		}
		if p.Filesystem.AllowedMountSources != nil && !pathMatchesAnyPrefix(hostPath, p.Filesystem.AllowedMountSources) {
			violations = append(violations, fmt.Sprintf("mount source %q is outside filesystem.allowed_mount_sources", hostPath))
		}
		if pathMatchesAnyPrefix(hostPath, p.Filesystem.ForbiddenMountSources) {
			violations = append(violations, fmt.Sprintf("mount source %q is forbidden by filesystem.forbidden_mount_sources", hostPath))
		}
	}

	for _, spec := range ws.Snapshots {
		if strings.HasPrefix(spec, ":") {
			continue
		}
		m, err := config.ParseMount(spec)
		if err != nil {
			continue
		}
		hostPath, err := config.ExpandPath(m.Host)
		if err != nil {
			hostPath = m.Host
		}
		if p.Filesystem.AllowedSnapshotSources != nil && !pathMatchesAnyPrefix(hostPath, p.Filesystem.AllowedSnapshotSources) {
			violations = append(violations, fmt.Sprintf("snapshot source %q is outside filesystem.allowed_snapshot_sources", hostPath))
		}
		if pathMatchesAnyPrefix(hostPath, p.Filesystem.ForbiddenMountSources) {
			violations = append(violations, fmt.Sprintf("snapshot source %q is forbidden by filesystem.forbidden_mount_sources", hostPath))
		}
	}

	return violations
}

func checkNetworkPolicy(ws *Resolved, p *config.Policy) []string {
	var violations []string

	if p.Network.ForbidIPv6 && ws.IPv6 {
		violations = append(violations, "network.ipv6 is forbidden by policy")
	}
	if p.Network.RequireDNS != "" && ws.DNSUpstream != p.Network.RequireDNS {
		violations = append(violations, fmt.Sprintf("network.dns must be %q (got %q)", p.Network.RequireDNS, ws.DNSUpstream))
	}
	if p.Network.RequireRules {
		if len(ws.AllowedHosts) > 0 || len(ws.AllowedNetworks) > 0 {
			violations = append(violations, "legacy allowed_hosts/allowed_networks are forbidden when network.require_rules is set; use network.rules")
		}
	}
	if p.Network.AllowedInternalZones != nil {
		for _, zone := range ws.InternalZones {
			if !internalZoneAllowed(zone, p.Network.AllowedInternalZones) {
				violations = append(violations, fmt.Sprintf("internal zone %q is not in network.allowed_internal_zones", zone))
			}
		}
	}

	for _, rule := range ws.NetworkRules {
		if len(rule.Allow) == 0 {
			continue
		}
		isAgentRule := rule.Name == "agent"
		for _, target := range rule.Allow {
			if p.Network.ForbidWildcardAllow && isWildcardAllow(target) {
				violations = append(violations, fmt.Sprintf("allow target %q is forbidden by network.forbid_wildcard_allow", target))
			}
			if p.Network.ForbidPrivateNetworks && isPrivateNetworkTarget(target) {
				violations = append(violations, fmt.Sprintf("allow target %q is forbidden by network.forbid_private_networks", target))
			}
			if isAgentRule {
				if p.Network.AllowedAgentEndpoints != nil && !targetCoveredByAny(target, p.Network.AllowedAgentEndpoints) {
					violations = append(violations, fmt.Sprintf("agent endpoint %q is not in network.allowed_agent_endpoints", target))
				}
			} else if p.Network.AllowedTargets != nil && !targetCoveredByAny(target, p.Network.AllowedTargets) {
				violations = append(violations, fmt.Sprintf("allow target %q is not in network.allowed_targets", target))
			}
			for _, denied := range p.Network.Deny {
				if targetsOverlap(target, denied) {
					violations = append(violations, fmt.Sprintf("allow target %q conflicts with policy network.deny %q", target, denied))
					break
				}
			}
		}
	}

	return violations
}

func checkSecretsPolicy(ws *Resolved, p *config.Policy) []string {
	var violations []string
	for _, s := range ws.Secrets {
		if p.Secrets.ForbidFromCommand && s.FromCommand != "" {
			violations = append(violations, fmt.Sprintf("secret %q uses from_command which is forbidden by policy", s.Name))
		}
		if p.Secrets.AllowedSecretFiles != nil && s.FromFile != "" {
			if !pathMatchesAnyPrefix(s.FromFile, p.Secrets.AllowedSecretFiles) {
				violations = append(violations, fmt.Sprintf("secret %q file %q is outside secrets.allowed_secret_files", s.Name, s.FromFile))
			}
		}
	}
	return violations
}

func pathMatchesAnyPrefix(path string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return false
	}
	resolvedPath := resolveSymlinksBestEffort(path)
	for _, prefix := range prefixes {
		resolvedPrefix := resolveSymlinksBestEffort(prefix)
		if pathWithinPrefix(resolvedPath, resolvedPrefix) {
			return true
		}
	}
	return false
}

func pathWithinPrefix(path, prefix string) bool {
	if prefix == string(filepath.Separator) {
		return strings.HasPrefix(path, string(filepath.Separator))
	}
	return path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator))
}

// resolveSymlinksBestEffort resolves symlinks for p. When p does not exist, it
// resolves the deepest existing ancestor so a symlinked parent cannot bypass
// prefix checks.
func resolveSymlinksBestEffort(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	var tail []string
	cur := clean
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			slices.Reverse(tail)
			return filepath.Clean(filepath.Join(append([]string{resolved}, tail...)...))
		}
	}
}

func internalZoneAllowed(zone string, allowed []string) bool {
	z := config.NormalizeInternalZone(zone)
	for _, a := range allowed {
		an := config.NormalizeInternalZone(a)
		if z == an || strings.HasSuffix(z, "."+an) {
			return true
		}
	}
	return false
}

func isWildcardAllow(target string) bool {
	kind, norm, err := config.ParseRuleTarget(target)
	if err != nil {
		return false
	}
	if kind == "wildcard" {
		return true
	}
	if kind == "cidr" {
		if pfx, err := netip.ParsePrefix(norm); err == nil && pfx.Bits() == 0 {
			return true
		}
	}
	return false
}

func isPrivateNetworkTarget(target string) bool {
	kind, norm, err := config.ParseRuleTarget(target)
	if err != nil || kind != "cidr" {
		return false
	}
	pfx, err := netip.ParsePrefix(norm)
	if err != nil {
		return false
	}
	for _, priv := range privatePolicyPrefixes {
		if pfx.Overlaps(priv) {
			return true
		}
	}
	return false
}

func targetCoveredByAny(target string, allowed []string) bool {
	for _, a := range allowed {
		if targetCoveredBy(target, a) {
			return true
		}
	}
	return false
}

func targetCoveredBy(target, allowed string) bool {
	tKind, tNorm, err := config.ParseRuleTarget(target)
	if err != nil {
		return false
	}
	aKind, aNorm, err := config.ParseRuleTarget(allowed)
	if err != nil {
		return false
	}
	if aKind == "wildcard" {
		return true
	}
	if tKind != aKind {
		return false
	}
	switch tKind {
	case "name":
		return nameCoveredBy(tNorm, aNorm)
	case "cidr":
		tp, err1 := netip.ParsePrefix(tNorm)
		ap, err2 := netip.ParsePrefix(aNorm)
		if err1 != nil || err2 != nil {
			return false
		}
		return ap.Contains(tp.Addr()) && ap.Bits() <= tp.Bits()
	}
	return false
}

func nameCoveredBy(target, allowed string) bool {
	if target == allowed {
		return true
	}
	if base, ok := strings.CutPrefix(allowed, "*."); ok {
		if tBase, tWild := strings.CutPrefix(target, "*."); tWild {
			return tBase == base || strings.HasSuffix(tBase, "."+base)
		}
		return strings.HasSuffix(target, "."+base)
	}
	return false
}

func targetsOverlap(a, b string) bool {
	aKind, aNorm, err := config.ParseRuleTarget(a)
	if err != nil {
		return false
	}
	bKind, bNorm, err := config.ParseRuleTarget(b)
	if err != nil {
		return false
	}
	if aKind == "wildcard" || bKind == "wildcard" {
		return true
	}
	if aKind != bKind {
		return false
	}
	switch aKind {
	case "name":
		return nameCoveredBy(aNorm, bNorm) || nameCoveredBy(bNorm, aNorm)
	case "cidr":
		ap, err1 := netip.ParsePrefix(aNorm)
		bp, err2 := netip.ParsePrefix(bNorm)
		if err1 != nil || err2 != nil {
			return false
		}
		return ap.Overlaps(bp)
	}
	return false
}
