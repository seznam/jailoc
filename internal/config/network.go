package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// RuleProto is a protocol name in a network rule.
const (
	RuleProtoTCP = "tcp"
	RuleProtoUDP = "udp"
	RuleProtoAny = "any"
)

// RulePort is a ports entry of a network rule: a single port number or an
// "A-B" port range. TOML accepts integers and strings.
type RulePort string

// UnmarshalTOML accepts an integer or a string.
func (p *RulePort) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case int64:
		*p = RulePort(strconv.FormatInt(v, 10))
	case string:
		*p = RulePort(v)
	default:
		return fmt.Errorf("rule port must be a port number or \"A-B\" range, got %T", v)
	}
	return nil
}

// MarshalText writes the entry as a string.
func (p RulePort) MarshalText() ([]byte, error) { return []byte(p), nil }

// ValidateRulePort checks a port number or "A-B" port range in 1-65535.
func ValidateRulePort(p RulePort) error {
	s := strings.TrimSpace(string(p))
	lo, hi, found := strings.Cut(s, "-")
	if !found {
		hi = lo
	}
	l, err1 := parsePortNumber(lo)
	h, err2 := parsePortNumber(hi)
	if err1 != nil || err2 != nil || l > h {
		return fmt.Errorf("invalid port %q: want a port number 1-65535 or \"A-B\" range", string(p))
	}
	return nil
}

// NetworkRule is one [[<scope>.network.rules]] entry. Exactly one of Allow
// and Deny must be set; Proto defaults to "tcp".
type NetworkRule struct {
	Name  string     `toml:"name,omitempty"`
	Allow []string   `toml:"allow,omitempty"`
	Deny  []string   `toml:"deny,omitempty"`
	Proto string     `toml:"proto,omitempty"`
	Ports []RulePort `toml:"ports,omitempty"`
}

// ParseRuleTarget classifies a target in a network rule's allow or deny list.
// It returns cidr when the target is an IP address or CIDR prefix, wildcard
// when it is "*", or name when it is a domain name or "*.domain" wildcard.
func ParseRuleTarget(target string) (kind string, normalized string, err error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", "", fmt.Errorf("empty rule target")
	}
	if t == "*" {
		return "wildcard", "*", nil
	}
	if strings.Contains(t, "/") {
		pfx, err := netip.ParsePrefix(t)
		if err != nil {
			return "", "", fmt.Errorf("invalid CIDR %q: %w", target, err)
		}
		return "cidr", pfx.Masked().String(), nil
	}
	if addr, err := netip.ParseAddr(t); err == nil {
		addr = addr.Unmap()
		bits := 32
		if addr.Is6() {
			bits = 128
		}
		return "cidr", netip.PrefixFrom(addr, bits).String(), nil
	}
	name := strings.TrimSuffix(strings.ToLower(t), ".")
	check := name
	if rest, ok := strings.CutPrefix(name, "*."); ok {
		if rest == "" || strings.Contains(rest, "*") {
			return "", "", fmt.Errorf("invalid wildcard hostname %q", target)
		}
		check = rest
	} else if strings.Contains(name, "*") {
		return "", "", fmt.Errorf("invalid hostname %q: wildcard must be a leading \"*.\"", target)
	}
	if err := validateRuleHostname(check); err != nil {
		return "", "", fmt.Errorf("invalid hostname %q: %w", target, err)
	}
	return "name", name, nil
}

func validateRuleHostname(name string) error {
	if len(name) == 0 || len(name) > 253 || strings.ContainsAny(name, " \t\r\n{}") {
		return fmt.Errorf("invalid domain name %q", name)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || !dnsLabelRe.MatchString(label) {
			return fmt.Errorf("invalid domain label %q", label)
		}
	}
	return nil
}

func validateNetworkRules(rules []NetworkRule, context string) error {
	for i, r := range rules {
		label := fmt.Sprintf("%s: network.rules[%d]", context, i)
		if r.Name != "" {
			label = fmt.Sprintf("%s: network rule %q", context, r.Name)
		}
		hasAllow := len(r.Allow) > 0
		hasDeny := len(r.Deny) > 0
		if hasAllow == hasDeny {
			if r.Allow != nil && r.Deny != nil {
				return fmt.Errorf("%s: cannot set both \"allow\" and \"deny\"", label)
			}
			return fmt.Errorf("%s: exactly one of \"allow\" or \"deny\" must be set and non-empty", label)
		}
		if r.Allow != nil && r.Deny != nil {
			return fmt.Errorf("%s: cannot set both \"allow\" and \"deny\"", label)
		}
		switch r.Proto {
		case "", RuleProtoTCP, RuleProtoUDP, RuleProtoAny:
		default:
			return fmt.Errorf("%s: invalid proto %q: must be %q, %q or %q", label, r.Proto, RuleProtoTCP, RuleProtoUDP, RuleProtoAny)
		}
		for _, p := range r.Ports {
			if err := ValidateRulePort(p); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
		targets := r.Allow
		if hasDeny {
			targets = r.Deny
		}
		for _, t := range targets {
			if _, _, err := ParseRuleTarget(t); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
	}
	return nil
}

// DeprecationWarnings returns one-line warnings for legacy network settings in
// cfg that are migrated in memory.
func DeprecationWarnings(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	var warnings []string
	collect := func(scope string, allowedHosts, allowedNetworks []string, net *Network) {
		n := net.Get()
		netScope := strings.TrimSuffix(scope, "]") + ".network]"
		rulesScope := strings.TrimSuffix(scope, "]") + ".network.rules]]"
		if len(allowedHosts) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: allowed_hosts is deprecated; use [%s with allow = [...] instead", scope, rulesScope))
		}
		if len(allowedNetworks) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: allowed_networks is deprecated; use [%s with allow = [...] instead", scope, rulesScope))
		}
		if n.FilteredDNS != nil {
			warnings = append(warnings, fmt.Sprintf("%s: filtered_dns is deprecated and ignored (DNS filtering is always on)", netScope))
		}
		if n.DNSUpstream != "" {
			warnings = append(warnings, fmt.Sprintf("%s: dns_upstream is deprecated; use dns = %q instead", netScope, n.DNSUpstream))
		}
		if len(n.DNSBlockedZones) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: dns_blocked_zones is deprecated; use [%s with deny = [...] instead", netScope, rulesScope))
		}
	}
	collect("[defaults]", cfg.Defaults.AllowedHosts, cfg.Defaults.AllowedNetworks, cfg.Defaults.Network)
	names := make([]string, 0, len(cfg.Workspaces))
	for name := range cfg.Workspaces {
		names = append(names, name)
	}
	for _, name := range names {
		ws := cfg.Workspaces[name]
		collect(fmt.Sprintf("[workspaces.%s]", name), ws.AllowedHosts, ws.AllowedNetworks, ws.Network)
	}
	return warnings
}
