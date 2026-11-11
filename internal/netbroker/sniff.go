package netbroker

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Passive application-layer inspection of the first bytes a guest sends on
// an allowed TCP connection. It never changes or delays the data; it only
// records what the connection is for (TLS server name and ALPN, or the HTTP
// request line and Host). A TLS-terminating proxy can later add request and
// response details with the same "l7" event.

const (
	sniffMax   = 16 << 10
	sniffReads = 8
)

// sniffConn wraps the guest side of a spliced connection and inspects the
// data the guest sends until it identifies the protocol or gives up.
type sniffConn struct {
	net.Conn
	report func(map[string]any)

	mu    sync.Mutex
	buf   []byte
	reads int
	done  bool
}

func (c *sniffConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.inspect(p[:n])
	}
	return n, err
}

func (c *sniffConn) inspect(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.reads++
	c.buf = append(c.buf, data[:min(len(data), sniffMax-len(c.buf))]...)
	info, more := identify(c.buf)
	if info != nil {
		c.report(info)
	}
	if info != nil || !more || c.reads >= sniffReads || len(c.buf) >= sniffMax {
		c.done, c.buf = true, nil
	}
}

// CloseWrite half-closes the guest side when the wrapped connection
// supports it, so splice keeps its half-close behavior.
func (c *sniffConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// identify recognises a TLS ClientHello or an HTTP/1 request at the start
// of b. more reports whether more data could still identify it.
func identify(b []byte) (info map[string]any, more bool) {
	if len(b) == 0 {
		return nil, true
	}
	if b[0] == 0x16 {
		return identifyTLS(b)
	}
	return identifyHTTP(b)
}

func identifyTLS(b []byte) (map[string]any, bool) {
	if len(b) < 5 {
		return nil, true
	}
	if b[1] != 3 {
		return nil, false
	}
	if need := 5 + int(binary.BigEndian.Uint16(b[3:5])); len(b) < need {
		return nil, true
	}
	hello := clientHello(b)
	if hello == nil {
		return nil, false
	}
	info := map[string]any{"proto": "tls"}
	if hello.ServerName != "" {
		info["sni"] = hello.ServerName
	}
	if len(hello.SupportedProtos) > 0 {
		info["alpn"] = hello.SupportedProtos
	}
	return info, false
}

var errStop = errors.New("stop after client hello")

// clientHello parses a TLS ClientHello with crypto/tls without answering it.
func clientHello(b []byte) *tls.ClientHelloInfo {
	var hello *tls.ClientHelloInfo
	// The config only parses the hello; no handshake is ever completed.
	conn := tls.Server(&replayConn{r: bytes.NewReader(b)}, &tls.Config{ //nolint:gosec // G402: never negotiates
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			hello = h
			return nil, errStop
		},
	})
	_ = conn.Handshake()
	return hello
}

var httpMethods = []string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "CONNECT", "TRACE"}

func identifyHTTP(b []byte) (map[string]any, bool) {
	s := string(b)
	method, _, found := strings.Cut(s, " ")
	if !found {
		return nil, len(s) < 8 && isMethodPrefix(s)
	}
	if !isMethod(method) {
		return nil, false
	}
	head, _, complete := strings.Cut(s, "\r\n\r\n")
	lines := strings.Split(head, "\r\n")
	parts := strings.Fields(lines[0])
	if len(parts) < 3 || !strings.HasPrefix(parts[2], "HTTP/") {
		return nil, !complete && !strings.Contains(s, "\r\n")
	}
	info := map[string]any{"proto": "http", "method": method, "path": stripQuery(parts[1])}
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			info["host"] = strings.TrimSpace(v)
		}
	}
	if _, ok := info["host"]; !ok && !complete {
		return nil, true
	}
	return info, false
}

// stripQuery drops the query string, which often carries tokens.
func stripQuery(target string) string {
	path, _, _ := strings.Cut(target, "?")
	return path
}

func isMethod(m string) bool {
	for _, x := range httpMethods {
		if m == x {
			return true
		}
	}
	return false
}

func isMethodPrefix(s string) bool {
	for _, x := range httpMethods {
		if strings.HasPrefix(x, s) {
			return true
		}
	}
	return false
}

// replayConn feeds recorded bytes to crypto/tls and discards its output.
type replayConn struct{ r *bytes.Reader }

func (c *replayConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *replayConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *replayConn) Close() error                     { return nil }
func (c *replayConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *replayConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *replayConn) SetDeadline(time.Time) error      { return nil }
func (c *replayConn) SetReadDeadline(time.Time) error  { return nil }
func (c *replayConn) SetWriteDeadline(time.Time) error { return nil }

