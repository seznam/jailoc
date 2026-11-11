// Package netbroker is the host-side network broker for VM workspaces. The
// guest's only NIC is a virtio-net device whose frames arrive on a unix
// socket; a userspace TCP/IP stack terminates every guest connection, and the
// broker opens a host socket only after the real destination passes the
// policy. When the broker is gone the guest has no network at all.
package netbroker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Protocol names used in policy exceptions and decisions.
const (
	ProtoTCP = "tcp"
	ProtoUDP = "udp"
	ProtoAny = "any"
)

// deniedV4 are destinations the guest may reach only through an exception.
var deniedV4 = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
)

// hardDeniedV4 are destinations no exception can open.
var hardDeniedV4 = mustPrefixes(
	"0.0.0.0/8", "127.0.0.0/8", "169.254.169.254/32", "169.254.170.2/32", "224.0.0.0/4", "240.0.0.0/4",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func inAny(a netip.Addr, pfx []netip.Prefix) bool {
	for _, p := range pfx {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

type portRange struct{ lo, hi uint16 }

// Exception opens a denied (not hard-denied) destination range.
type Exception struct {
	CIDR  string   `json:"cidr"`
	Proto string   `json:"proto"`
	Ports []string `json:"ports,omitempty"`

	prefix netip.Prefix
	ranges []portRange
}

func (e *Exception) compile() error {
	pfx, err := netip.ParsePrefix(e.CIDR)
	if err != nil {
		return fmt.Errorf("exception cidr %q: %w", e.CIDR, err)
	}
	e.prefix = pfx.Masked()
	switch e.Proto {
	case ProtoTCP, ProtoUDP, ProtoAny:
	default:
		return fmt.Errorf("exception %q: proto %q, want tcp, udp or any", e.CIDR, e.Proto)
	}
	for _, s := range e.Ports {
		lo, hi, found := strings.Cut(s, "-")
		if !found {
			hi = lo
		}
		l, err1 := strconv.ParseUint(lo, 10, 16)
		h, err2 := strconv.ParseUint(hi, 10, 16)
		if err1 != nil || err2 != nil || l == 0 || l > h {
			return fmt.Errorf("exception %q: invalid port range %q", e.CIDR, s)
		}
		e.ranges = append(e.ranges, portRange{uint16(l), uint16(h)})
	}
	return nil
}

func (e *Exception) matches(proto string, dst netip.AddrPort) bool {
	if !e.prefix.Contains(dst.Addr()) || (e.Proto != ProtoAny && e.Proto != proto) {
		return false
	}
	if len(e.ranges) == 0 {
		return true
	}
	for _, r := range e.ranges {
		if dst.Port() >= r.lo && dst.Port() <= r.hi {
			return true
		}
	}
	return false
}

// Rule is one allow or deny rule in the broker policy.
type Rule struct {
	Name  string   `json:"name,omitempty"`
	Names []string `json:"names,omitempty"`
	CIDRs []string `json:"cidrs,omitempty"`
	Proto string   `json:"proto,omitempty"`
	Ports []string `json:"ports,omitempty"`

	nameSet  nameSet
	prefixes []netip.Prefix
	wildcard bool
	ranges   []portRange
}

func (r *Rule) compile(kind string) error {
	if r.Proto == "" {
		r.Proto = ProtoTCP
	}
	switch r.Proto {
	case ProtoTCP, ProtoUDP, ProtoAny:
	default:
		return fmt.Errorf("%s rule %q: proto %q, want tcp, udp or any", kind, r.Name, r.Proto)
	}
	for _, s := range r.Ports {
		lo, hi, found := strings.Cut(s, "-")
		if !found {
			hi = lo
		}
		l, err1 := strconv.ParseUint(lo, 10, 16)
		h, err2 := strconv.ParseUint(hi, 10, 16)
		if err1 != nil || err2 != nil || l == 0 || l > h {
			return fmt.Errorf("%s rule %q: invalid port range %q", kind, r.Name, s)
		}
		r.ranges = append(r.ranges, portRange{uint16(l), uint16(h)})
	}
	for _, c := range r.CIDRs {
		if c == "*" {
			r.wildcard = true
			continue
		}
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			if addr, aerr := netip.ParseAddr(c); aerr == nil {
				addr = addr.Unmap()
				bits := 32
				if addr.Is6() {
					bits = 128
				}
				pfx = netip.PrefixFrom(addr, bits)
			} else {
				return fmt.Errorf("%s rule %q: cidr %q: %w", kind, r.Name, c, err)
			}
		}
		if pfx.Addr().Is4In6() && pfx.Bits() >= 96 {
			pfx = netip.PrefixFrom(pfx.Addr().Unmap(), pfx.Bits()-96)
		}
		r.prefixes = append(r.prefixes, pfx.Masked())
	}
	var names []string
	for _, n := range r.Names {
		if n == "*" {
			r.wildcard = true
			continue
		}
		n = normalizeName(n)
		if n == "" {
			return fmt.Errorf("%s rule %q: empty name", kind, r.Name)
		}
		names = append(names, n)
	}
	r.nameSet = newNameSet(names)
	if !r.wildcard && len(r.prefixes) == 0 && r.nameSet.empty() {
		return fmt.Errorf("%s rule %q: rule has no names or cidrs", kind, r.Name)
	}
	return nil
}

func (r *Rule) matchesProtoPort(proto string, port uint16) bool {
	if r.Proto != ProtoAny && r.Proto != proto {
		return false
	}
	if len(r.ranges) == 0 {
		return true
	}
	for _, rng := range r.ranges {
		if port >= rng.lo && port <= rng.hi {
			return true
		}
	}
	return false
}

// matchesAddr reports whether r matches dst by CIDR or public wildcard "*".
func (r *Rule) matchesAddr(proto string, dst netip.AddrPort, isPublic bool) bool {
	if !r.matchesProtoPort(proto, dst.Port()) {
		return false
	}
	addr := dst.Addr()
	for _, pfx := range r.prefixes {
		if pfx.Contains(addr) {
			return true
		}
	}
	if r.wildcard && isPublic {
		// Wildcard "*" never opens DoT (853) or QUIC (UDP 443) unless the rule
		// explicitly lists those ports.
		if len(r.ranges) == 0 && (dst.Port() == 853 || (proto == ProtoUDP && dst.Port() == 443)) {
			return false
		}
		return true
	}
	return false
}

// matchesName reports whether r matches name on proto and port.
func (r *Rule) matchesName(proto string, port uint16, name string) bool {
	if !r.matchesProtoPort(proto, port) {
		return false
	}
	return r.nameSet.matches(name)
}

// Policy is an immutable, validated broker policy.
type Policy struct {
	exceptions   []Exception
	allow        []Rule
	deny         []Rule
	rulesMode    bool
	dnsUpstreams []netip.AddrPort
	blockedZones []string
	denyAll      bool
	ipv6         bool
	source       string
}

type policyFile struct {
	DNS          string      `json:"dns"`
	Allow        *[]Rule     `json:"allow"`
	Deny         *[]Rule     `json:"deny"`
	Exceptions   []Exception `json:"exceptions"`
	DNSUpstreams []string    `json:"dns_upstreams"`
	// DNSBlockedZones makes the gateway resolver answer NXDOMAIN for these
	// zones and their subdomains, and denies guest DNS (port 53) to any
	// other resolver so the filter cannot be bypassed.
	DNSBlockedZones []string `json:"dns_blocked_zones"`
	// IPv6 allows guest flows to IPv6 destinations. Without it the guest
	// network is IPv4 only and every IPv6 flow is denied.
	IPv6 bool `json:"ipv6"`
}

// ParsePolicy validates a JSON policy. Unknown fields are rejected so a typo
// cannot silently widen or drop a rule.
func ParsePolicy(data []byte, source string) (*Policy, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f policyFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse policy %s: %w", source, err)
	}
	p := &Policy{source: source, ipv6: f.IPv6, rulesMode: f.Allow != nil || f.Deny != nil || f.DNS != ""}
	for _, e := range f.Exceptions {
		if err := e.compile(); err != nil {
			return nil, fmt.Errorf("policy %s: %w", source, err)
		}
		p.exceptions = append(p.exceptions, e)
	}
	if f.Allow != nil {
		for _, r := range *f.Allow {
			if err := r.compile("allow"); err != nil {
				return nil, fmt.Errorf("policy %s: %w", source, err)
			}
			p.allow = append(p.allow, r)
		}
	}
	if f.Deny != nil {
		for _, r := range *f.Deny {
			if err := r.compile("deny"); err != nil {
				return nil, fmt.Errorf("policy %s: %w", source, err)
			}
			p.deny = append(p.deny, r)
		}
	}
	if f.DNS != "" {
		ap, err := parseAddrPort(f.DNS, 53)
		if err != nil {
			return nil, fmt.Errorf("policy %s: dns: %w", source, err)
		}
		p.dnsUpstreams = append(p.dnsUpstreams, ap)
	}
	for _, s := range f.DNSUpstreams {
		ap, err := parseAddrPort(s, 53)
		if err != nil {
			return nil, fmt.Errorf("policy %s: dns upstream: %w", source, err)
		}
		p.dnsUpstreams = append(p.dnsUpstreams, ap)
	}
	for _, z := range f.DNSBlockedZones {
		z = normalizeName(z)
		if z == "" {
			return nil, fmt.Errorf("policy %s: empty blocked DNS zone", source)
		}
		p.blockedZones = append(p.blockedZones, z)
	}
	if len(p.blockedZones) > 0 && len(p.dnsUpstreams) == 0 {
		return nil, fmt.Errorf("policy %s: dns_blocked_zones requires dns_upstreams", source)
	}
	return p, nil
}

// normalizeName lowercases a DNS name and drops its trailing dot.
func normalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// denied reports whether name is explicitly blocked by blockedZones or a
// blanket deny rule.
func (p *Policy) denied(name string) (bool, string) {
	if p == nil {
		return false, ""
	}
	name = normalizeName(name)
	for _, z := range p.blockedZones {
		if name == z || strings.HasSuffix(name, "."+z) {
			return true, ""
		}
	}
	for i := range p.deny {
		r := &p.deny[i]
		// Only a blanket deny rule (proto=any and all ports, or proto=tcp with
		// all ports when no UDP allow rule matches name) blocks DNS resolution
		// outright; port-scoped denies are enforced at connection time so other
		// allowed ports on the same host keep working.
		if (r.nameSet.matches(name) || r.wildcard) && p.deniesAllForName(r, name) {
			return true, r.Name
		}
	}
	return false, ""
}

// blocked reports whether name is blocked by blockedZones or by DNS rules
// (matching a blanket deny rule, or not allowed when rulesMode is active).
func (p *Policy) blocked(name string) (bool, string) {
	if p == nil {
		return false, ""
	}
	if denied, rule := p.denied(name); denied {
		return true, rule
	}
	if !p.rulesMode {
		return false, ""
	}
	name = normalizeName(name)
	for i := range p.allow {
		r := &p.allow[i]
		if r.wildcard || r.nameSet.matches(name) {
			return false, r.Name
		}
	}
	return true, ""
}

// deniesAllForName reports whether deny rule d leaves no allowed proto/port
// combination for name.
func (p *Policy) deniesAllForName(d *Rule, name string) bool {
	if len(d.ranges) == 0 && d.Proto == ProtoAny {
		return true
	}
	for i := range p.allow {
		a := &p.allow[i]
		if !a.wildcard && !a.nameSet.matches(name) {
			continue
		}
		if !ruleCovers(&d.Proto, d.ranges, a.Proto, a.ranges) {
			return false
		}
	}
	return len(d.ranges) == 0
}

func ruleCovers(denyProto *string, denyRanges []portRange, allowProto string, allowRanges []portRange) bool {
	if *denyProto != ProtoAny && *denyProto != allowProto {
		return false
	}
	if len(denyRanges) == 0 {
		return true
	}
	if len(allowRanges) == 0 {
		return false
	}
	for _, ar := range allowRanges {
		covered := false
		for _, dr := range denyRanges {
			if dr.lo <= ar.lo && dr.hi >= ar.hi {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// needsNameInspection reports whether a TCP flow to dst on port requires
// reading the TLS SNI or HTTP Host header before dialing upstream.
func (p *Policy) needsNameInspection(dst netip.AddrPort, byName bool) bool {
	if p == nil || !p.rulesMode {
		return false
	}
	// If the flow was authorized via a DNS-cached name, verify the TLS SNI /
	// HTTP Host matches the policy and belongs to the resolved IP.
	if byName {
		return true
	}
	// If any deny rule has name patterns on this port, inspect to prevent
	// bypassing name-based denies via a shared IP or wildcard allow.
	for i := range p.deny {
		if !p.deny[i].nameSet.empty() && p.deny[i].matchesProtoPort(ProtoTCP, dst.Port()) {
			return true
		}
	}
	return false
}

// isHTTPOrTLSPort reports whether port is a standard HTTP or TLS port where the
// client always speaks first with a TLS ClientHello or HTTP request.
func isHTTPOrTLSPort(port uint16) bool {
	switch port {
	case 80, 443, 8080, 8443:
		return true
	default:
		return false
	}
}

// DenyAll is the fail-closed policy installed when a reload fails.
func DenyAll(reason string) *Policy {
	return &Policy{denyAll: true, source: reason}
}

func parseAddrPort(s string, defPort uint16) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("address %q: %w", s, err)
	}
	return netip.AddrPortFrom(a, defPort), nil
}

// Env is the per-decision view of the host and the virtual network.
type Env struct {
	Gateway   netip.Addr
	Subnet    netip.Prefix
	HostAddrs map[netip.Addr]bool
	Names     []string
}

// Decision is the outcome for one new guest flow.
type Decision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason"`
	Name   string `json:"name,omitempty"`
	Rule   string `json:"rule,omitempty"`
	byName bool
}

// Decide evaluates a new guest flow to dst. Gateway services (DNS, DHCP) are
// served by the broker itself and never reach Decide.
func Decide(p *Policy, env Env, proto string, dst netip.AddrPort) Decision {
	a := dst.Addr().WithZone("")
	switch {
	case p == nil || p.denyAll:
		return Decision{Reason: "fail-closed"}
	case !a.IsValid():
		return Decision{Reason: "invalid-address"}
	case a == env.Gateway:
		return Decision{Reason: "gateway"}
	case env.Subnet.Contains(a):
		return Decision{Reason: "guest-network"}
	case env.HostAddrs[a]:
		return Decision{Reason: "host-address"}
	case a.Is6() && !p.ipv6:
		return Decision{Reason: "ipv6-disabled"}
	case a.Is6():
		return decideIPv6(p, env, proto, netip.AddrPortFrom(a, dst.Port()))
	case inAny(a, hardDeniedV4):
		return Decision{Reason: "hard-deny"}
	case (p.rulesMode || len(p.blockedZones) > 0) && dst.Port() == 53:
		return Decision{Reason: "dns-filtered"}
	}
	isPublic := !inAny(a, deniedV4)
	if p.rulesMode {
		return decideRules(p, env, proto, netip.AddrPortFrom(a, dst.Port()), isPublic, "denied-range")
	}
	for i := range p.deny {
		if d, ok := matchRule(&p.deny[i], env.Names, proto, netip.AddrPortFrom(a, dst.Port()), isPublic, false); ok {
			return d
		}
	}
	if isPublic {
		return Decision{Allow: true, Reason: "public", Name: firstName(env.Names)}
	}
	for i := range p.exceptions {
		if p.exceptions[i].matches(proto, netip.AddrPortFrom(a, dst.Port())) {
			return Decision{Allow: true, Reason: "exception " + p.exceptions[i].CIDR, Name: firstName(env.Names)}
		}
	}
	return Decision{Reason: "denied-range", Name: firstName(env.Names)}
}

func decideRules(p *Policy, env Env, proto string, dst netip.AddrPort, isPublic bool, privateDenyReason string) Decision {
	for i := range p.deny {
		if d, ok := matchRule(&p.deny[i], env.Names, proto, dst, isPublic, false); ok {
			return d
		}
	}
	for i := range p.allow {
		if d, ok := matchRule(&p.allow[i], env.Names, proto, dst, isPublic, true); ok {
			return d
		}
	}
	for i := range p.exceptions {
		if p.exceptions[i].matches(proto, dst) {
			return Decision{Allow: true, Reason: "exception " + p.exceptions[i].CIDR, Name: firstName(env.Names)}
		}
	}
	if !isPublic {
		return Decision{Reason: privateDenyReason, Name: firstName(env.Names)}
	}
	return Decision{Reason: "default-deny", Name: firstName(env.Names)}
}

func matchRule(r *Rule, names []string, proto string, dst netip.AddrPort, isPublic, allow bool) (Decision, bool) {
	reason := "rule"
	if r.Name != "" {
		reason = "rule " + r.Name
	}
	for _, name := range names {
		// Name rules only open public addresses unless the rule also matches by CIDR;
		// deny rules match any address whose name matches.
		if (!allow || isPublic) && r.matchesName(proto, dst.Port(), name) {
			return Decision{Allow: allow, Reason: reason, Name: name, Rule: r.Name, byName: true}, true
		}
	}
	if r.matchesAddr(proto, dst, isPublic) {
		return Decision{Allow: allow, Reason: reason, Name: firstName(names), Rule: r.Name}, true
	}
	return Decision{}, false
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}
