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

func TestReviewFixes(t *testing.T) {
	t.Parallel()

	t.Run("DNS cache skips nonpositive TTL and caps names per IP", func(t *testing.T) {
		t.Parallel()
		c := newDNSCache()
		now := time.Now()
		c.now = func() time.Time { return now }
		ip := netip.MustParseAddr("1.2.3.4")

		c.record(ip, "zero.example", 0)
		if got := c.lookup(ip); len(got) != 0 {
			t.Fatalf("TTL 0 cached: %v", got)
		}

		c.record(ip, "one.example", time.Second)
		if got := c.lookup(ip); len(got) != 1 {
			t.Fatalf("TTL 1s not cached: %v", got)
		}
		now = now.Add(2 * time.Second)
		if got := c.lookup(ip); len(got) != 0 {
			t.Fatalf("TTL 1s retained past expiry: %v", got)
		}

		for i := range maxDNSCachePerIP + 10 {
			c.record(ip, "sub"+string(rune('a'+i%26))+string(rune('0'+i/26))+".example", time.Minute)
		}
		if got := c.lookup(ip); len(got) != maxDNSCachePerIP {
			t.Fatalf("names per IP = %d, want capped at %d", len(got), maxDNSCachePerIP)
		}
	})

	t.Run("CIDR allow rule does not authorize arbitrary DNS names", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"allow": [
				{"name": "db", "cidrs": ["10.20.5.10/32"], "proto": "tcp", "ports": ["5432"]},
				{"name": "web", "names": ["allowed.example.com"], "proto": "tcp", "ports": ["443"]}
			]
		}`), "cidr-dns")
		if err != nil {
			t.Fatal(err)
		}
		if blocked, _ := pol.blocked("exfil.evil.example.com"); !blocked {
			t.Fatal("CIDR rule allowed arbitrary DNS name")
		}
		if blocked, _ := pol.blocked("allowed.example.com"); blocked {
			t.Fatal("allowed hostname was blocked")
		}
	})

	t.Run("port-scoped deny does not block DNS when another port is allowed", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"deny": [
				{"name": "no-http", "names": ["example.com"], "proto": "tcp", "ports": ["80"]}
			],
			"allow": [
				{"name": "https", "names": ["example.com"], "proto": "tcp", "ports": ["443"]}
			]
		}`), "scoped-deny")
		if err != nil {
			t.Fatal(err)
		}
		if blocked, _ := pol.blocked("example.com"); blocked {
			t.Fatal("port-80 deny blocked DNS for example.com despite port-443 allow")
		}
	})

	t.Run("EDNS OPT root owner and HTTPS/SVCB stripping in responses", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"allow": [{"name": "web", "names": ["allowed.example.com"], "proto": "tcp", "ports": ["443"]}]
		}`), "edns")
		if err != nil {
			t.Fatal(err)
		}
		qname := dnsmessage.MustNewName("allowed.example.com.")
		msg := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: 1, Response: true},
			Questions: []dnsmessage.Question{{Name: qname, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
			Answers: []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{Name: qname, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte{93, 184, 216, 34}},
				},
				{
					Header: dnsmessage.ResourceHeader{Name: qname, Type: dnsTypeHTTPS, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.UnknownResource{Type: dnsTypeHTTPS, Data: []byte{0, 1, 0}},
				},
			},
			Additionals: []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("."), Type: dnsmessage.TypeOPT, Class: 4096},
					Body:   &dnsmessage.OPTResource{},
				},
			},
		}
		raw, err := msg.Pack()
		if err != nil {
			t.Fatal(err)
		}
		if blockedName, _, ok := blockedAnswer(pol, raw); !ok || blockedName != "" {
			t.Fatalf("blockedAnswer with EDNS OPT = %q, ok=%v; want allowed", blockedName, ok)
		}
		stripped := stripHTTPSSVCB(raw)
		var unpacked dnsmessage.Message
		if err := unpacked.Unpack(stripped); err != nil {
			t.Fatal(err)
		}
		if len(unpacked.Answers) != 1 || unpacked.Answers[0].Header.Type != dnsmessage.TypeA {
			t.Fatalf("HTTPS record not stripped: %+v", unpacked.Answers)
		}
	})

	t.Run("identifyForPolicy waits for complete HTTP headers", func(t *testing.T) {
		t.Parallel()
		partial := []byte("GET / HTTP/1.1\r\nHost: allowed.example")
		if info, more := identifyForPolicy(partial); info != nil || !more {
			t.Fatalf("identifyForPolicy(partial) = %v, more=%v; want nil, true", info, more)
		}
		complete := []byte("GET / HTTP/1.1\r\nHost: allowed.example.evil.com\r\n\r\n")
		if info, more := identifyForPolicy(complete); info == nil || more || info["host"] != "allowed.example.evil.com" {
			t.Fatalf("identifyForPolicy(complete) = %v, more=%v", info, more)
		}
	})

	t.Run("mapped IPv4 literal in policy rule", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"allow": [{"name": "mapped", "cidrs": ["::ffff:10.20.5.10"], "proto": "tcp", "ports": ["5432"]}]
		}`), "mapped")
		if err != nil {
			t.Fatal(err)
		}
		got := Decide(pol, Env{}, ProtoTCP, netip.MustParseAddrPort("10.20.5.10:5432"))
		if !got.Allow || got.Rule != "mapped" {
			t.Fatalf("Decide mapped = %+v, want allow rule=mapped", got)
		}
	})

	t.Run("SOA authority owner in parent zone is not blocked", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"allow": [{"name": "web", "names": ["allowed.example.com"], "proto": "tcp", "ports": ["443"]}]
		}`), "soa")
		if err != nil {
			t.Fatal(err)
		}
		qname := dnsmessage.MustNewName("allowed.example.com.")
		zone := dnsmessage.MustNewName("example.com.")
		msg := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: 1, Response: true},
			Questions: []dnsmessage.Question{{Name: qname, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET}},
			Authorities: []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{Name: zone, Type: dnsmessage.TypeSOA, Class: dnsmessage.ClassINET, TTL: 60},
					Body: &dnsmessage.SOAResource{
						NS:     dnsmessage.MustNewName("ns1.example.com."),
						MBox:   dnsmessage.MustNewName("hostmaster.example.com."),
						Serial: 1,
					},
				},
			},
		}
		raw, err := msg.Pack()
		if err != nil {
			t.Fatal(err)
		}
		if blockedName, _, ok := blockedAnswer(pol, raw); !ok || blockedName != "" {
			t.Fatalf("blockedAnswer with SOA authority = %q, ok=%v; want allowed", blockedName, ok)
		}
	})

	t.Run("CNAME chain bounds query-name TTL to minimum CNAME TTL", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"allow": [{"name": "web", "names": ["alias.example.com", "target.example.com"], "proto": "tcp", "ports": ["443"]}]
		}`), "cname-ttl")
		if err != nil {
			t.Fatal(err)
		}
		b, err := New(Config{
			Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
			Gateway: netip.MustParseAddr("192.168.127.1"),
			GuestIP: netip.MustParseAddr("192.168.127.2"),
		}, pol)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		b.names.now = func() time.Time { return now }

		alias := dnsmessage.MustNewName("alias.example.com.")
		target := dnsmessage.MustNewName("target.example.com.")
		q, _ := (&dnsmessage.Message{
			Questions: []dnsmessage.Question{{Name: alias, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
		}).Pack()
		resp, _ := (&dnsmessage.Message{
			Header:    dnsmessage.Header{Response: true},
			Questions: []dnsmessage.Question{{Name: alias, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
			Answers: []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{Name: alias, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 2},
					Body:   &dnsmessage.CNAMEResource{CNAME: target},
				},
				{
					Header: dnsmessage.ResourceHeader{Name: target, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 3600},
					Body:   &dnsmessage.AResource{A: [4]byte{93, 184, 216, 34}},
				},
			},
		}).Pack()
		b.logDNS("udp", q, resp, nil)
		ip := netip.MustParseAddr("93.184.216.34")
		now = now.Add(5 * time.Second)
		got := b.names.lookup(ip)
		if slices.Contains(got, "alias.example.com") {
			t.Fatalf("alias remained cached past CNAME TTL: %v", got)
		}
		if !slices.Contains(got, "target.example.com") {
			t.Fatalf("target should retain its own TTL: %v", got)
		}
	})

	t.Run("strict HTTP parser handles extension methods and conflicting authorities", func(t *testing.T) {
		t.Parallel()
		propfind := []byte("PROPFIND /dav HTTP/1.1\r\nHost: files.example.com:8080\r\n\r\n")
		info, more := identifyForPolicy(propfind)
		if more || info == nil {
			t.Fatalf("PROPFIND not identified: info=%v more=%v", info, more)
		}
		if name, bad := sniffedPeerNameStrict(info); bad || name != "files.example.com" {
			t.Fatalf("sniffedPeerNameStrict(PROPFIND) = %q, bad=%v", name, bad)
		}

		conflict := []byte("GET http://denied.example.com/path HTTP/1.1\r\nHost: allowed.example.com\r\n\r\n")
		info2, _ := identifyForPolicy(conflict)
		if _, bad := sniffedPeerNameStrict(info2); !bad {
			t.Fatalf("expected bad authority for conflicting absolute URI and Host header: %v", info2)
		}

		dupHost := []byte("GET / HTTP/1.1\r\nHost: allowed.example.com\r\nHost: denied.example.com\r\n\r\n")
		info3, _ := identifyForPolicy(dupHost)
		if _, bad := sniffedPeerNameStrict(info3); !bad {
			t.Fatalf("expected bad authority for duplicate Host headers: %v", info3)
		}
	})

	t.Run("CNAME chain bounds query-name TTL even when A precedes CNAME", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"allow": [{"name": "web", "names": ["alias.example.com", "target.example.com"], "proto": "tcp", "ports": ["443"]}]
		}`), "cname-ttl-reordered")
		if err != nil {
			t.Fatal(err)
		}
		b, err := New(Config{
			Subnet:  netip.MustParsePrefix("192.168.127.0/24"),
			Gateway: netip.MustParseAddr("192.168.127.1"),
			GuestIP: netip.MustParseAddr("192.168.127.2"),
		}, pol)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		b.names.now = func() time.Time { return now }

		alias := dnsmessage.MustNewName("alias.example.com.")
		target := dnsmessage.MustNewName("target.example.com.")
		q, _ := (&dnsmessage.Message{
			Questions: []dnsmessage.Question{{Name: alias, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
		}).Pack()
		resp, _ := (&dnsmessage.Message{
			Header:    dnsmessage.Header{Response: true},
			Questions: []dnsmessage.Question{{Name: alias, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
			Answers: []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{Name: target, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 3600},
					Body:   &dnsmessage.AResource{A: [4]byte{93, 184, 216, 34}},
				},
				{
					Header: dnsmessage.ResourceHeader{Name: alias, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 2},
					Body:   &dnsmessage.CNAMEResource{CNAME: target},
				},
			},
		}).Pack()
		b.logDNS("udp", q, resp, nil)
		ip := netip.MustParseAddr("93.184.216.34")
		now = now.Add(5 * time.Second)
		got := b.names.lookup(ip)
		if slices.Contains(got, "alias.example.com") {
			t.Fatalf("alias remained cached past CNAME TTL when A preceded CNAME: %v", got)
		}
	})

	t.Run("mapped IPv4 CIDR prefix normalized to IPv4", func(t *testing.T) {
		t.Parallel()
		pol, err := ParsePolicy([]byte(`{
			"allow": [{"name": "mapped-cidr", "cidrs": ["::ffff:10.20.5.0/120"], "proto": "tcp", "ports": ["5432"]}]
		}`), "mapped-cidr")
		if err != nil {
			t.Fatal(err)
		}
		got := Decide(pol, Env{}, ProtoTCP, netip.MustParseAddrPort("10.20.5.42:5432"))
		if !got.Allow || got.Rule != "mapped-cidr" {
			t.Fatalf("Decide mapped CIDR = %+v, want allow rule=mapped-cidr", got)
		}
	})

	t.Run("enforceConn blocks pipelined HTTP request with denied Host", func(t *testing.T) {
		t.Parallel()
		aborted := false
		ec := &enforceConn{
			check: func(name string, bad bool) bool {
				return !bad && name == "allowed.example.com"
			},
			abort: func() { aborted = true },
		}
		req1 := []byte("GET /one HTTP/1.1\r\nHost: allowed.example.com\r\n\r\n")
		if !ec.inspect(req1) || aborted {
			t.Fatal("first allowed HTTP request was rejected")
		}
		req2 := []byte("GET /two HTTP/1.1\r\nHost: denied.example.com\r\n\r\n")
		if ec.inspect(req2) {
			t.Fatal("second pipelined HTTP request with denied Host was allowed")
		}
	})

}
