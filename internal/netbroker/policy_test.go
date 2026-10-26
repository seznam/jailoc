package netbroker

import (
	"net/netip"
	"testing"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	p, err := ParsePolicy([]byte(`{"exceptions":[
		{"cidr":"10.99.0.20/32","proto":"tcp","ports":["47801"]},
		{"cidr":"192.168.50.0/24","proto":"any"},
		{"cidr":"127.0.0.1/32","proto":"any"},
		{"cidr":"10.99.0.73/32","proto":"any"}]}`), "test")
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	env := Env{
		Gateway:   netip.MustParseAddr("192.168.127.1"),
		Subnet:    netip.MustParsePrefix("192.168.127.0/24"),
		HostAddrs: map[netip.Addr]bool{netip.MustParseAddr("10.99.0.73"): true, netip.MustParseAddr("2001:db8::1"): true},
	}
	tests := []struct {
		name   string
		policy *Policy
		proto  string
		dst    string
		allow  bool
		reason string
	}{
		{"public tcp", p, ProtoTCP, "1.1.1.1:443", true, "public"},
		{"public udp high port", p, ProtoUDP, "8.8.8.8:40000", true, "public"},
		{"rfc1918 denied", p, ProtoTCP, "10.99.1.53:53", false, "denied-range"},
		{"exception port", p, ProtoTCP, "10.99.0.20:47801", true, "exception 10.99.0.20/32"},
		{"exception other port", p, ProtoTCP, "10.99.0.20:47802", false, "denied-range"},
		{"exception other proto", p, ProtoUDP, "10.99.0.20:47801", false, "denied-range"},
		{"exception any", p, ProtoUDP, "192.168.50.9:1", true, "exception 192.168.50.0/24"},
		{"loopback not openable", p, ProtoTCP, "127.0.0.1:22", false, "hard-deny"},
		{"host address not openable", p, ProtoTCP, "10.99.0.73:22", false, "host-address"},
		{"metadata", p, ProtoTCP, "169.254.169.254:80", false, "hard-deny"},
		{"gateway", p, ProtoTCP, "192.168.127.1:22", false, "gateway"},
		{"guest network", p, ProtoTCP, "192.168.127.7:22", false, "guest-network"},
		{"ipv6", p, ProtoTCP, "[2606:4700:4700::1111]:443", false, "ipv6"},
		{"mapped v4 evaluated as v4", p, ProtoTCP, "[::ffff:10.1.2.3]:80", false, "denied-range"},
		{"cgnat", p, ProtoTCP, "100.100.1.1:80", false, "denied-range"},
		{"broadcast", p, ProtoUDP, "255.255.255.255:67", false, "hard-deny"},
		{"fail closed", DenyAll("bad reload"), ProtoTCP, "1.1.1.1:443", false, "fail-closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tt.policy, env, tt.proto, netip.MustParseAddrPort(tt.dst))
			if got.Allow != tt.allow || got.Reason != tt.reason {
				t.Fatalf("Decide(%s %s) = %+v, want allow=%v reason=%q", tt.proto, tt.dst, got, tt.allow, tt.reason)
			}
		})
	}
}

func TestParsePolicyRejects(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"unknown field": `{"exceptoins":[]}`,
		"bad cidr":      `{"exceptions":[{"cidr":"10.0.0.0/33","proto":"tcp"}]}`,
		"ipv6 cidr":     `{"exceptions":[{"cidr":"fd00::/8","proto":"tcp"}]}`,
		"bad proto":     `{"exceptions":[{"cidr":"10.0.0.0/8","proto":"icmp"}]}`,
		"bad port":      `{"exceptions":[{"cidr":"10.0.0.0/8","proto":"tcp","ports":["0"]}]}`,
		"reversed":      `{"exceptions":[{"cidr":"10.0.0.0/8","proto":"tcp","ports":["90-80"]}]}`,
		"bad upstream":  `{"dns_upstreams":["resolver.example"]}`,
		"not json":      `exceptions: []`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParsePolicy([]byte(in), name); err == nil {
				t.Fatalf("ParsePolicy(%s) accepted invalid policy", in)
			}
		})
	}
}
