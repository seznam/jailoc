package netbroker

import (
	"net/netip"

	"github.com/seznam/jailoc/internal/vmnet"
)

var (
	guestSubnet6 = netip.MustParsePrefix(vmnet.Subnet6)
	gateway6     = netip.MustParseAddr(vmnet.Gateway6)
	linkLocal6   = netip.MustParseAddr(vmnet.LinkLocal6)
	nat64        = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour    = netip.MustParsePrefix("2002::/16")
	public6      = netip.MustParsePrefix("2000::/3")
)

var hardDeniedV6 = []struct {
	prefix netip.Prefix
	reason string
}{
	{netip.MustParsePrefix("::/128"), "ipv6-unspecified"},
	{netip.MustParsePrefix("::1/128"), "ipv6-loopback"},
	{netip.MustParsePrefix("::/96"), "ipv4-compatible"},
	{netip.MustParsePrefix("64:ff9b:1::/48"), "nat64-local"},
	{netip.MustParsePrefix("2001::/32"), "teredo"},
	{netip.MustParsePrefix("fe80::/10"), "ipv6-link-local"},
	{netip.MustParsePrefix("fec0::/10"), "ipv6-site-local"},
	{netip.MustParsePrefix("ff00::/8"), "ipv6-multicast"},
	{netip.MustParsePrefix("2001:db8::/32"), "ipv6-documentation"},
	{netip.MustParsePrefix("3fff::/20"), "ipv6-documentation"},
	{netip.MustParsePrefix("100::/64"), "ipv6-discard"},
	{netip.MustParsePrefix("2001:20::/28"), "orchidv2"},
	{netip.MustParsePrefix("2001:2::/48"), "ipv6-benchmark"},
	{netip.MustParsePrefix("2001:10::/28"), "orchid"},
	{netip.MustParsePrefix("fd00:ec2::254/128"), "ipv6-metadata"},
	{netip.MustParsePrefix("fd00:ec2::23/128"), "ipv6-metadata"},
}

func decideIPv6(policy *Policy, env Env, proto string, dst netip.AddrPort) Decision {
	address := dst.Addr()
	switch {
	case address == gateway6 || address == linkLocal6:
		return Decision{Reason: "gateway"}
	case guestSubnet6.Contains(address):
		return Decision{Reason: "guest-network"}
	}
	for _, denied := range hardDeniedV6 {
		if denied.prefix.Contains(address) {
			return Decision{Reason: denied.reason}
		}
	}
	if embedded, ok := embeddedIPv4(address); ok {
		return Decide(policy, env, proto, netip.AddrPortFrom(embedded, dst.Port()))
	}
	if len(policy.blockedZones) > 0 && dst.Port() == 53 {
		return Decision{Reason: "dns-filtered"}
	}
	if address.IsPrivate() {
		for _, exception := range policy.exceptions {
			if exception.matches(proto, dst) {
				return Decision{Allow: true, Reason: "exception " + exception.CIDR}
			}
		}
		return Decision{Reason: "ipv6-private"}
	}
	if !public6.Contains(address) {
		return Decision{Reason: "ipv6-reserved"}
	}
	return Decision{Allow: true, Reason: "public"}
}

func embeddedIPv4(address netip.Addr) (netip.Addr, bool) {
	bytes := address.As16()
	switch {
	case address.Is4In6():
		return address.Unmap(), true
	case nat64.Contains(address):
		return netip.AddrFrom4([4]byte(bytes[12:16])), true
	case sixToFour.Contains(address):
		return netip.AddrFrom4([4]byte(bytes[2:6])), true
	default:
		return netip.Addr{}, false
	}
}