// prefixConn replays buffered bytes before reading from Conn.
type prefixConn struct {
	net.Conn
	buf []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.buf) > 0 {
		n := copy(p, c.buf)
		c.buf = c.buf[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func (c *prefixConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// sniffBeforeDial reads the initial bytes from c (up to sniffMax or timeout)
// to identify a TLS ClientHello or HTTP/1 request, returning the wrapped
// connection that replays those bytes, the identified info (if any), and
// whether the client began a TLS/HTTP stream that did not complete.
func sniffBeforeDial(c net.Conn, timeout time.Duration) (net.Conn, map[string]any, bool) {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	var buf []byte
	chunk := make([]byte, 4096)
	for reads := 0; reads < sniffReads && len(buf) < sniffMax; reads++ {
		n, err := c.Read(chunk[:min(len(chunk), sniffMax-len(buf))])
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			info, more := identifyForPolicy(buf)
			if info != nil {
				return &prefixConn{Conn: c, buf: buf}, info, false
			}
			if !more {
				return &prefixConn{Conn: c, buf: buf}, nil, false
			}
		}
		if err != nil {
			// If the client sent partial TLS/HTTP bytes before the deadline,
			// mark it incomplete; if 0 bytes were sent (e.g. a server-first
			// protocol), incomplete is false and the caller decides based on port.
			return &prefixConn{Conn: c, buf: buf}, nil, len(buf) > 0
		}
	}
	return &prefixConn{Conn: c, buf: buf}, nil, len(buf) > 0
}

// identifyForPolicy parses a TLS ClientHello (including across multiple TLS
// records) or a complete HTTP/1 request header block (\r\n\r\n) with strict
// authority checking.
func identifyForPolicy(b []byte) (info map[string]any, more bool) {
	if len(b) == 0 {
		return nil, true
	}
	if b[0] == 0x16 {
		if len(b) < 5 {
			return nil, true
		}
		if b[1] != 3 {
			return nil, false
		}
		info, _ := identifyTLS(b)
		if info != nil {
			return info, false
		}
		// Keep reading fragmented TLS records until ClientHello is complete or
		// sniffMax is reached.
		return nil, true
	}
	return identifyHTTPForPolicy(b)
}

func identifyHTTPForPolicy(b []byte) (map[string]any, bool) {
	s := string(b)
	method, rest, found := strings.Cut(s, " ")
	if !found {
		return nil, len(s) < 32 && isHTTPToken(s)
	}
	if !isHTTPToken(method) {
		return nil, false
	}
	head, _, complete := strings.Cut(s, "\r\n\r\n")
	if !complete {
		lineEnd := strings.Index(rest, "\r\n")
		if lineEnd >= 0 {
			parts := strings.Fields(rest[:lineEnd])
			if len(parts) < 2 || !strings.HasPrefix(parts[1], "HTTP/") {
				return nil, false
			}
		}
		return nil, true
	}
	lines := strings.Split(head, "\r\n")
	parts := strings.Fields(lines[0])
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "HTTP/") {
		return nil, false
	}
	target := parts[1]
	targetHost := extractTargetAuthority(method, target)
	var hostHeader string
	hostCount := 0
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			hostCount++
			hostHeader = normalizeAuthority(strings.TrimSpace(v))
		}
	}
	info := map[string]any{"proto": "http", "method": method, "path": stripQuery(target)}
	if hostCount > 1 || (hostCount == 1 && hostHeader == "") {
		info["bad_authority"] = true
		return info, false
	}
	if targetHost != "" && hostHeader != "" && targetHost != hostHeader {
		info["bad_authority"] = true
		return info, false
	}
	effective := hostHeader
	if effective == "" {
		effective = targetHost
	}
	if effective != "" {
		info["host"] = effective
	}
	return info, false
}

func isHTTPToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlpha := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		isDigit := c >= '0' && c <= '9'
		isTchar := strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !isAlpha && !isDigit && !isTchar {
			return false
		}
	}
	return true
}

func extractTargetAuthority(method, target string) string {
	if method == "CONNECT" {
		return normalizeAuthority(target)
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(target), "http://"); ok {
		auth, _, _ := strings.Cut(rest, "/")
		return normalizeAuthority(auth)
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(target), "https://"); ok {
		auth, _, _ := strings.Cut(rest, "/")
		return normalizeAuthority(auth)
	}
	return ""
}

func normalizeAuthority(auth string) string {
	if auth == "" || strings.ContainsAny(auth, " \t/@\\") {
		return ""
	}
	if h, _, err := net.SplitHostPort(auth); err == nil {
		return normalizeName(h)
	}
	return normalizeName(auth)
}

// sniffedPeerNameStrict extracts the normalized hostname from an l7 info map
// and reports whether an HTTP request had a malformed or conflicting authority.
func sniffedPeerNameStrict(info map[string]any) (string, bool) {
	if info == nil {
		return "", false
	}
	if bad, _ := info["bad_authority"].(bool); bad {
		return "", true
	}
	if sni, _ := info["sni"].(string); sni != "" {
		return normalizeName(sni), false
	}
	if host, _ := info["host"].(string); host != "" {
		return normalizeAuthority(host), false
	}
	return "", false
}

// enforceConn wraps a spliced guest connection to enforce hostname policy on
// delayed TLS/HTTP handshakes (after a server-first greeting) and on subsequent
// HTTP requests on keep-alive/pipelined HTTP connections, tracking HTTP body
// framing (Content-Length, chunked Transfer-Encoding, and protocol upgrades).
type enforceConn struct {
	net.Conn
	check func(peerName string, badAuthority bool) bool
	abort func()

	mu        sync.Mutex
	buf       []byte
	reads     int
	doneTLS   bool
	inHTTP    bool
	bodyBytes int64
	chunked   bool
	chunkRem  int64 // -1 when reading chunk size line; >= 0 when reading chunk data + \r\n
}

