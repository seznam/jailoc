package netbroker

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// TestBrokerDNSBlockedZones checks that, with blocked zones, answers that
// lead into a blocked zone and queries with other than one question get no
// upstream data.
func TestBrokerDNSBlockedZones(t *testing.T) {
	t.Parallel()
	name := dnsmessage.MustNewName("app.example.com.")
	question := dnsmessage.Question{Name: name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET}
	tests := []struct {
		name      string
		questions []dnsmessage.Question
		answers   []dnsmessage.Resource
		rcode     dnsmessage.RCode
		answered  bool
	}{
		{
			name:      "allowed answer",
			questions: []dnsmessage.Question{question},
			answers:   []dnsmessage.Resource{txt(name, "public")},
			rcode:     dnsmessage.RCodeSuccess,
			answered:  true,
		},
		{
			name:      "cname into blocked zone",
			questions: []dnsmessage.Question{question},
			answers: []dnsmessage.Resource{
				{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("db.corp.internal.")}},
				txt(dnsmessage.MustNewName("db.corp.internal."), "private"),
			},
			rcode: dnsmessage.RCodeNameError,
		},
		{
			name:      "record owner in blocked zone",
			questions: []dnsmessage.Question{question},
			answers:   []dnsmessage.Resource{txt(dnsmessage.MustNewName("x.corp.internal."), "private")},
			rcode:     dnsmessage.RCodeNameError,
		},
		{
			name:      "second question in blocked zone",
			questions: []dnsmessage.Question{question, {Name: dnsmessage.MustNewName("db.corp.internal."), Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET}},
			answers:   []dnsmessage.Resource{txt(name, "public")},
			rcode:     dnsmessage.RCodeFormatError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = upstream.Close() })
			go func() {
				buffer := make([]byte, 512)
				count, peer, err := upstream.ReadFrom(buffer)
				if err != nil {
					return
				}
				var request dnsmessage.Message
				if err := request.Unpack(buffer[:count]); err != nil {
					return
				}
				request.Response = true
				request.Answers = tt.answers
				response, err := request.Pack()
				if err == nil {
					_, _ = upstream.WriteTo(response, peer)
				}
			}()
			policy, err := ParsePolicy([]byte(`{"dns_upstreams":["`+upstream.LocalAddr().String()+`"],"dns_blocked_zones":["corp.internal"]}`), "dns")
			if err != nil {
				t.Fatal(err)
			}
			broker, err := New(Config{Subnet: netip.MustParsePrefix("192.168.127.0/24"), Gateway: netip.MustParseAddr("192.168.127.1"), GuestIP: netip.MustParseAddr("192.168.127.2")}, policy)
			if err != nil {
				t.Fatal(err)
			}
			guest := connectTestBroker(t, broker)
			conn, err := gonet.DialUDP(guest, nil, guestAddr(broker.cfg.Gateway, 53), ipv4.ProtocolNumber)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			request := dnsmessage.Message{Header: dnsmessage.Header{ID: 42, RecursionDesired: true}, Questions: tt.questions}
			query, err := request.Pack()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write(query); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 512)
			count, err := conn.Read(buffer)
			if err != nil {
				t.Fatal(err)
			}
			var response dnsmessage.Message
			if err := response.Unpack(buffer[:count]); err != nil {
				t.Fatal(err)
			}
			if response.RCode != tt.rcode {
				t.Fatalf("rcode = %v, want %v", response.RCode, tt.rcode)
			}
			if got := len(response.Answers) > 0; got != tt.answered {
				t.Fatalf("answers = %+v, want answered %v", response.Answers, tt.answered)
			}
		})
	}
}

func txt(name dnsmessage.Name, text string) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.TXTResource{TXT: []string{text}}}
}
