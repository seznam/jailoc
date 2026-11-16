package netbroker

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// testRecord is one answer record: an address or, with cname set, a CNAME.
type testRecord struct {
	owner string
	addr  string
	cname string
}

func packAnswer(t *testing.T, qname string, records []testRecord) []byte {
	t.Helper()
	q := dnsmessage.MustNewName(qname)
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 7, Response: true},
		Questions: []dnsmessage.Question{{Name: q, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	for _, r := range records {
		h := dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(r.owner), Class: dnsmessage.ClassINET, TTL: 60}
		switch {
		case r.cname != "":
			h.Type = dnsmessage.TypeCNAME
			msg.Answers = append(msg.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(r.cname)}})
		case netip.MustParseAddr(r.addr).Is4():
			h.Type = dnsmessage.TypeA
			msg.Answers = append(msg.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AResource{A: netip.MustParseAddr(r.addr).As4()}})
		default:
			h.Type = dnsmessage.TypeAAAA
			msg.Answers = append(msg.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr(r.addr).As16()}})
		}
	}
	b, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func answerAddrs(t *testing.T, resp []byte) (addrs []string, cnames int) {
	t.Helper()
	var msg dnsmessage.Message
	if err := msg.Unpack(resp); err != nil {
		t.Fatalf("unpack filtered answer: %v", err)
	}
	for _, r := range msg.Answers {
		switch body := r.Body.(type) {
		case *dnsmessage.AResource:
			addrs = append(addrs, netip.AddrFrom4(body.A).String())
		case *dnsmessage.AAAAResource:
			addrs = append(addrs, netip.AddrFrom16(body.AAAA).String())
		case *dnsmessage.CNAMEResource:
			cnames++
		}
	}
	return addrs, cnames
}

