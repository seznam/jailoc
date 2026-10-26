package netbroker

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const dnsTimeout = 3 * time.Second

func (b *Broker) upstreams() []netip.AddrPort {
	if p := b.policy.Load(); p != nil && len(p.dnsUpstreams) > 0 {
		return p.dnsUpstreams
	}
	return b.cfg.DNSFallback
}

// startDNS serves DNS on the gateway over UDP and TCP. Queries are forwarded
// from the host to the configured upstreams; answers never change the policy.
func (b *Broker) startDNS(ctx context.Context) error {
	uc, err := gonet.DialUDP(b.stack, guestAddr(b.cfg.Gateway, 53), nil, ipv4.ProtocolNumber)
	if err != nil {
		return fmt.Errorf("dns udp listener: %w", err)
	}
	tl, err := gonet.ListenTCP(b.stack, *guestAddr(b.cfg.Gateway, 53), ipv4.ProtocolNumber)
	if err != nil {
		return fmt.Errorf("dns tcp listener: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = uc.Close()
		_ = tl.Close()
	}()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, peer, err := uc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			go func() {
				if resp := b.resolve(ctx, "udp", q); resp != nil {
					_, _ = uc.WriteTo(resp, peer)
				}
			}()
		}
	}()
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			go b.serveDNSTCP(ctx, c)
		}
	}()
	return nil
}

func (b *Broker) serveDNSTCP(ctx context.Context, c net.Conn) {
	defer func() { _ = c.Close() }()
	for {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		var l [2]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		q := make([]byte, binary.BigEndian.Uint16(l[:]))
		if _, err := io.ReadFull(c, q); err != nil {
			return
		}
		resp := b.resolve(ctx, "tcp", q)
		if resp == nil {
			return
		}
		out := binary.BigEndian.AppendUint16(nil, uint16(len(resp))) //nolint:gosec // G115: DNS messages are at most 65535 bytes
		if _, err := c.Write(append(out, resp...)); err != nil {
			return
		}
	}
}

// resolve forwards one query to the upstreams in order and returns the first
// answer, or SERVFAIL when none answers.
func (b *Broker) resolve(ctx context.Context, network string, q []byte) []byte {
	var resp []byte
	lastErr := errNoUpstream
	for _, up := range b.upstreams() {
		r, err := exchange(ctx, network, up, q)
		if err == nil {
			resp = r
			break
		}
		lastErr = err
	}
	b.logDNS(network, q, resp, lastErr)
	if resp == nil {
		return servfail(q)
	}
	return resp
}

