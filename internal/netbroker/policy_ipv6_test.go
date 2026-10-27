package netbroker

import (
	"net/netip"
	"testing"
)

func TestDecideIPv6(t *testing.T) {
	t.Parallel()
	policy, err := ParsePolicy([]byte(`{"exceptions":[
		{"cidr":"fd42::/48","proto":"tcp","ports":["443"]},
		{"cidr":"10.42.0.0/16","proto":"any"}]}`), "ipv6")
	if err != nil {
		t.Fatal(err)
	}
	env := Env{
		Gateway:   netip.MustParseAddr("192.168.127.1"),
		Subnet:    netip.MustParsePrefix("192.168.127.0/24"),
		HostAddrs: map[netip.Addr]bool{netip.MustParseAddr("2606:4700::99"): true},
	}
	for _, test := range []struct {
		name, address, proto, reason string
		allow                        bool
	}{
		{"public TCP", "2606:4700:4700::1111", ProtoTCP, "public", true},
		{"public UDP", "2001:4860:4860::8888", ProtoUDP, "public", true},
		{"unspecified", "::", ProtoTCP, "ipv6-unspecified", false},
		{"loopback", "::1", ProtoTCP, "ipv6-loopback", false},
		{"compatible", "::8.8.8.8", ProtoTCP, "ipv4-compatible", false},
		{"mapped public", "::ffff:8.8.8.8", ProtoTCP, "public", true},
		{"mapped private", "::ffff:10.1.2.3", ProtoTCP, "denied-range", false},
		{"mapped exception", "::ffff:10.42.1.2", ProtoTCP, "exception 10.42.0.0/16", true},
		{"NAT64 public", "64:ff9b::808:808", ProtoTCP, "public", true},
		{"NAT64 private", "64:ff9b::a01:203", ProtoTCP, "denied-range", false},
		{"NAT64 exception", "64:ff9b::a2a:102", ProtoTCP, "exception 10.42.0.0/16", true},
		{"NAT64 metadata", "64:ff9b::a9fe:a9fe", ProtoTCP, "hard-deny", false},
		{"NAT64 local prefix", "64:ff9b:1::808:808", ProtoTCP, "nat64-local", false},
		{"6to4 public", "2002:808:808::1", ProtoTCP, "public", true},
		{"6to4 private", "2002:a01:203::1", ProtoTCP, "denied-range", false},
		{"6to4 exception", "2002:a2a:102::1", ProtoTCP, "exception 10.42.0.0/16", true},
		{"6to4 gateway", "2002:c0a8:7f01::1", ProtoTCP, "gateway", false},
		{"Teredo", "2001::1", ProtoTCP, "teredo", false},
		{"link local", "fe80::2", ProtoTCP, "ipv6-link-local", false},
		{"site local", "fec0::2", ProtoTCP, "ipv6-site-local", false},
		{"multicast", "ff02::1", ProtoUDP, "ipv6-multicast", false},
		{"documentation", "2001:db8::1", ProtoTCP, "ipv6-documentation", false},
		{"documentation new", "3fff::1", ProtoTCP, "ipv6-documentation", false},
		{"discard", "100::1", ProtoTCP, "ipv6-discard", false},
		{"ORCHIDv2", "2001:20::1", ProtoTCP, "orchidv2", false},
		{"benchmark", "2001:2::1", ProtoTCP, "ipv6-benchmark", false},
		{"ULA", "fd43::1", ProtoTCP, "ipv6-private", false},
		{"ULA exception", "fd42::1", ProtoTCP, "exception fd42::/48", true},
		{"ULA wrong protocol", "fd42::1", ProtoUDP, "ipv6-private", false},
		{"gateway", "fd00:127::1", ProtoTCP, "gateway", false},
		{"gateway link local", "fe80::1", ProtoTCP, "gateway", false},
		{"guest subnet", "fd00:127::99", ProtoTCP, "guest-network", false},
		{"host address", "2606:4700::99", ProtoTCP, "host-address", false},
		{"zoned host address", "2606:4700::99%en0", ProtoTCP, "host-address", false},
		{"AWS metadata", "fd00:ec2::254", ProtoTCP, "ipv6-metadata", false},
		{"EKS metadata", "fd00:ec2::23", ProtoTCP, "ipv6-metadata", false},
		{"ORCHID", "2001:10::1", ProtoTCP, "orchid", false},
		{"unallocated", "4000::1", ProtoTCP, "ipv6-reserved", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dst := netip.AddrPortFrom(netip.MustParseAddr(test.address), 443)
			got := Decide(policy, env, test.proto, dst)
			if got.Allow != test.allow || got.Reason != test.reason {
				t.Fatalf("Decide(%s) = %+v, want allow=%v reason=%s", dst, got, test.allow, test.reason)
			}
		})
	}
}

func TestIPv6AdditionalBounds(t *testing.T) {
	t.Parallel()
	policy, err := ParsePolicy([]byte(`{"exceptions":[{"cidr":"::/0","proto":"any"}]}`), "broad")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		policy *Policy
		dst    string
		reason string
	}{
		{"metadata exception", policy, "[fd00:ec2::23]:80", "ipv6-metadata"},
		{"fail closed", DenyAll("reload"), "[2606:4700:4700::1111]:443", "fail-closed"},
		{"embedded host", policy, "[64:ff9b::a2a:102]:443", "host-address"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			env := Env{HostAddrs: map[netip.Addr]bool{netip.MustParseAddr("10.42.1.2"): true}}
			if got := Decide(test.policy, env, ProtoTCP, netip.MustParseAddrPort(test.dst)); got.Allow || got.Reason != test.reason {
				t.Fatalf("Decide = %+v, want %s", got, test.reason)
			}
		})
	}
}

func TestIPv6ExceptionsCannotOpenProtectedAddresses(t *testing.T) {
	t.Parallel()
	policy, err := ParsePolicy([]byte(`{"exceptions":[{"cidr":"::/0","proto":"any"}]}`), "broad")
	if err != nil {
		t.Fatal(err)
	}
	env := Env{HostAddrs: map[netip.Addr]bool{netip.MustParseAddr("2606:4700::99"): true}}
	for _, address := range []string{"::", "::1", "fe80::2", "fec0::2", "ff02::1", "fd00:ec2::254", "fd00:127::1", "fd00:127::2", "2606:4700::99", "2001:db8::1", "3fff::1", "100::1", "2001:20::1", "2001:2::1", "2001::1", "64:ff9b:1::1", "::127.0.0.1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::1"} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			if got := Decide(policy, env, ProtoTCP, netip.AddrPortFrom(netip.MustParseAddr(address), 443)); got.Allow {
				t.Fatalf("broad exception opened %s: %+v", address, got)
			}
		})
	}
}

func TestIPv6FilteredDNS(t *testing.T) {
	t.Parallel()
	policy, err := ParsePolicy([]byte(`{"dns_upstreams":["1.1.1.1"],"dns_blocked_zones":["internal"],"exceptions":[{"cidr":"::/0","proto":"any"}]}`), "filtered")
	if err != nil {
		t.Fatal(err)
	}
	for _, proto := range []string{ProtoTCP, ProtoUDP} {
		t.Run(proto, func(t *testing.T) {
			t.Parallel()
			got := Decide(policy, Env{}, proto, netip.MustParseAddrPort("[2606:4700:4700::1111]:53"))
			if got.Allow || got.Reason != "dns-filtered" {
				t.Fatalf("filtered DNS = %+v", got)
			}
		})
	}
}
