package netbroker

import (
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Answers to TCP flows the broker denies after reading the TLS ClientHello or
// the HTTP request head. The client reports "access denied" or a 403 that
// names the rule, instead of a connection reset that looks like a network
// fault. L4 denials keep the reset.

const (
	// tlsAlertAccessDenied is the TLS access_denied alert description.
	tlsAlertAccessDenied = 49
	// denyReplyText caps a rule name or reason in a 403 body.
	denyReplyText = 64

	replyWriteTimeout = 2 * time.Second
	replyLinger       = time.Second
	replyDrainMax     = 64 << 10
)

// denyReply returns what the broker sends the guest before closing a flow it
// denied after inspection: a fatal access_denied TLS alert when head is a TLS
// ClientHello, a 403 when it is a well-formed HTTP request, and nil to reset
// the connection otherwise. A malformed request (bad_authority: conflicting
// or duplicate Host, broken header or body framing) is reset, not answered.
func denyReply(head []byte, info map[string]any, d Decision) []byte {
	switch proto, _ := info["proto"].(string); proto {
	case "tls":
		if len(head) < 3 || head[0] != 0x16 {
			return nil
		}
		// RFC 8446 §6 lets a server send a plaintext alert before the
		// ServerHello, so no certificate is needed. The record keeps the
		// version of the client's ClientHello record.
		return []byte{0x15, head[1], head[2], 0x00, 0x02, 0x02, tlsAlertAccessDenied}
	case "http":
		if bad, _ := info["bad_authority"].(bool); bad {
			return nil
		}
		return http403(info, d)
	}
	return nil
}

// http403 builds a 403 response whose body names the rule or reason that
// denied the request. It never reflects guest input such as the Host header.
func http403(info map[string]any, d Decision) []byte {
	label := "reason=" + replyText(d.Reason)
	if d.Rule != "" {
		label = "rule=" + replyText(d.Rule)
	}
	body := "blocked by network policy (" + label + ")\n"
	var b strings.Builder
	b.WriteString("HTTP/1.1 403 Forbidden\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n")
	b.WriteString("Connection: close\r\n\r\n")
	if method, _ := info["method"].(string); method != "HEAD" {
		b.WriteString(body)
	}
	return []byte(b.String())
}

// replyText keeps printable ASCII and caps the length, so a rule name cannot
// break the response.
func replyText(s string) string {
	out := make([]byte, 0, min(len(s), denyReplyText))
	for i := 0; i < len(s) && len(out) < denyReplyText; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		out = append(out, c)
	}
	return string(out)
}

// replyAndClose sends reply to the guest, half-closes the connection and
// discards what the guest still sends for a short time before closing it.
// Closing with unread data would make the stack reset the connection and
// drop the reply. Errors are ignored: the connection is closed either way.
func replyAndClose(c net.Conn, reply []byte) {
	defer func() { _ = c.Close() }()
	_ = c.SetWriteDeadline(time.Now().Add(replyWriteTimeout))
	if _, err := c.Write(reply); err != nil {
		return
	}
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
	_ = c.SetReadDeadline(time.Now().Add(replyLinger))
	_, _ = io.Copy(io.Discard, io.LimitReader(c, replyDrainMax))
}