func exchange(ctx context.Context, network string, up netip.AddrPort, q []byte) ([]byte, error) {
	d := net.Dialer{Timeout: dnsTimeout}
	c, err := d.DialContext(ctx, network, up.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(dnsTimeout))
	if network == "udp" {
		if _, err := c.Write(q); err != nil {
			return nil, err
		}
		buf := make([]byte, 65535)
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
	msg := binary.BigEndian.AppendUint16(nil, uint16(len(q))) //nolint:gosec // G115: DNS messages are at most 65535 bytes
	if _, err := c.Write(append(msg, q...)); err != nil {
		return nil, err
	}
	var l [2]byte
	if _, err := io.ReadFull(c, l[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func servfail(q []byte) []byte {
	if len(q) < 12 {
		return nil
	}
	r := append([]byte(nil), q...)
	r[2] |= 0x80                // QR
	r[3] = (r[3] & 0xf0) | 0x02 // RCODE SERVFAIL
	return r
}

// logDNS records the question and A/AAAA answers. The mapping is evidence for
// allowed_hosts review; it never authorizes a destination.
func (b *Broker) logDNS(network string, q, resp []byte, err error) {
	entry := map[string]any{"event": "dns", "network": network}
	var p dnsmessage.Parser
	if _, perr := p.Start(q); perr == nil {
		if qs, perr := p.Question(); perr == nil {
			entry["name"], entry["type"] = qs.Name.String(), qs.Type.String()
		}
	}
	if resp == nil {
		entry["error"] = err.Error()
	} else {
		var answers []string
		var rp dnsmessage.Parser
		if _, perr := rp.Start(resp); perr == nil {
			_ = rp.SkipAllQuestions()
			for {
				h, perr := rp.AnswerHeader()
				if perr != nil {
					break
				}
				switch h.Type {
				case dnsmessage.TypeA:
					if a, perr := rp.AResource(); perr == nil {
						answers = append(answers, netip.AddrFrom4(a.A).String())
					}
				case dnsmessage.TypeAAAA:
					if a, perr := rp.AAAAResource(); perr == nil {
						answers = append(answers, netip.AddrFrom16(a.AAAA).String())
					}
				default:
					_ = rp.SkipAnswer()
				}
			}
		}
		entry["answers"] = answers
	}
	b.record(entry)
}

// startDHCP leases the single guest address with the gateway as router and
// resolver.
func (b *Broker) startDHCP(ctx context.Context) error {
	var wq waiter.Queue
	ep, terr := b.stack.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	if terr != nil {
		return fmt.Errorf("dhcp endpoint: %s", terr)
	}
	ep.SocketOptions().SetBroadcast(true)
	if terr := ep.Bind(tcpip.FullAddress{NIC: nicID, Port: 67}); terr != nil {
		ep.Close()
		return fmt.Errorf("dhcp bind: %s", terr)
	}
	c := gonet.NewUDPConn(&wq, ep)
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
	bcast := &tcpip.FullAddress{NIC: nicID, Addr: header.IPv4Broadcast, Port: 68}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if reply := b.dhcpReply(buf[:n]); reply != nil {
				_, _ = c.WriteTo(reply, fullAddrNet(bcast))
			}
		}
	}()
	return nil
}

func fullAddrNet(a *tcpip.FullAddress) net.Addr {
	return &net.UDPAddr{IP: net.IP(a.Addr.AsSlice()), Port: int(a.Port)}
}

const (
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpAck      = 5
)

var dhcpCookie = []byte{0x63, 0x82, 0x53, 0x63}

// dhcpReply answers DISCOVER with OFFER and REQUEST with ACK, always for the
// configured guest address.
func (b *Broker) dhcpReply(req []byte) []byte {
	if len(req) < 240 || req[0] != 1 || string(req[236:240]) != string(dhcpCookie) {
		return nil
	}
	var msgType byte
	for opts := req[240:]; len(opts) > 0; {
		code := opts[0]
		if code == 255 {
			break
		}
		if code == 0 {
			opts = opts[1:]
			continue
		}
		if len(opts) < 2 || len(opts) < 2+int(opts[1]) {
			return nil
		}
		if code == 53 && opts[1] == 1 {
			msgType = opts[2]
		}
		opts = opts[2+int(opts[1]):]
	}
	var replyType byte
	switch msgType {
	case dhcpDiscover:
		replyType = dhcpOffer
	case dhcpRequest:
		replyType = dhcpAck
	default:
		return nil
	}
	gw, guest := b.cfg.Gateway.As4(), b.cfg.GuestIP.As4()
	mask := net.CIDRMask(b.cfg.Subnet.Bits(), 32)
	r := make([]byte, 240, 300)
	r[0], r[1], r[2] = 2, 1, 6
	copy(r[4:8], req[4:8])     // xid
	copy(r[10:12], req[10:12]) // flags
	copy(r[16:20], guest[:])   // yiaddr
	copy(r[20:24], gw[:])      // siaddr
	copy(r[28:44], req[28:44]) // chaddr
	copy(r[236:240], dhcpCookie)
	mtu := binary.BigEndian.AppendUint16(nil, uint16(b.cfg.MTU)) //nolint:gosec // G115: MTU fits in 16 bits
	r = append(r, 53, 1, replyType)
	r = append(r, 54, 4, gw[0], gw[1], gw[2], gw[3])
	r = append(r, 51, 4, 0, 1, 0x51, 0x80) // 86400 s
	r = append(r, 1, 4, mask[0], mask[1], mask[2], mask[3])
	r = append(r, 3, 4, gw[0], gw[1], gw[2], gw[3])
	r = append(r, 6, 4, gw[0], gw[1], gw[2], gw[3])
	r = append(r, 26, 2, mtu[0], mtu[1])
	r = append(r, 255)
	for len(r) < 300 {
		r = append(r, 0)
	}
	b.record(map[string]any{"event": "dhcp", "type": replyType, "lease": b.cfg.GuestIP.String()})
	return r
}
