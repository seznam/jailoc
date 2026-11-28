package workspace

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/seznam/jailoc/internal/config"
)

// resolveUpstreamProxy returns the upstream proxy of a workspace: its own
// table when it has one, else the defaults' table. A workspace table replaces
// the defaults table as a whole. A disabled table, in either scope, resolves
// to nil. The result is a copy that never aliases cfg.
func resolveUpstreamProxy(defaults, ws *config.UpstreamProxy) *config.UpstreamProxy {
	chosen := defaults
	if ws != nil {
		chosen = ws
	}
	if !chosen.IsEnabled() {
		return nil
	}
	return chosen.Clone()
}

// BuilderUpstreamProxy returns the upstream proxy the image builder uses: the
// static [defaults.network.upstream_proxy], cloned. It is nil when there is
// none, when it is disabled or when it selects the proxy with a command,
// since a builder cannot evaluate a workspace's command. Workspace tables are
// not consulted.
func BuilderUpstreamProxy(cfg *config.Config) *config.UpstreamProxy {
	if cfg == nil {
		return nil
	}
	p := cfg.Defaults.Network.Get().UpstreamProxy
	if !p.IsEnabled() || p.URL == "" || p.Command != "" {
		return nil
	}
	return p.Clone()
}

// ConfigWarnings returns the non-deprecation configuration warnings across
// all workspaces of cfg in sorted workspace order.
func ConfigWarnings(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	defaults := cfg.Defaults
	for _, name := range slices.Sorted(maps.Keys(cfg.Workspaces)) {
		ws := cfg.Workspaces[name]
		p := resolveUpstreamProxy(defaults.Network.Get().UpstreamProxy, ws.Network.Get().UpstreamProxy)
		if p == nil {
			continue
		}
		h, _, err := resolveAgent(cfg, ws)
		if err != nil {
			continue
		}
		hosts := mergeUnique(defaults.AllowedHosts, ws.AllowedHosts)
		networks := mergeUnique(defaults.AllowedNetworks, ws.AllowedNetworks)
		zones := ws.Network.Get().DNSBlockedZones
		if zones == nil {
			zones = defaults.Network.Get().DNSBlockedZones
		}
		rules := resolveNetworkRules(cfg, ws, hosts, networks, zones, h)
		out = append(out, upstreamDirectWarnings(name, p, rules)...)
	}
	return out
}

// upstreamDirectWarnings returns one warning per direct entry of p that no
// allow rule can match. Direct does not bypass the network rules: the broker
// still denies a target the rules do not allow, so such an entry never takes
// effect. An entry is only reported when it is certain that no rule matches;
// a host name against an address rule, for example, depends on DNS answers
// and is never reported.
func upstreamDirectWarnings(name string, p *config.UpstreamProxy, rules []config.NetworkRule) []string {
	var warnings []string
	for _, entry := range p.Direct {
		direct, err := config.ParseUpstreamDirect(entry)
		if err != nil {
			continue
		}
		if !anyRuleMayAllow(rules, direct) {
			warnings = append(warnings, fmt.Sprintf("[workspaces.%s]: network.upstream_proxy.direct entry %q matches no allow rule, so it is denied like any other target; direct does not bypass network rules", name, entry))
		}
	}
	return warnings
}

// anyRuleMayAllow reports whether some allow rule can match the direct
// target, a rule from ParseUpstreamDirect.
func anyRuleMayAllow(rules []config.NetworkRule, direct config.NetworkRule) bool {
	var port string
	if len(direct.Ports) > 0 {
		port = string(direct.Ports[0])
	}
	for _, r := range rules {
		if len(r.Allow) == 0 || !rulePortsMayContain(r.Ports, port) {
			continue
		}
		for _, t := range r.Allow {
			if targetsMayOverlap(t, direct.Allow[0]) {
				return true
			}
		}
	}
	return false
}

// rulePortsMayContain reports whether a rule with the given ports can match
// port; no ports match every port, and an empty port stands for any port.
func rulePortsMayContain(ports []config.RulePort, port string) bool {
	if len(ports) == 0 || port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return true
	}
	for _, p := range ports {
		lo, hi, found := strings.Cut(strings.TrimSpace(string(p)), "-")
		l, err1 := strconv.Atoi(lo)
		h, err2 := l, error(nil)
		if found {
			h, err2 = strconv.Atoi(hi)
		}
		if err1 != nil || err2 != nil || (l <= n && n <= h) {
			return true
		}
	}
	return false
}

// targetsMayOverlap reports whether two rule targets may match a common
// destination. It is false only when they certainly cannot: two address
// ranges that are disjoint, or two names where neither equals nor is a
// subdomain of the other. A name and an address are never ruled out.
func targetsMayOverlap(a, b string) bool {
	ka, na, errA := config.ParseRuleTarget(a)
	kb, nb, errB := config.ParseRuleTarget(b)
	if errA != nil || errB != nil || ka == "wildcard" || kb == "wildcard" || ka != kb {
		return true
	}
	if ka == "cidr" {
		pa, errA := netip.ParsePrefix(na)
		pb, errB := netip.ParsePrefix(nb)
		return errA != nil || errB != nil || pa.Overlaps(pb)
	}
	wa, wb := strings.HasPrefix(na, "*."), strings.HasPrefix(nb, "*.")
	na, nb = strings.TrimPrefix(na, "*."), strings.TrimPrefix(nb, "*.")
	switch {
	case wa && wb:
		return na == nb || strings.HasSuffix(na, "."+nb) || strings.HasSuffix(nb, "."+na)
	case wa:
		return strings.HasSuffix(nb, "."+na)
	case wb:
		return strings.HasSuffix(na, "."+nb)
	default:
		return na == nb
	}
}
