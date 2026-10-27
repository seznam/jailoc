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

func TestBrokerAAAA(t *testing.T) {
	t.Parallel()
	for _, blocked := range []bool{false, true} {
		name := "forwarded"
		if blocked {
			name = "blocked"
		}
		t.Run(name, func(t *testing.T) {
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
				request.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: request.Questions[0].Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr("2606:4700:4700::1111").As16()}}}
				response, err := request.Pack()
				if err == nil {
					_, _ = upstream.WriteTo(response, peer)
				}
			}()
			zones := ""
			if blocked {
				zones = `,"dns_blocked_zones":["example.com"]`
			}
			policy, err := ParsePolicy([]byte(`{"dns_upstreams":["`+upstream.LocalAddr().String()+`"]`+zones+`}`), "dns")
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
			request := dnsmessage.Message{Header: dnsmessage.Header{ID: 42, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET}}}
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
			if blocked {
				if response.RCode != dnsmessage.RCodeNameError || len(response.Answers) != 0 {
					t.Fatalf("blocked AAAA response = %+v", response)
				}
				return
			}
			if len(response.Answers) != 1 {
				t.Fatalf("AAAA answers = %+v", response.Answers)
			}
			answer, ok := response.Answers[0].Body.(*dnsmessage.AAAAResource)
			if !ok || netip.AddrFrom16(answer.AAAA).String() != "2606:4700:4700::1111" {
				t.Fatalf("AAAA answer = %+v", response.Answers[0])
			}
		})
	}
}
