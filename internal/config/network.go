package config

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/seznam/jailoc/internal/agent"
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

// CanonicalRulePort validates and returns the canonical "PORT" or "LO-HI"
// string for p.
func CanonicalRulePort(p RulePort) (string, error) {
	s := strings.TrimSpace(string(p))
	lo, hi, found := strings.Cut(s, "-")
	if !found {
		n, err := parsePortNumber(lo)
		if err != nil {
			return "", fmt.Errorf("invalid port %q: want a port number 1-65535 or \"A-B\" range", string(p))
		}
		return strconv.Itoa(n), nil
	}
	l, err1 := parsePortNumber(lo)
	h, err2 := parsePortNumber(hi)
	if err1 != nil || err2 != nil || l > h {
		return "", fmt.Errorf("invalid port %q: want a port number 1-65535 or \"A-B\" range", string(p))
	}
	return strconv.Itoa(l) + "-" + strconv.Itoa(h), nil
}

// ValidateRulePort checks a port number or "A-B" port range in 1-65535.
func ValidateRulePort(p RulePort) error {
	_, err := CanonicalRulePort(p)
	return err
}

// NetworkRule is one [[<scope>.network.rules]] entry. Exactly one of Allow
// and Deny must be set; Proto defaults to "tcp".
type NetworkRule struct {
	Name  string     `toml:"name,omitempty"`
	Allow []string   `toml:"allow,omitempty"`
	Deny  []string   `toml:"deny,omitempty"`
	Proto string     `toml:"proto,omitempty"`
	Ports []RulePort `toml:"ports,omitempty"`
	// HTTP, when set, narrows the rule to matching HTTP requests: the broker
	// inspects TLS and plain HTTP traffic to the targets and applies the rule
	// per request. An empty table matches every request.
	HTTP *HTTPMatch `toml:"http"`
	// Log, when false, makes `jailoc traffic` hide the events this rule
	// decides unless --all is given. The broker still records them. Unset
	// means true.
	Log *bool `toml:"log"`
}

// Quiet reports whether the rule's events are hidden from `jailoc traffic`
// by default: log = false.
func (r NetworkRule) Quiet() bool { return r.Log != nil && !*r.Log }

// HTTPMatch is the http sub-table of a network rule. Empty Methods or Paths
// match any method or path.
type HTTPMatch struct {
	Methods []string `toml:"methods,omitempty"`
	Paths   []string `toml:"paths,omitempty"`
}

// NormalizeHTTPMethod validates an HTTP method of an http rule and returns
// it in upper case. "*" matches any method; anything else must be an RFC 9110
// token.
func NormalizeHTTPMethod(m string) (string, error) {
	if m == "" {
		return "", fmt.Errorf("empty HTTP method")
	}
	if m == "*" {
		return m, nil
	}
	for i := 0; i < len(m); i++ {
		if !isHTTPTokenChar(m[i]) {
			return "", fmt.Errorf("invalid HTTP method %q: must be \"*\" or an RFC 9110 token", m)
		}
	}
	return strings.ToUpper(m), nil
}

func isHTTPTokenChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// ValidateHTTPPathPattern checks a path pattern of an http rule. It starts
// with "/" and is split on "/" into segments; "*" matches exactly one
// non-empty segment and "**" zero or more trailing segments (last segment
// only). Partial wildcards, "?", "#", "%", "\\", control characters, "." and
// ".." segments and empty middle segments are invalid; a trailing "/" is a
// trailing empty segment matched literally.
func ValidateHTTPPathPattern(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("invalid HTTP path pattern %q: must start with \"/\"", p)
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < 0x20 || c == 0x7f || strings.IndexByte("?#%\\", c) >= 0 {
			return fmt.Errorf("invalid HTTP path pattern %q: character %q is not allowed", p, c)
		}
	}
	segs := strings.Split(p[1:], "/")
	for i, seg := range segs {
		last := i == len(segs)-1
		switch {
		case seg == "**":
			if !last {
				return fmt.Errorf("invalid HTTP path pattern %q: \"**\" is only allowed as the last segment", p)
			}
		case seg == "." || seg == "..":
			return fmt.Errorf("invalid HTTP path pattern %q: %q segments are not allowed", p, seg)
		case strings.Contains(seg, "*") && seg != "*":
			return fmt.Errorf("invalid HTTP path pattern %q: wildcards must be a whole segment", p)
		case seg == "" && !last:
			return fmt.Errorf("invalid HTTP path pattern %q: empty segment", p)
		}
	}
	return nil
}