func TestDropPrivateAnswers(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{
		"internal_zones": ["internal.example.com"],
		"allow": [{"name": "all", "names": ["*"], "proto": "tcp"}]
	}`), "internal-zones")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		qname   string
		records []testRecord
		kept    []string
		dropped []string
	}{
		{
			name: "internal name private address", qname: "gitlab.internal.example.com.",
			records: []testRecord{{owner: "gitlab.internal.example.com.", addr: "10.20.5.10"}},
			kept:    []string{"10.20.5.10"},
		},
		{
			name: "internal zone apex", qname: "internal.example.com.",
			records: []testRecord{{owner: "internal.example.com.", addr: "192.168.1.10"}},
			kept:    []string{"192.168.1.10"},
		},
		{
			name: "public name private address", qname: "10-0-0-5.nip.io.",
			records: []testRecord{{owner: "10-0-0-5.nip.io.", addr: "10.0.0.5"}},
			dropped: []string{"10.0.0.5"},
		},
		{
			name: "lookalike zone", qname: "gitlab.notinternal.example.com.",
			records: []testRecord{{owner: "gitlab.notinternal.example.com.", addr: "10.0.0.5"}},
			dropped: []string{"10.0.0.5"},
		},
		{
			name: "public name public address", qname: "github.com.",
			records: []testRecord{{owner: "github.com.", addr: "140.82.121.4"}},
			kept:    []string{"140.82.121.4"},
		},
		{
			name: "mixed answer keeps public addresses", qname: "app.example.net.",
			records: []testRecord{{owner: "app.example.net.", addr: "1.2.3.4"}, {owner: "app.example.net.", addr: "172.16.0.9"}},
			kept:    []string{"1.2.3.4"}, dropped: []string{"172.16.0.9"},
		},
		{
			name: "public name cnamed into internal zone", qname: "x.nip.io.",
			records: []testRecord{{owner: "x.nip.io.", cname: "db.internal.example.com."}, {owner: "db.internal.example.com.", addr: "10.20.5.11"}},
			dropped: []string{"10.20.5.11"},
		},
		{
			name: "internal name cnamed out of the zone", qname: "db.internal.example.com.",
			records: []testRecord{{owner: "db.internal.example.com.", cname: "db.attacker.example."}, {owner: "db.attacker.example.", addr: "10.20.5.11"}},
			dropped: []string{"10.20.5.11"},
		},
		{
			name: "internal cname chain", qname: "git.internal.example.com.",
			records: []testRecord{{owner: "git.internal.example.com.", cname: "gitlab.internal.example.com."}, {owner: "gitlab.internal.example.com.", addr: "10.20.5.10"}},
			kept:    []string{"10.20.5.10"},
		},
		{
			name: "external hop between internal names", qname: "api.internal.example.com.",
			records: []testRecord{
				{owner: "api.internal.example.com.", cname: "app.saas.example."},
				{owner: "app.saas.example.", cname: "db.internal.example.com."},
				{owner: "db.internal.example.com.", addr: "10.20.5.11"},
			},
			dropped: []string{"10.20.5.11"},
		},
		{
			name: "cgnat", qname: "app.example.net.",
			records: []testRecord{{owner: "app.example.net.", addr: "100.64.1.1"}},
			dropped: []string{"100.64.1.1"},
		},
		{
			name: "ula", qname: "app.example.net.",
			records: []testRecord{{owner: "app.example.net.", addr: "fd12::5"}},
			dropped: []string{"fd12::5"},
		},
		{
			name: "ipv4-mapped private", qname: "app.example.net.",
			records: []testRecord{{owner: "app.example.net.", addr: "::ffff:10.0.0.5"}},
			dropped: []string{"::ffff:10.0.0.5"},
		},
		{
			name: "internal ula", qname: "gitlab.internal.example.com.",
			records: []testRecord{{owner: "gitlab.internal.example.com.", addr: "fd12::5"}},
			kept:    []string{"fd12::5"},
		},
		{
			name: "loopback stays in the guest", qname: "app.localtest.me.",
			records: []testRecord{{owner: "app.localtest.me.", addr: "127.0.0.1"}},
			kept:    []string{"127.0.0.1"},
		},
		{
			name: "hard-denied metadata address", qname: "meta.example.net.",
			records: []testRecord{{owner: "meta.example.net.", addr: "169.254.169.254"}},
			kept:    []string{"169.254.169.254"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := packAnswer(t, tt.qname, tt.records)
			q := packAnswer(t, tt.qname, nil)
			got, dropped := dropPrivateAnswers(pol, q, resp)
			if !slices.Equal(dropped, tt.dropped) {
				t.Fatalf("dropped = %v, want %v", dropped, tt.dropped)
			}
			addrs, cnames := answerAddrs(t, got)
			if !slices.Equal(addrs, tt.kept) {
				t.Fatalf("kept = %v, want %v", addrs, tt.kept)
			}
			wantCNAMEs := 0
			for _, r := range tt.records {
				if r.cname != "" {
					wantCNAMEs++
				}
			}
			if cnames != wantCNAMEs {
				t.Fatalf("CNAME records = %d, want %d", cnames, wantCNAMEs)
			}
			if len(tt.dropped) == 0 && !slices.Equal(got, resp) {
				t.Fatal("answer without dropped records was rewritten")
			}
		})
	}
}

// TestResolveDropsPrivateAnswers checks the gateway resolver end to end: a
// private answer outside the internal zones never reaches the guest or the
// IP-to-name cache and is logged as dropped, while one inside them does.
func TestResolveDropsPrivateAnswers(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{
		"dns": "10.99.1.53",
		"internal_zones": ["corp.example"],
		"allow": [{"name": "web", "names": ["*.corp.example", "*.nip.io"], "proto": "tcp"}]
	}`), "resolve")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		qname   string
		addr    string
		kept    bool
		dropped bool
	}{
		{"internal zone", "gitlab.corp.example.", "10.20.5.10", true, false},
		{"rebinding name", "10-20-5-10.nip.io.", "10.20.5.10", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log := &syncBuf{}
			b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2"), Log: log}, pol)
			if err != nil {
				t.Fatal(err)
			}
			b.exchange = func(_ context.Context, _ string, _ netip.AddrPort, _ []byte) ([]byte, error) {
				return packAnswer(t, tt.qname, []testRecord{{owner: tt.qname, addr: tt.addr}}), nil
			}
			resp := b.resolve(t.Context(), "udp", packAnswer(t, tt.qname, nil), netip.AddrPort{})
			addrs, _ := answerAddrs(t, resp)
			if got := slices.Contains(addrs, tt.addr); got != tt.kept {
				t.Fatalf("answer %v contains %s = %v, want %v", addrs, tt.addr, got, tt.kept)
			}
			if got := len(b.names.lookup(netip.MustParseAddr(tt.addr))) > 0; got != tt.kept {
				t.Fatalf("IP-to-name cache has %s = %v, want %v", tt.addr, got, tt.kept)
			}
			if got := strings.Contains(log.String(), `"dropped":["`+tt.addr+`"]`); got != tt.dropped {
				t.Fatalf("log records dropped %s = %v, want %v:\n%s", tt.addr, got, tt.dropped, log.String())
			}
		})
	}
}

