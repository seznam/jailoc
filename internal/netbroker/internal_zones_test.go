package netbroker

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"

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
