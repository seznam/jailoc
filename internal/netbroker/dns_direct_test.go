package netbroker

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestDecideDirectDNS(t *testing.T) {
	t.Parallel()
	parse := func(t *testing.T, policy string) *Policy {
		t.Helper()
		p, err := ParsePolicy([]byte(policy), "direct-dns")
		if err != nil {
			t.Fatalf("ParsePolicy: %v", err)
		}
		return p
	}
	byCIDR := parse(t, `{"ipv6":true,"allow":[
		{"name":"internet","names":["*"],"proto":"any"},
		{"name":"resolver","cidrs":["10.33.4.25","fd00:53::/64"],"proto":"any","ports":["53"]}]}`)
	byName := parse(t, `{"allow":[{"name":"coredns","names":["*.lb.scif.cz"],"proto":"udp","ports":["53"]}]}`)
	public := parse(t, `{"ipv6":true,"allow":[{"name":"public-dns","names":["*"],"proto":"any","ports":["53"]}]}`)
	denied := parse(t, `{"allow":[{"name":"public-dns","names":["*"],"proto":"any","ports":["53"]}],
		"deny":[{"name":"no-google","cidrs":["8.8.8.8"],"proto":"any"}]}`)
	zones := parse(t, `{"dns_upstreams":["10.99.1.53"],"dns_blocked_zones":["internal"],"exceptions":[{"cidr":"10.33.4.25/32","proto":"any"}]}`)
	env := Env{Gateway: netip.MustParseAddr("192.168.127.1"), Subnet: netip.MustParsePrefix("192.168.127.0/24")}
	lbEnv := env
	lbEnv.Names = []string{"coredns-lb.ko2.os.lb.scif.cz"}
	tests := []struct {
		name   string
		policy *Policy
		env    Env
		proto  string
		dst    string
		allow  bool
		reason string
	}{
		{"cidr rule with port 53", byCIDR, env, ProtoUDP, "10.33.4.25:53", true, "rule resolver"},
		{"cidr rule over tcp", byCIDR, env, ProtoTCP, "10.33.4.25:53", true, "rule resolver"},
		{"other private resolver", byCIDR, env, ProtoUDP, "10.33.4.26:53", false, "dns-filtered"},
		{"wildcard without ports", byCIDR, env, ProtoUDP, "8.8.8.8:53", false, "dns-filtered"},
		{"ipv6 cidr rule", byCIDR, env, ProtoUDP, "[fd00:53::1]:53", true, "rule resolver"},
		{"ipv6 wildcard without ports", byCIDR, env, ProtoUDP, "[2606:4700:4700::1111]:53", false, "dns-filtered"},
		{"name rule", byName, lbEnv, ProtoUDP, "10.33.4.25:53", true, "rule coredns"},
		{"name rule other proto", byName, lbEnv, ProtoTCP, "10.33.4.25:53", false, "dns-filtered"},
		{"name rule unresolved", byName, env, ProtoUDP, "10.33.4.25:53", false, "dns-filtered"},
		{"public wildcard with port 53", public, env, ProtoUDP, "8.8.8.8:53", true, "rule public-dns"},
		{"public wildcard ipv6", public, env, ProtoTCP, "[2606:4700:4700::1111]:53", true, "rule public-dns"},
		{"public wildcard private resolver", public, env, ProtoUDP, "10.33.4.25:53", false, "dns-filtered"},
		{"public wildcard loopback", public, env, ProtoUDP, "127.0.0.1:53", false, "hard-deny"},
		{"deny wins", denied, env, ProtoUDP, "8.8.8.8:53", false, "rule no-google"},
		{"deny other resolver", denied, env, ProtoUDP, "1.1.1.1:53", true, "rule public-dns"},
		{"blocked zones ignore exceptions", zones, env, ProtoUDP, "10.33.4.25:53", false, "dns-filtered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tt.policy, tt.env, tt.proto, netip.MustParseAddrPort(tt.dst))
			if got.Allow != tt.allow || got.Reason != tt.reason {
				t.Fatalf("Decide(%s %s) = %+v, want allow=%v reason=%q", tt.proto, tt.dst, got, tt.allow, tt.reason)
			}
		})
	}
}