// TestDropPrivateAnswersMalformed checks that an answer the filter cannot
// parse is never passed through.
func TestDropPrivateAnswersMalformed(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{"allow": [{"name": "all", "names": ["*"], "proto": "tcp"}]}`), "malformed")
	if err != nil {
		t.Fatal(err)
	}
	q := packAnswer(t, "app.example.net.", nil)
	resp := packAnswer(t, "app.example.net.", []testRecord{{owner: "app.example.net.", addr: "10.0.0.5"}})
	got, _ := dropPrivateAnswers(pol, q, resp[:len(resp)-2])
	var msg dnsmessage.Message
	if err := msg.Unpack(got); err != nil || msg.RCode != dnsmessage.RCodeServerFailure || len(msg.Answers) != 0 {
		t.Fatalf("truncated answer = %+v (err %v), want SERVFAIL without answers", msg, err)
	}
}

// TestDropPrivateAnswersAllSections checks that a private address cannot
// bypass the filter in the authority or additional section.
func TestDropPrivateAnswersAllSections(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{"allow": [{"name": "all", "names": ["*"], "proto": "tcp"}]}`), "sections")
	if err != nil {
		t.Fatal(err)
	}
	name := dnsmessage.MustNewName("app.example.net.")
	record := func(a string) dnsmessage.Resource {
		return dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.AResource{A: netip.MustParseAddr(a).As4()},
		}
	}
	msg := dnsmessage.Message{
		Header:      dnsmessage.Header{ID: 7, Response: true},
		Questions:   []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
		Answers:     []dnsmessage.Resource{record("1.2.3.4")},
		Authorities: []dnsmessage.Resource{record("10.0.0.5")},
		Additionals: []dnsmessage.Resource{record("192.168.1.5")},
	}
	resp, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	got, dropped := dropPrivateAnswers(pol, packAnswer(t, "app.example.net.", nil), resp)
	if !slices.Equal(dropped, []string{"10.0.0.5", "192.168.1.5"}) {
		t.Fatalf("dropped = %v, want the authority and additional addresses", dropped)
	}
	var out dnsmessage.Message
	if err := out.Unpack(got); err != nil {
		t.Fatal(err)
	}
	if len(out.Answers) != 1 || len(out.Authorities) != 0 || len(out.Additionals) != 0 {
		t.Fatalf("filtered sections = %d answers, %d authorities, %d additionals; want 1, 0, 0", len(out.Answers), len(out.Authorities), len(out.Additionals))
	}
}

