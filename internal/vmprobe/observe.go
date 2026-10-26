package vmprobe

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// Observation classes (spec section O). A completed TCP handshake alone is
// never evidence of reachability: userspace network stacks accept locally and
// reset once the policy rejects the upstream connection.
const (
	ObsBlocked     = "blocked"        // refused, unreachable, no route or timeout before any data
	ObsProxyClosed = "proxy-closed"   // connection accepted, then closed or reset without data
	ObsReached     = "reached"        // application data received from the destination
	ObsAmbiguous   = "ambiguous-open" // connection accepted, no data and no close within the timeout
)

// DefaultTimeout bounds every single probe.
const DefaultTimeout = 3 * time.Second

// Payload is the line a probe sends to sentinels so hits can be attributed.
func Payload(nonce, checkID, ctxName string) []byte {
	return fmt.Appendf(nil, "JLP1 %s %s %s\n", nonce, checkID, ctxName)
}

// ParsePayload extracts nonce, check ID and context from a probe line.
func ParsePayload(line string) (nonce, checkID, ctxName string, ok bool) {
	f := strings.Fields(line)
	if len(f) != 4 || f[0] != "JLP1" {
		return "", "", "", false
	}
	return f[1], f[2], f[3], true
}

func errDetail(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "host unreachable"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "address not available"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return err.Error()
}

// ProbeTCP connects, sends payload and classifies the outcome.
func ProbeTCP(ctx context.Context, addr string, payload []byte, timeout time.Duration) (string, string) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return ObsBlocked, errDetail(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(payload); err != nil {
		return ObsProxyClosed, "write: " + errDetail(err)
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if n > 0 {
		return ObsReached, firstLine(buf[:n])
	}
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return ObsProxyClosed, "accepted then " + errDetail(err)
	}
	return ObsAmbiguous, "accepted, " + errDetail(err)
}

// ProbeUDP sends payload and waits for any reply. A missing reply is reported
// as blocked; sentinel logs are the authoritative evidence for UDP denials.
func ProbeUDP(ctx context.Context, addr string, payload []byte, timeout time.Duration) (string, string) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return ObsBlocked, errDetail(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(payload); err != nil {
		return ObsBlocked, "write: " + errDetail(err)
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if n > 0 {
		return ObsReached, firstLine(buf[:n])
	}
	return ObsBlocked, "no reply: " + errDetail(err)
}

// ProbeTLS completes a TLS handshake with serverName.
func ProbeTLS(ctx context.Context, addr, serverName string, timeout time.Duration) (string, string) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config:    &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12},
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := d.DialContext(cctx, "tcp", addr)
	if err != nil {
		return ObsBlocked, errDetail(err)
	}
	defer func() { _ = conn.Close() }()
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return ObsReached, "tls handshake complete"
	}
	st := tc.ConnectionState()
	peer := ""
	if len(st.PeerCertificates) > 0 {
		peer = st.PeerCertificates[0].Subject.CommonName
	}
	return ObsReached, fmt.Sprintf("tls %s, peer %q", tls.VersionName(st.Version), peer)
}

// QUICVersionNegotiationPacket builds a QUIC long-header Initial with a
// reserved version, which RFC 9000 servers answer with Version Negotiation.
func QUICVersionNegotiationPacket() []byte {
	pkt := make([]byte, 1200)
	pkt[0] = 0xc0
	copy(pkt[1:5], []byte{0x1a, 0x2a, 0x3a, 0x4a})
	pkt[5] = 8
	_, _ = rand.Read(pkt[6:14])
	pkt[14] = 8
	_, _ = rand.Read(pkt[15:23])
	return pkt
}

// ProbeQUIC sends a version-negotiation trigger and expects a reply with version 0.
func ProbeQUIC(ctx context.Context, addr string, timeout time.Duration) (string, string) {
	obs, detail := probeUDPRaw(ctx, addr, QUICVersionNegotiationPacket(), timeout, func(b []byte) bool {
		return len(b) >= 5 && b[0]&0x80 != 0 && b[1] == 0 && b[2] == 0 && b[3] == 0 && b[4] == 0
	})
	return obs, detail
}

func probeUDPRaw(ctx context.Context, addr string, payload []byte, timeout time.Duration, valid func([]byte) bool) (string, string) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return ObsBlocked, errDetail(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(payload); err != nil {
		return ObsBlocked, "write: " + errDetail(err)
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if n == 0 {
		return ObsBlocked, "no reply: " + errDetail(err)
	}
	if !valid(buf[:n]) {
		return ObsReached, fmt.Sprintf("unexpected %d-byte reply", n)
	}
	return ObsReached, fmt.Sprintf("valid %d-byte reply", n)
}

// ProbeDNS queries name (A records) at server over network ("udp" or "tcp").
// Any DNS response, including NXDOMAIN or SERVFAIL, counts as reached.
func ProbeDNS(ctx context.Context, server, network, name string, timeout time.Duration) (string, string, []netip.Addr) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, network, server)
		},
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := r.LookupNetIP(cctx, "ip4", name)
	if err == nil {
		return ObsReached, fmt.Sprintf("answer %v", addrs), addrs
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		switch {
		case de.IsNotFound:
			return ObsReached, "NXDOMAIN/NODATA", nil
		case strings.Contains(de.Err, "server misbehaving"):
			return ObsReached, "error response (SERVFAIL/REFUSED)", nil
		case de.IsTimeout:
			return ObsBlocked, "timeout", nil
		}
		return ObsBlocked, de.Err, nil
	}
	return ObsBlocked, errDetail(err), nil
}

// Evaluate maps an observation to a status for the given expectation.
func Evaluate(expect, obs string) Status {
	switch expect {
	case ExpectAllow:
		if obs == ObsReached {
			return StatusPass
		}
		return StatusFail
	case ExpectDeny:
		switch obs {
		case ObsBlocked, ObsProxyClosed:
			return StatusPass
		default:
			return StatusFail
		}
	}
	return StatusReview
}

func firstLine(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return strings.ToValidUTF8(s, "?")
}
