// Package vmprobe implements the executable acceptance probes for VM
// isolation backends (see docs/research/vm-acceptance-spec.md).
package vmprobe

import (
	"fmt"
	"net/netip"
)

// DeniedRange is a destination range that the egress policy must deny unless
// an explicit exception opens it.
type DeniedRange struct {
	Prefix netip.Prefix
	Name   string
}

func mustPrefixes(defs [][2]string) []DeniedRange {
	out := make([]DeniedRange, 0, len(defs))
	for _, d := range defs {
		out = append(out, DeniedRange{Prefix: netip.MustParsePrefix(d[0]), Name: d[1]})
	}
	return out
}

// DeniedV4 is the normative IPv4 deny list (spec section N.1).
var DeniedV4 = mustPrefixes([][2]string{
	{"0.0.0.0/8", "this-network"},
	{"10.0.0.0/8", "rfc1918"},
	{"100.64.0.0/10", "cgnat"},
	{"127.0.0.0/8", "loopback"},
	{"169.254.0.0/16", "link-local"},
	{"172.16.0.0/12", "rfc1918"},
	{"192.0.0.0/24", "ietf-protocol-assignments"},
	{"192.0.2.0/24", "test-net-1"},
	{"192.88.99.0/24", "6to4-relay-anycast"},
	{"192.168.0.0/16", "rfc1918"},
	{"198.18.0.0/15", "benchmarking"},
	{"198.51.100.0/24", "test-net-2"},
	{"203.0.113.0/24", "test-net-3"},
	{"224.0.0.0/4", "multicast"},
	{"240.0.0.0/4", "reserved-and-broadcast"},
})

// DeniedV6 is the normative IPv6 deny list (spec section N.1). The
// IPv4-mapped and NAT64 well-known prefixes are not listed: their embedded
// IPv4 address is evaluated instead.
var DeniedV6 = mustPrefixes([][2]string{
	{"::/128", "unspecified"},
	{"::1/128", "loopback"},
	{"64:ff9b:1::/48", "local-nat64"},
	{"100::/64", "discard-only"},
	{"2001::/23", "ietf-protocol-assignments-incl-teredo"},
	{"2001:db8::/32", "documentation"},
	{"2002::/16", "6to4"},
	{"fc00::/7", "unique-local"},
	{"fe80::/10", "link-local"},
	{"fec0::/10", "site-local-deprecated"},
	{"ff00::/8", "multicast"},
})

var nat64WKP = netip.MustParsePrefix("64:ff9b::/96")

// Classification is the policy class of a destination address.
type Classification struct {
	Public bool
	// Range names the deny range that matched; empty when Public.
	Range string
}

func (c Classification) String() string {
	if c.Public {
		return "public"
	}
	return "denied:" + c.Range
}

// Classify evaluates an address against the normative deny lists.
// IPv4-mapped and NAT64 well-known-prefix addresses are evaluated by their
// embedded IPv4 address.
func Classify(a netip.Addr) Classification {
	a = a.WithZone("")
	if a.Is4In6() {
		inner := Classify(a.Unmap())
		if !inner.Public {
			inner.Range = "ipv4-mapped/" + inner.Range
		}
		return inner
	}
	if a.Is6() && nat64WKP.Contains(a) {
		b := a.As16()
		inner := Classify(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		if !inner.Public {
			inner.Range = "nat64/" + inner.Range
		}
		return inner
	}
	list := DeniedV6
	if a.Is4() {
		list = DeniedV4
	}
	for _, r := range list {
		if r.Prefix.Contains(a) {
			return Classification{Range: r.Name}
		}
	}
	return Classification{Public: true}
}

// Exception opens a non-public destination for a protocol and port set.
// An empty Ports slice means all ports.
type Exception struct {
	CIDR  string `json:"cidr"`
	Proto string `json:"proto"` // "tcp", "udp" or "any"
	Ports []int  `json:"ports,omitempty"`
}

// Validate checks the exception syntax.
func (e Exception) Validate() error {
	if _, err := netip.ParsePrefix(e.CIDR); err != nil {
		return fmt.Errorf("exception cidr %q: %w", e.CIDR, err)
	}
	switch e.Proto {
	case "tcp", "udp", "any":
	default:
		return fmt.Errorf("exception proto %q: want tcp, udp or any", e.Proto)
	}
	for _, p := range e.Ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("exception port %d out of range", p)
		}
	}
	return nil
}

// Matches reports whether the exception opens addr:port over proto.
func (e Exception) Matches(addr netip.Addr, proto string, port int) bool {
	pfx, err := netip.ParsePrefix(e.CIDR)
	if err != nil || !pfx.Contains(addr.WithZone("")) {
		return false
	}
	if e.Proto != "any" && e.Proto != proto {
		return false
	}
	if len(e.Ports) == 0 {
		return true
	}
	for _, p := range e.Ports {
		if p == port {
			return true
		}
	}
	return false
}