// TestExternalCNAMEHopCannotOpenInternalHost checks the resolver and the
// connect-time decision together: an allowed internal name whose CNAME chain
// passes through a public name never opens the private address it ends at.
func TestExternalCNAMEHopCannotOpenInternalHost(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{
		"dns": "10.99.1.53",
		"internal_zones": ["corp.example"],
		"allow": [
			{"name": "api", "names": ["api.corp.example"], "proto": "tcp"},
			{"name": "saas", "names": ["app.saas.example"], "proto": "tcp"},
			{"name": "db", "names": ["db.corp.example"], "proto": "tcp"}
		]
	}`), "cname-hop")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2")}, pol)
	if err != nil {
		t.Fatal(err)
	}
	const qname = "api.corp.example."
	b.exchange = func(_ context.Context, _ string, _ netip.AddrPort, _ []byte) ([]byte, error) {
		return packAnswer(t, qname, []testRecord{
			{owner: qname, cname: "app.saas.example."},
			{owner: "app.saas.example.", cname: "db.corp.example."},
			{owner: "db.corp.example.", addr: "10.0.0.5"},
		}), nil
	}
	resp := b.resolve(t.Context(), "udp", packAnswer(t, qname, nil), netip.AddrPort{})
	if addrs, _ := answerAddrs(t, resp); len(addrs) != 0 {
		t.Fatalf("answer addresses = %v, want none", addrs)
	}
	db := netip.MustParseAddr("10.0.0.5")
	names := b.names.lookup(db)
	if len(names) != 0 {
		t.Fatalf("IP-to-name cache for %s = %v, want empty", db, names)
	}
	env := Env{Gateway: netip.MustParseAddr("192.168.127.1"), Subnet: netip.MustParsePrefix("192.168.127.0/24"), Names: names}
	if d := Decide(pol, env, ProtoTCP, netip.AddrPortFrom(db, 5432)); d.Allow {
		t.Fatalf("Decide(10.0.0.5:5432) = %+v, want denied", d)
	}
}

func testBroker(t *testing.T, pol *Policy, exchange func() []byte) *Broker {
	t.Helper()
	b, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2")}, pol)
	if err != nil {
		t.Fatal(err)
	}
	b.exchange = func(context.Context, string, netip.AddrPort, []byte) ([]byte, error) { return exchange(), nil }
	return b
}

// TestRemovedInternalZoneClosesCachedNames checks that removing an internal
// zone by reload stops cached names whose DNS chain passed through it from
// opening their private address, before the cached answer expires.
func TestRemovedInternalZoneClosesCachedNames(t *testing.T) {
	t.Parallel()
	parse := func(zones string) *Policy {
		t.Helper()
		p, err := ParsePolicy([]byte(`{"dns": "10.99.1.53", "internal_zones": [`+zones+`],
			"allow": [{"name": "api", "names": ["api.corp.example"], "proto": "tcp"}]}`), "zones")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	before, after := parse(`"corp.example", "vendor.example"`), parse(`"corp.example"`)
	const qname = "api.corp.example."
	b := testBroker(t, before, func() []byte {
		return packAnswer(t, qname, []testRecord{
			{owner: qname, cname: "edge.vendor.example."},
			{owner: "edge.vendor.example.", addr: "10.0.0.5"},
		})
	})
	b.resolve(t.Context(), "udp", packAnswer(t, qname, nil), netip.AddrPort{})
	dst := netip.MustParseAddrPort("10.0.0.5:443")
	if d := Decide(before, b.env(before, dst.Addr()), ProtoTCP, dst); !d.Allow {
		t.Fatalf("Decide before reload = %+v, want allowed", d)
	}
	b.SetPolicy(after)
	if d := Decide(after, b.env(after, dst.Addr()), ProtoTCP, dst); d.Allow {
		t.Fatalf("Decide after removing vendor.example = %+v, want denied", d)
	}
}

// TestMalformedReplyIsNotCached checks that a reply the filter rejects as
// malformed leaves no records in the IP-to-name cache, including the valid
// private answer before the malformed record.
func TestMalformedReplyIsNotCached(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{"dns": "10.99.1.53", "allow": [{"name": "all", "names": ["*"], "proto": "tcp"}]}`), "malformed")
	if err != nil {
		t.Fatal(err)
	}
	const qname = "app.example.net."
	name := dnsmessage.MustNewName(qname)
	b := testBroker(t, pol, func() []byte {
		msg := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: 7, Response: true},
			Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
			Answers: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: [4]byte{10, 0, 0, 5}},
			}},
			Additionals: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: [4]byte{10, 0, 0, 6}},
			}},
		}
		r, err := msg.Pack()
		if err != nil {
			t.Fatal(err)
		}
		return r[:len(r)-2] // truncate the additional record
	})
	resp := b.resolve(t.Context(), "udp", packAnswer(t, qname, nil), netip.AddrPort{})
	var out dnsmessage.Message
	if err := out.Unpack(resp); err != nil || out.RCode != dnsmessage.RCodeServerFailure || len(out.Answers) != 0 {
		t.Fatalf("reply = %+v (err %v), want SERVFAIL without answers", out, err)
	}
	if names := b.names.lookup(netip.MustParseAddr("10.0.0.5")); len(names) != 0 {
		t.Fatalf("IP-to-name cache = %v, want empty", names)
	}
}