func validateHTTPMatch(m *HTTPMatch, proto string) error {
	if proto != "" && proto != RuleProtoTCP {
		return fmt.Errorf("\"http\" requires proto %q, got %q", RuleProtoTCP, proto)
	}
	for _, method := range m.Methods {
		if _, err := NormalizeHTTPMethod(method); err != nil {
			return err
		}
	}
	for _, p := range m.Paths {
		if err := ValidateHTTPPathPattern(p); err != nil {
			return err
		}
	}
	return nil
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
		if pfx.Addr().Is4In6() && pfx.Bits() >= 96 {
			pfx = netip.PrefixFrom(pfx.Addr().Unmap(), pfx.Bits()-96)
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
		if r.HTTP != nil {
			if err := validateHTTPMatch(r.HTTP, r.Proto); err != nil {
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

// wildcardDNSZones are public services that answer any name under them with
// the address written into the name (10-0-0-5.nip.io → 10.0.0.5). Anyone can
// point such a name anywhere, so they can never be internal zones.
var wildcardDNSZones = []string{"nip.io", "sslip.io", "xip.io", "localtest.me", "lvh.me", "traefik.me", "vcap.me", "local.gd"}

// NormalizeInternalZone lowercases zone and drops a trailing dot and a
// leading "*.", since an internal zone always covers its subdomains.
func NormalizeInternalZone(zone string) string {
	z := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	return strings.TrimPrefix(z, "*.")
}

// ValidateInternalZone checks one internal_zones entry. A zone must be a
// domain someone owns: not "*", not a top-level domain or other public
// suffix (com, co.uk, github.io) and not a wildcard DNS service. Unlisted
// top-level names such as internal, corp or lan, and home.arpa, are private
// by convention and allowed.
func ValidateInternalZone(zone string) error {
	z := NormalizeInternalZone(zone)
	if z == "" || strings.Contains(z, "*") {
		return fmt.Errorf("invalid internal_zones entry %q: must be a domain name", zone)
	}
	if err := validateRuleHostname(z); err != nil {
		return fmt.Errorf("invalid internal_zones entry %q: %w", zone, err)
	}
	if suffix, icann := publicsuffix.PublicSuffix(z); z == suffix && (icann || strings.Contains(suffix, ".")) && z != "home.arpa" {
		return fmt.Errorf("internal_zones entry %q is a public suffix: anyone can register names under it", zone)
	}
	for _, w := range wildcardDNSZones {
		if z == w || strings.HasSuffix(z, "."+w) {
			return fmt.Errorf("internal_zones entry %q is a wildcard DNS service that resolves names to any address", zone)
		}
	}
	return nil
}

// validateInternalZones checks the internal_zones of a [<scope>.network]
// table.
func validateInternalZones(zones []string, context string) error {
	for _, zone := range zones {
		if err := ValidateInternalZone(zone); err != nil {
			return fmt.Errorf("%s: %w", context, err)
		}
	}
	return nil
}

// validateAgentEndpoints checks the agent_endpoints of a [<scope>.network]
// table: each entry is a host name or a "*.domain" wildcard.
func validateAgentEndpoints(endpoints []string, context string) error {
	for _, e := range endpoints {
		kind, _, err := ParseRuleTarget(e)
		if err != nil {
			return fmt.Errorf("%s: agent_endpoints: %w", context, err)
		}
		if kind != "name" {
			return fmt.Errorf("%s: agent_endpoints entry %q must be a host name or a \"*.domain\" wildcard", context, e)
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
	slices.Sort(names)
	for _, name := range names {
		ws := cfg.Workspaces[name]
		collect(fmt.Sprintf("[workspaces.%s]", name), ws.AllowedHosts, ws.AllowedNetworks, ws.Network)
	}
	hasRules := len(cfg.Defaults.Network.Get().Rules) > 0
	for _, ws := range cfg.Workspaces {
		if len(ws.Network.Get().Rules) > 0 {
			hasRules = true
			break
		}
	}
	if !hasRules && len(cfg.Workspaces) > 0 {
		warnings = append(warnings, "no [[*.network.rules]] configured: only the agent's endpoints are allowed; add allow = [\"*\"] or configure [[defaults.network.rules]]")
	}
	defaultEndpoints := cfg.Defaults.Network.Get().AgentEndpoints
	for _, name := range names {
		ws := cfg.Workspaces[name]
		if defaultEndpoints != nil || ws.Network.Get().AgentEndpoints != nil || !explicitEndpoints(cfg, ws) {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("[workspaces.%s]: no network.agent_endpoints configured: the coding agent cannot reach its model and auth endpoints; set agent_endpoints in [defaults.network]", name))
	}

	return warnings
}

// explicitEndpoints reports whether the workspace's agent is a multi-provider
// agent whose endpoints come only from network.agent_endpoints.
func explicitEndpoints(cfg *Config, ws Workspace) bool {
	name := cmp.Or(ws.Agent, cfg.Defaults.Agent, agent.Default)
	h, ok := agent.Lookup(name)
	if !ok {
		h, ok = agent.Lookup(cmp.Or(ws.Agents[name].From, cfg.Defaults.Agents[name].From))
	}
	return ok && h.ExplicitEndpoints
}

// LegacyHostZones returns the internal zones that stand for legacy
// allowed_hosts entries. allowed_hosts opened whatever its names resolved to,
// private addresses included, so each name stays trusted for private
// answers. IP addresses and CIDRs are ignored; names that cannot be internal
// zones (public suffixes, wildcard DNS services) are returned in skipped.
func LegacyHostZones(hosts []string) (zones, skipped []string) {
	for _, h := range hosts {
		kind, name, err := ParseRuleTarget(h)
		if err != nil || kind != "name" {
			continue
		}
		if ValidateInternalZone(name) != nil {
			skipped = append(skipped, h)
			continue
		}
		zones = append(zones, NormalizeInternalZone(name))
	}
	return zones, skipped
}
