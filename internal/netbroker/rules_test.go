package netbroker

import (
	"context"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

func TestDecideRules(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{
		"ipv6": true,
		"dns": "9.9.9.9",
		"deny": [
			{"name": "no-paste", "names": ["pastebin.com", "gist.github.com", "*.ngrok.io"], "proto": "tcp"}
		],
		"allow": [
			{"name": "db", "cidrs": ["10.20.5.10/32"], "proto": "tcp", "ports": ["5432"]},
			{"name": "dev-deps", "names": ["registry.npmjs.org", "*.githubusercontent.com"], "proto": "tcp"},
			{"name": "agent", "names": ["api.anthropic.com"], "proto": "tcp", "ports": ["443"]},
			{"name": "ntp", "names": ["time.example.com"], "proto": "udp", "ports": ["123"]}
		]
	}`), "rules")
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	wildcardPol, err := ParsePolicy([]byte(`{
		"allow": [
			{"name": "internet", "names": ["*"], "proto": "any"}
		]
	}`), "wildcard")
	if err != nil {
		t.Fatalf("ParsePolicy wildcard: %v", err)
	}
	dotQuicPol, err := ParsePolicy([]byte(`{
		"allow": [
			{"name": "dot", "names": ["*"], "proto": "tcp", "ports": ["853"]},
			{"name": "quic", "names": ["*"], "proto": "udp", "ports": ["443"]}
		]
	}`), "dot-quic")
	if err != nil {
		t.Fatalf("ParsePolicy dot-quic: %v", err)
	}

	baseEnv := Env{
		Gateway:   netip.MustParseAddr("192.168.127.1"),
		Subnet:    netip.MustParsePrefix("192.168.127.0/24"),
		HostAddrs: map[netip.Addr]bool{netip.MustParseAddr("10.20.5.99"): true},
	}

	tests := []struct {
		name   string
		policy *Policy
		names  []string
		proto  string
		dst    string
		allow  bool
		reason string
		rule   string
	}{
		{
			name: "default deny uncached public IP", policy: pol,
			proto: ProtoTCP, dst: "1.1.1.1:443", allow: false, reason: "default-deny",
		},
		{
			name: "allowed exact name", policy: pol, names: []string{"registry.npmjs.org"},
			proto: ProtoTCP, dst: "104.16.1.1:443", allow: true, reason: "rule dev-deps", rule: "dev-deps",
		},
		{
			name: "allowed wildcard name", policy: pol, names: []string{"objects.githubusercontent.com"},
			proto: ProtoTCP, dst: "185.199.108.133:443", allow: true, reason: "rule dev-deps", rule: "dev-deps",
		},
		{
			name: "deny wins over allow", policy: pol, names: []string{"gist.github.com", "objects.githubusercontent.com"},
			proto: ProtoTCP, dst: "140.82.121.4:443", allow: false, reason: "rule no-paste", rule: "no-paste",
		},
		{
			name: "denied wildcard", policy: pol, names: []string{"tunnel.eu.ngrok.io"},
			proto: ProtoTCP, dst: "3.120.0.1:443", allow: false, reason: "rule no-paste", rule: "no-paste",
		},
		{
			name: "agent port match", policy: pol, names: []string{"api.anthropic.com"},
			proto: ProtoTCP, dst: "160.79.104.10:443", allow: true, reason: "rule agent", rule: "agent",
		},
		{
			name: "agent wrong port denied", policy: pol, names: []string{"api.anthropic.com"},
			proto: ProtoTCP, dst: "160.79.104.10:80", allow: false, reason: "default-deny",
		},
		{
			name: "private cidr allowed without dns", policy: pol,
			proto: ProtoTCP, dst: "10.20.5.10:5432", allow: true, reason: "rule db", rule: "db",
		},
		{
			name: "private cidr wrong port", policy: pol,
			proto: ProtoTCP, dst: "10.20.5.10:5433", allow: false, reason: "denied-range",
		},
		{
			name: "name rule does not open private ip", policy: pol, names: []string{"registry.npmjs.org"},
			proto: ProtoTCP, dst: "10.20.5.11:443", allow: false, reason: "denied-range",
		},
		{
			name: "udp allowed by rule", policy: pol, names: []string{"time.example.com"},
			proto: ProtoUDP, dst: "1.2.3.4:123", allow: true, reason: "rule ntp", rule: "ntp",
		},
		{
			name: "direct dns port 53 denied in rules mode", policy: wildcardPol,
			proto: ProtoUDP, dst: "1.1.1.1:53", allow: false, reason: "dns-filtered",
		},
		{
			name: "wildcard allows public https", policy: wildcardPol,
			proto: ProtoTCP, dst: "1.1.1.1:443", allow: true, reason: "rule internet", rule: "internet",
		},
		{
			name: "wildcard does not open private ip", policy: wildcardPol,
			proto: ProtoTCP, dst: "10.0.0.1:443", allow: false, reason: "denied-range",
		},
		{
			name: "wildcard excludes DoT port 853", policy: wildcardPol,
			proto: ProtoTCP, dst: "1.1.1.1:853", allow: false, reason: "default-deny",
		},
		{
			name: "wildcard excludes QUIC udp 443", policy: wildcardPol,
			proto: ProtoUDP, dst: "1.1.1.1:443", allow: false, reason: "default-deny",
		},
		{
			name: "explicit port 853 allowed", policy: dotQuicPol,
			proto: ProtoTCP, dst: "1.1.1.1:853", allow: true, reason: "rule dot", rule: "dot",
		},
		{
			name: "explicit udp 443 allowed", policy: dotQuicPol,
			proto: ProtoUDP, dst: "1.1.1.1:443", allow: true, reason: "rule quic", rule: "quic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := baseEnv
			env.Names = tt.names
			got := Decide(tt.policy, env, tt.proto, netip.MustParseAddrPort(tt.dst))
			if got.Allow != tt.allow || got.Reason != tt.reason || got.Rule != tt.rule {
				t.Fatalf("Decide(%s %s, names=%v) = %+v, want allow=%v reason=%q rule=%q",
					tt.proto, tt.dst, tt.names, got, tt.allow, tt.reason, tt.rule)
			}
		})
	}
}

func TestDNSCacheAndNameEnforcement(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })

	// Upstream resolves allowed.example.com and shared-denied.example.com to 93.184.216.34.
	sharedIP := [4]byte{93, 184, 216, 34}
	go func() {
		buf := make([]byte, 512)
		for {
			n, peer, err := upstream.ReadFrom(buf)
			if err != nil {
				return
			}
			var req dnsmessage.Message
			if err := req.Unpack(buf[:n]); err != nil || len(req.Questions) == 0 {
				continue
			}
			q := req.Questions[0]
			req.Response = true
			switch q.Type {
			case dnsmessage.TypeA:
				req.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 120},
					Body:   &dnsmessage.AResource{A: sharedIP},
				}}
			case dnsTypeHTTPS:
				req.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsTypeHTTPS, Class: dnsmessage.ClassINET, TTL: 120},
					Body:   &dnsmessage.UnknownResource{Type: dnsTypeHTTPS, Data: []byte{0, 1, 0}},
				}}
			}
			if resp, err := req.Pack(); err == nil {
				_, _ = upstream.WriteTo(resp, peer)
			}
		}
	}()

	policy, err := ParsePolicy([]byte(`{
		"dns": "`+upstream.LocalAddr().String()+`",
		"deny": [
			{"name": "no-paste", "names": ["pastebin.com", "*.evil.example.com"], "proto": "tcp"}
		],
		"allow": [
			{"name": "web", "names": ["allowed.example.com", "other.example.com"], "proto": "tcp", "ports": ["443"]}
		]
	}`), "test-rules")
	if err != nil {
		t.Fatal(err)
	}

	log := &syncBuf{}
	broker, err := New(Config{
		Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
		Gateway: netip.MustParseAddr("192.168.127.1"),
		GuestIP: netip.MustParseAddr("192.168.127.2"),
		Log:     log,
	}, policy)
	if err != nil {
		t.Fatal(err)
	}

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echoListener.Close() })
	receivedHello := make(chan []byte, 4)
	go func() {
		for {
			c, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				b := make([]byte, 2048)
				n, _ := c.Read(b)
				if n > 0 {
					receivedHello <- slices.Clone(b[:n])
					_, _ = c.Write([]byte("upstream-ok"))
				}
			}()
		}
	}()
	broker.dialTCP = func(network, _ string) (net.Conn, error) {
		return net.DialTimeout(network, echoListener.Addr().String(), 2*time.Second)
	}

	guest := connectTestBroker(t, broker)

	queryDNS := func(qname string, qtype dnsmessage.Type) dnsmessage.Message {
		t.Helper()
		c, err := gonet.DialUDP(guest, nil, guestAddr(broker.cfg.Gateway, 53), ipv4.ProtocolNumber)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		req := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: 1, RecursionDesired: true},
			Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName(qname), Type: qtype, Class: dnsmessage.ClassINET}},
		}
		raw, err := req.Pack()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write(raw); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 512)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		var resp dnsmessage.Message
		if err := resp.Unpack(buf[:n]); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 1. Hard-coded IP before any DNS resolution is denied at L4.
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	c, err := gonet.DialContextTCP(dctx, guest, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(sharedIP), Port: 443}, ipv4.ProtocolNumber)
	cancel()
	if c != nil {
		_ = c.Close()
	}
	if err == nil {
		t.Fatal("hard-coded IP without DNS lookup succeeded, want RST")
	}

	// 2. Denied domain gets NXDOMAIN, unallowed domain gets NXDOMAIN.
	if resp := queryDNS("pastebin.com.", dnsmessage.TypeA); resp.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("denied domain rcode = %v, want NXDOMAIN", resp.RCode)
	}
	if resp := queryDNS("unlisted.example.com.", dnsmessage.TypeA); resp.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("unlisted domain rcode = %v, want NXDOMAIN", resp.RCode)
	}

	// 3. HTTPS record is stripped (NODATA: RCodeSuccess with 0 answers).
	if resp := queryDNS("allowed.example.com.", dnsTypeHTTPS); resp.RCode != dnsmessage.RCodeSuccess || len(resp.Answers) != 0 {
		t.Fatalf("HTTPS query = rcode %v answers %v, want NODATA", resp.RCode, resp.Answers)
	}

	// 4. Allowed domain resolves and populates the IP->name cache.
	if resp := queryDNS("allowed.example.com.", dnsmessage.TypeA); resp.RCode != dnsmessage.RCodeSuccess || len(resp.Answers) != 1 {
		t.Fatalf("allowed domain response = %+v", resp)
	}

	// 5. Connect to resolved IP with matching SNI -> allowed and spliced to upstream.
	helloOK := clientHelloBytes(t, "allowed.example.com", []string{"h2"})
	dctx, cancel = context.WithTimeout(ctx, 5*time.Second)
	c, err = gonet.DialContextTCP(dctx, guest, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(sharedIP), Port: 443}, ipv4.ProtocolNumber)
	cancel()
	if err != nil {
		t.Fatalf("dial resolved IP: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(helloOK); err != nil {
		t.Fatalf("write helloOK: %v", err)
	}
	reply, err := io.ReadAll(c)
	_ = c.Close()
	if err != nil || string(reply) != "upstream-ok" {
		t.Fatalf("allowed SNI reply = %q, %v", reply, err)
	}
	select {
	case gotHello := <-receivedHello:
		if !slices.Equal(gotHello, helloOK) {
			t.Fatalf("upstream did not receive full replayed ClientHello (%d vs %d bytes)", len(gotHello), len(helloOK))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive ClientHello")
	}

	// 6. Connect to the SAME resolved IP (shared CDN IP) with a denied or mismatched SNI -> aborted before dialing upstream.
	for _, badSNI := range []string{"pastebin.com", "other.example.com", "other-tenant.cdn.example.com"} {
		helloBad := clientHelloBytes(t, badSNI, nil)
		dctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		c, err = gonet.DialContextTCP(dctx, guest, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(sharedIP), Port: 443}, ipv4.ProtocolNumber)
		cancel()
		if err != nil {
			t.Fatalf("dial for %s: %v", badSNI, err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Write(helloBad)
		got, readErr := io.ReadAll(c)
		_ = c.Close()
		if len(got) > 0 && readErr == nil {
			t.Fatalf("SNI %s received upstream data %q, want connection aborted", badSNI, got)
		}
		select {
		case <-receivedHello:
			t.Fatalf("upstream was dialed for disallowed SNI %s", badSNI)
		default:
		}
	}

	logText := log.String()
	for _, want := range []string{
		`"name":"allowed.example.com"`,
		`"rule":"web"`,
		`"rule":"no-paste"`,
		`"reason":"sni-mismatch"`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log missing %s:\n%s", want, logText)
		}
	}
}