// TestErrorRepliesDropGuestRecords checks that an error reply never reflects
// records the guest put into its query, so a forged answer cannot reach the
// IP-to-name cache through a SERVFAIL.
func TestErrorRepliesDropGuestRecords(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{"dns": "10.99.1.53", "internal_zones": ["corp.example"],
		"allow": [{"name": "api", "names": ["api.corp.example"], "proto": "tcp"}]}`), "forged")
	if err != nil {
		t.Fatal(err)
	}
	const qname = "api.corp.example."
	forged := packAnswer(t, qname, []testRecord{{owner: qname, addr: "10.0.0.5"}})
	forged[2] &^= 0x80 // a query, not a response
	b := testBroker(t, pol, func() []byte {
		// An A record whose data is one byte short at the end of the reply:
		// the record walk skips it by length, but the full parse fails.
		r := packAnswer(t, qname, []testRecord{{owner: qname, addr: "10.0.0.7"}})
		r = r[:len(r)-1]
		r[len(r)-5] = 3 // RDLENGTH 4 → 3
		return r
	})
	resp := b.resolve(t.Context(), "udp", forged, netip.AddrPort{})
	var out dnsmessage.Message
	if err := out.Unpack(resp); err != nil || out.RCode != dnsmessage.RCodeServerFailure || len(out.Answers) != 0 || out.ID != 7 || len(out.Questions) != 1 {
		t.Fatalf("reply = %+v (err %v), want SERVFAIL with the question and no answers", out, err)
	}
	if names := b.names.lookup(netip.MustParseAddr("10.0.0.5")); len(names) != 0 {
		t.Fatalf("IP-to-name cache = %v, want the forged answer absent", names)
	}
	for _, rcode := range []byte{0x02, 0x03} {
		var msg dnsmessage.Message
		if err := msg.Unpack(dnsError(forged, rcode)); err != nil || len(msg.Answers) != 0 || len(msg.Questions) != 1 || !msg.Response {
			t.Fatalf("dnsError(forged, %d) = %+v (err %v), want the question only", rcode, msg, err)
		}
	}
}

// TestLogDNSStopsAtMalformedRecord checks that logging a reply with a record
// that cannot be parsed ends instead of spinning on the same record.
func TestLogDNSStopsAtMalformedRecord(t *testing.T) {
	t.Parallel()
	pol, err := ParsePolicy([]byte(`{"dns": "10.99.1.53"}`), "malformed")
	if err != nil {
		t.Fatal(err)
	}
	b := testBroker(t, pol, func() []byte { return nil })
	const qname = "app.example.net."
	for _, rec := range []testRecord{{owner: qname, addr: "192.0.2.1"}, {owner: qname, cname: "edge.example.net."}} {
		r := packAnswer(t, qname, []testRecord{rec})
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.logDNS("udp", packAnswer(t, qname, nil), r[:len(r)-2], nil, netip.AddrPort{}, nil)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("logDNS of a truncated %+v record did not return", rec)
		}
	}
}