func (c *enforceConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && !c.inspect(p[:n]) {
		c.abort()
		return 0, errors.New("policy denied stream")
	}
	return n, err
}

func (c *enforceConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

func (c *enforceConn) inspect(data []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.doneTLS {
		return true
	}
	c.reads++
	c.buf = append(c.buf, data...)
	if !c.inHTTP && len(c.buf) > 0 && c.buf[0] == 0x16 {
		info, more := identifyForPolicy(c.buf)
		if info != nil {
			c.doneTLS, c.buf = true, nil
			name, bad := sniffedPeerNameStrict(info)
			return name != "" && c.check(name, bad)
		}
		return more && len(c.buf) <= sniffMax
	}
	for len(c.buf) > 0 {
		if c.bodyBytes > 0 {
			take := min(int64(len(c.buf)), c.bodyBytes)
			c.buf = c.buf[take:]
			c.bodyBytes -= take
			continue
		}
		if c.chunked {
			done, ok := c.consumeChunked()
			if !ok {
				return false
			}
			if !done {
				return true
			}
			continue
		}
		idx := bytes.Index(c.buf, []byte("\r\n\r\n"))
		if idx < 0 {
			if !c.inHTTP {
				_, more := identifyHTTPForPolicy(c.buf)
				if !more {
					// Not TLS and not HTTP (e.g. SSH or SMTP binary/text protocol).
					c.doneTLS, c.buf = true, nil
					return true
				}
			}
			return len(c.buf) <= sniffMax
		}
		block := c.buf[:idx+4]
		info, _ := identifyHTTPForPolicy(block)
		if info == nil {
			if !c.inHTTP {
				c.doneTLS, c.buf = true, nil
				return true
			}
			return false
		}
		c.inHTTP = true
		name, bad := sniffedPeerNameStrict(info)
		if name == "" || !c.check(name, bad) {
			return false
		}
		bodyLen, chunked, upgraded, ok := parseHTTPFraming(string(block[:idx]))
		if !ok || upgraded {
			return false
		}
		c.bodyBytes = bodyLen
		c.chunked = chunked
		if chunked {
			c.chunkRem = -1
		}
		c.buf = c.buf[idx+4:]
	}
	return true
}

func (c *enforceConn) consumeChunked() (done, ok bool) {
	for len(c.buf) > 0 {
		if c.chunkRem >= 0 {
			take := min(int64(len(c.buf)), c.chunkRem)
			c.buf = c.buf[take:]
			c.chunkRem -= take
			if c.chunkRem > 0 {
				return false, true
			}
			if len(c.buf) < 2 {
				return false, true
			}
			if c.buf[0] != '\r' || c.buf[1] != '\n' {
				return false, false
			}
			c.buf = c.buf[2:]
			c.chunkRem = -1
			continue
		}
		lineEnd := bytes.Index(c.buf, []byte("\r\n"))
		if lineEnd < 0 {
			return false, len(c.buf) <= 1024
		}
		line := string(c.buf[:lineEnd])
		sizeStr, _, _ := strings.Cut(line, ";")
		sz, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 16, 64)
		if err != nil || sz < 0 {
			return false, false
		}
		c.buf = c.buf[lineEnd+2:]
		if sz == 0 {
			// Consume trailer headers ending in \r\n.
			if bytes.HasPrefix(c.buf, []byte("\r\n")) {
				c.buf = c.buf[2:]
				c.chunked = false
				return true, true
			}
			trEnd := bytes.Index(c.buf, []byte("\r\n\r\n"))
			if trEnd < 0 {
				// Put back "0\r\n" until trailers arrive.
				c.buf = append([]byte("0\r\n"), c.buf...)
				return false, len(c.buf) <= sniffMax
			}
			c.buf = c.buf[trEnd+4:]
			c.chunked = false
			return true, true
		}
		c.chunkRem = sz
	}
	return false, true
}

func parseHTTPFraming(head string) (contentLen int64, chunked, upgraded, ok bool) {
	lines := strings.Split(head, "\r\n")
	parts := strings.Fields(lines[0])
	if len(parts) == 3 && parts[0] == "CONNECT" {
		return 0, false, true, true
	}
	hasCL := false
	for _, l := range lines[1:] {
		k, v, found := strings.Cut(l, ":")
		if !found {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "content-length":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 || (hasCL && n != contentLen) {
				return 0, false, false, false
			}
			contentLen, hasCL = n, true
		case "transfer-encoding":
			if strings.Contains(strings.ToLower(v), "chunked") {
				chunked = true
			} else {
				return 0, false, false, false
			}
		case "upgrade":
			upgraded = true
		}
	}
	if hasCL && chunked {
		// Reject CL + TE smuggling ambiguity.
		return 0, false, false, false
	}
	return contentLen, chunked, upgraded, true
}
