package vmprobe

import (
	"net/netip"
	"testing"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr   string
		public bool
		rng    string
	}{
		{"1.1.1.1", true, ""},
		{"8.8.8.8", true, ""},
		{"10.1.2.3", false, "rfc1918"},
		{"100.100.100.100", false, "cgnat"},
		{"127.0.0.1", false, "loopback"},
		{"169.254.169.254", false, "link-local"},
		{"172.31.255.255", false, "rfc1918"},
		{"172.32.0.1", true, ""},
		{"192.168.1.1", false, "rfc1918"},
		{"198.19.0.1", false, "benchmarking"},
		{"224.0.0.251", false, "multicast"},
		{"255.255.255.255", false, "reserved-and-broadcast"},
		{"0.200.200.200", false, "this-network"},
		{"2606:4700:4700::1111", true, ""},
		{"::1", false, "loopback"},
		{"::", false, "unspecified"},
		{"fd00:ec2::254", false, "unique-local"},
		{"fe80::1", false, "link-local"},
		{"fe80::1%eth0", false, "link-local"},
		{"ff02::1", false, "multicast"},
		{"2001::1", false, "ietf-protocol-assignments-incl-teredo"},
		{"2001:db8::1", false, "documentation"},
		{"2002:c0a8:101::1", false, "6to4"},
		{"::ffff:10.0.0.1", false, "ipv4-mapped/rfc1918"},
		{"::ffff:1.1.1.1", true, ""},
		{"64:ff9b::a00:1", false, "nat64/rfc1918"},
		{"64:ff9b::101:101", true, ""},
		{"64:ff9b:1::1", false, "local-nat64"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()
			got := Classify(netip.MustParseAddr(tt.addr))
			if got.Public != tt.public || got.Range != tt.rng {
				t.Fatalf("Classify(%s) = %+v, want public=%v range=%q", tt.addr, got, tt.public, tt.rng)
			}
		})
	}
}

func TestExceptionMatches(t *testing.T) {
	t.Parallel()
	e := Exception{CIDR: "192.168.1.50/32", Proto: "tcp", Ports: []int{5432}}
	a := netip.MustParseAddr("192.168.1.50")
	tests := []struct {
		name  string
		addr  netip.Addr
		proto string
		port  int
		want  bool
	}{
		{"exact", a, "tcp", 5432, true},
		{"other port", a, "tcp", 22, false},
		{"other proto", a, "udp", 5432, false},
		{"other addr", netip.MustParseAddr("192.168.1.51"), "tcp", 5432, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := e.Matches(tt.addr, tt.proto, tt.port); got != tt.want {
				t.Fatalf("Matches = %v, want %v", got, tt.want)
			}
		})
	}
	all := Exception{CIDR: "10.0.0.0/8", Proto: "any"}
	if !all.Matches(netip.MustParseAddr("10.9.9.9"), "udp", 53) {
		t.Fatal("any-proto all-ports exception should match")
	}
}

func TestExceptionValidate(t *testing.T) {
	t.Parallel()
	bad := []Exception{
		{CIDR: "nope", Proto: "tcp"},
		{CIDR: "10.0.0.0/8", Proto: "icmp"},
		{CIDR: "10.0.0.0/8", Proto: "tcp", Ports: []int{0}},
	}
	for _, e := range bad {
		if e.Validate() == nil {
			t.Errorf("Validate(%+v) = nil, want error", e)
		}
	}
	if err := (Exception{CIDR: "10.0.0.0/8", Proto: "tcp", Ports: []int{22}}).Validate(); err != nil {
		t.Fatalf("Validate valid: %v", err)
	}
}