// TestBrokerDirectDNS checks that the broker answers DNS the guest sends to an
// allowed resolver itself: the query goes to that resolver, it is filtered
// like the gateway resolver, and its answers stay out of the IP-to-name cache.
func TestBrokerDirectDNS(t *testing.T) {
	t.Parallel()
	server := netip.MustParseAddrPort("10.33.4.25:53")
	answer := netip.MustParseAddr("93.184.216.34")
	tests := []struct {
		name     string
		network  string
		query    string
		rcode    dnsmessage.RCode
		upstream bool
	}{
		{"udp allowed name", "udp", "app.example.com.", dnsmessage.RCodeSuccess, true},
		{"tcp allowed name", "tcp", "app.example.com.", dnsmessage.RCodeSuccess, true},
		{"udp unallowed name", "udp", "db.corp.test.", dnsmessage.RCodeNameError, false},
		{"tcp unallowed name", "tcp", "db.corp.test.", dnsmessage.RCodeNameError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy, err := ParsePolicy([]byte(`{"allow":[
				{"name":"resolver","cidrs":["10.33.4.25"],"proto":"any","ports":["53"]},
				{"name":"web","names":["*.example.com"],"proto":"tcp","ports":["443"]}]}`), "direct-dns")
			if err != nil {
				t.Fatal(err)
			}
			log := &syncBuf{}
			broker, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), Log: log}, policy)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var asked []string
			broker.exchange = func(_ context.Context, network string, up netip.AddrPort, q []byte) ([]byte, error) {
				mu.Lock()
				asked = append(asked, network+" "+up.String())
				mu.Unlock()
				var msg dnsmessage.Message
				if err := msg.Unpack(q); err != nil {
					return nil, err
				}
				msg.Response = true
				msg.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: msg.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: answer.As4()},
				}}
				return msg.Pack()
			}
			guest := connectTestBroker(t, broker)
			request := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: 42, RecursionDesired: true},
				Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName(tt.query), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
			}
			query, err := request.Pack()
			if err != nil {
				t.Fatal(err)
			}
			response := directDNSQuery(t, guest, tt.network, server, query)
			if response.RCode != tt.rcode {
				t.Fatalf("rcode = %v, want %v", response.RCode, tt.rcode)
			}
			if got := len(response.Answers) > 0; got != tt.upstream {
				t.Fatalf("answers = %+v, want answered %v", response.Answers, tt.upstream)
			}
			mu.Lock()
			gotAsked := strings.Join(asked, ",")
			mu.Unlock()
			wantAsked := ""
			if tt.upstream {
				wantAsked = tt.network + " " + server.String()
			}
			if gotAsked != wantAsked {
				t.Fatalf("upstream queries = %q, want %q", gotAsked, wantAsked)
			}
			if names := broker.names.lookup(answer); len(names) != 0 {
				t.Fatalf("IP-to-name cache for %s = %v, want empty", answer, names)
			}
			if !strings.Contains(log.String(), `"server":"10.33.4.25:53"`) {
				t.Fatalf("log has no dns event for server %s:\n%s", server, log.String())
			}
		})
	}
}

// directDNSQuery sends query from the guest to server over network and
// returns the parsed answer.
func directDNSQuery(t *testing.T, guest *stack.Stack, network string, server netip.AddrPort, query []byte) dnsmessage.Message {
	t.Helper()
	addr := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(server.Addr().As4()), Port: server.Port()}
	var conn net.Conn
	var err error
	if network == "udp" {
		conn, err = gonet.DialUDP(guest, nil, &addr, ipv4.ProtocolNumber)
	} else {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		conn, err = gonet.DialContextTCP(ctx, guest, addr, ipv4.ProtocolNumber)
	}
	if err != nil {
		t.Fatalf("dial %s %s: %v", network, server, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var raw []byte
	if network == "udp" {
		if _, err := conn.Write(query); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 512)
		count, err := conn.Read(buffer)
		if err != nil {
			t.Fatal(err)
		}
		raw = buffer[:count]
	} else {
		if _, err := conn.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(query))), query...)); err != nil { //nolint:gosec // G115: test query is small
			t.Fatal(err)
		}
		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			t.Fatal(err)
		}
		raw = make([]byte, binary.BigEndian.Uint16(length[:]))
		if _, err := io.ReadFull(conn, raw); err != nil {
			t.Fatal(err)
		}
	}
	var response dnsmessage.Message
	if err := response.Unpack(raw); err != nil {
		t.Fatal(err)
	}
	return response
}
