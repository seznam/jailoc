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

// Policy is an immutable, validated broker policy.
type Policy struct {
	exceptions   []Exception
	dnsUpstreams []netip.AddrPort
	blockedZones []string
	denyAll      bool
	source       string
}

type policyFile struct {
	Exceptions   []Exception `json:"exceptions"`
	DNSUpstreams []string    `json:"dns_upstreams"`
	// DNSBlockedZones makes the gateway resolver answer NXDOMAIN for these
	// zones and their subdomains, and denies guest DNS (port 53) to any
	// other resolver so the filter cannot be bypassed.
	DNSBlockedZones []string `json:"dns_blocked_zones"`
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
	p := &Policy{source: source}
	for _, e := range f.Exceptions {
		if err := e.compile(); err != nil {
			return nil, fmt.Errorf("policy %s: %w", source, err)
		}
		p.exceptions = append(p.exceptions, e)
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

// blocked reports whether name is one of the blocked zones or below one.
func (p *Policy) blocked(name string) bool {
	if p == nil {
		return false
	}
	name = normalizeName(name)
	for _, z := range p.blockedZones {
		if name == z || strings.HasSuffix(name, "."+z) {
			return true
		}
	}
	return false
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
}

// Decision is the outcome for one new guest flow.
type Decision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason"`
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
	case a.Is6():
		return decideIPv6(p, env, proto, netip.AddrPortFrom(a, dst.Port()))
	case inAny(a, hardDeniedV4):
		return Decision{Reason: "hard-deny"}
	case len(p.blockedZones) > 0 && dst.Port() == 53:
		return Decision{Reason: "dns-filtered"}
	}
	if !inAny(a, deniedV4) {
		return Decision{Allow: true, Reason: "public"}
	}
	for i := range p.exceptions {
		if p.exceptions[i].matches(proto, netip.AddrPortFrom(a, dst.Port())) {
			return Decision{Allow: true, Reason: "exception " + p.exceptions[i].CIDR}
		}
	}
	return Decision{Reason: "denied-range"}
}
