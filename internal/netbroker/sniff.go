package netbroker

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
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
