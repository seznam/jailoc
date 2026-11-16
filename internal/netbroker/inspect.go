package netbroker

import (
	"bufio"
	"container/list"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// TLS inspection and per-request HTTP policy. For a flow to a host that an
// http rule covers, the broker terminates the guest's TLS with a certificate
// for the requested name signed by the workspace inspection CA, serves HTTP
// itself, and sends each request that passes the rules to the original
// destination over a new, fully verified TLS connection (or plain HTTP). The
// broker never dials anything but the flow's original address and port.

const (
	// leafCacheSize bounds the issued certificates kept per inspection CA.
	leafCacheSize = 256
	leafLifetime  = 24 * time.Hour
	leafBackdate  = time.Hour
	// leafMinLeft is the remaining validity under which a cached certificate
	// is issued again.
	leafMinLeft = time.Hour

	tlsHandshakeTimeout = 10 * time.Second
	// peekTimeout bounds how long the first decrypted bytes of a TLS flow
	// without ALPN are awaited to tell HTTP from another protocol.
	peekTimeout = time.Second
	// maxRequestLine caps the first line read to tell HTTP from another
	// protocol. A longer line without a valid request line fails closed.
	maxRequestLine = 8 << 10

	httpReadHeaderTimeout = 30 * time.Second
	httpIdleTimeout       = 2 * time.Minute

	upstreamIdleTimeout = 90 * time.Second

	hintUpstreamCert = "upstream certificate failed verification; the broker only trusts the host system roots and ca_bundle"
	hintGuestTrust   = "the client does not trust the workspace inspection CA (pinned certificates, its own CA store or mTLS); remove the http table from the rules matching this host to pass it through uninspected"
)

// inspectFile locates the inspection CA in the policy file.
type inspectFile struct {
	CACert string `json:"ca_cert"`
	CAKey  string `json:"ca_key"`
	// UpstreamCA is an optional PEM bundle trusted for upstream servers in
	// addition to the host system roots.
	UpstreamCA string `json:"upstream_ca,omitempty"`
}

// inspector holds the inspection CA, the certificates issued from it and the
// roots that upstream servers are verified against. It belongs to one
// compiled Policy, so a reload with a new policy starts with a fresh cache.
type inspector struct {
	ca       *x509.Certificate
	caKey    crypto.Signer
	leafKey  *ecdsa.PrivateKey
	upstream *x509.CertPool
	now      func() time.Time
	// peekTimeout is how long a decrypted no-ALPN stream may stay silent
	// before it is relayed under the first-line guard; tests shorten it.
	peekTimeout time.Duration

	mu     sync.Mutex
	lru    *list.List // of *leafEntry, most recently used first
	leaves map[string]*list.Element
}

type leafEntry struct {
	name     string
	cert     *tls.Certificate
	notAfter time.Time
}

func loadInspector(f *inspectFile) (*inspector, error) {
	if f.CACert == "" || f.CAKey == "" {
		return nil, errors.New("inspect: ca_cert and ca_key are required")
	}
	certPEM, err := os.ReadFile(f.CACert) //nolint:gosec // G304: path from the jailoc-written policy
	if err != nil {
		return nil, fmt.Errorf("inspect ca_cert: %w", err)
	}
	keyPEM, err := os.ReadFile(f.CAKey) //nolint:gosec // G304: path from the jailoc-written policy
	if err != nil {
		return nil, fmt.Errorf("inspect ca_key: %w", err)
	}
	ca, err := parseCACert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("inspect ca_cert %s: %w", f.CACert, err)
	}
	key, err := parseCAKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("inspect ca_key %s: %w", f.CAKey, err)
	}
	if pub, ok := ca.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(key.Public()) {
		return nil, errors.New("inspect: ca_key does not match ca_cert")
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("inspect leaf key: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		// An empty pool fails closed: only upstream_ca can then verify.
		pool = x509.NewCertPool()
	}
	if f.UpstreamCA != "" {
		pemData, err := os.ReadFile(f.UpstreamCA) //nolint:gosec // G304: path from the jailoc-written policy
		if err != nil {
			return nil, fmt.Errorf("inspect upstream_ca: %w", err)
		}
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("inspect upstream_ca %s: no certificates", f.UpstreamCA)
		}
	}
	return &inspector{
		ca: ca, caKey: key, leafKey: leafKey, upstream: pool, now: time.Now, peekTimeout: peekTimeout,
		lru: list.New(), leaves: map[string]*list.Element{},
	}, nil
}

func parseCACert(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("not a CA certificate")
	}
	if time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("expired at %s", cert.NotAfter.Format(time.RFC3339))
	}
	return cert, nil
}

func parseCAKey(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("no PEM PRIVATE KEY block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8 key: %w", err)
	}
	signer, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is %T, want an ECDSA key", key)
	}
	return signer, nil
}

// leaf returns a certificate for name signed by the inspection CA, issued
// once and then reused until it has less than leafMinLeft of validity left.
func (i *inspector) leaf(name string) (*tls.Certificate, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := i.now()
	if el, ok := i.leaves[name]; ok {
		e := el.Value.(*leafEntry)
		if e.notAfter.Sub(now) >= leafMinLeft {
			i.lru.MoveToFront(el)
			return e.cert, nil
		}
		i.lru.Remove(el)
		delete(i.leaves, name)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("leaf serial: %w", err)
	}
	notAfter := now.Add(leafLifetime)
	if notAfter.After(i.ca.NotAfter) {
		notAfter = i.ca.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		NotBefore:             now.Add(-leafBackdate),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if len(name) <= 64 {
		tmpl.Subject = pkix.Name{CommonName: name}
	}
	if ip, err := netip.ParseAddr(name); err == nil {
		tmpl.IPAddresses = []net.IP{ip.AsSlice()}
	} else {
		tmpl.DNSNames = []string{name}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, i.ca, &i.leafKey.PublicKey, i.caKey)
	if err != nil {
		return nil, fmt.Errorf("issue certificate for %q: %w", name, err)
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: i.leafKey}
	i.leaves[name] = i.lru.PushFront(&leafEntry{name: name, cert: cert, notAfter: notAfter})
	for i.lru.Len() > leafCacheSize {
		oldest := i.lru.Back()
		i.lru.Remove(oldest)
		delete(i.leaves, oldest.Value.(*leafEntry).name)
	}
	return cert, nil
}

// httpCapable reports whether a sniffed flow can carry HTTP, so that http
// rules may apply to it: plain HTTP, or TLS that offers h2 or http/1.1 or no
// ALPN at all. Other TLS (an ALPN list without HTTP) is decided by the plain
// rules alone.
func httpCapable(info map[string]any) bool {
	switch proto, _ := info["proto"].(string); proto {
	case "http":
		return true
	case "tls":
		alpn, _ := info["alpn"].([]string)
		return len(alpn) == 0 || slices.Contains(alpn, "h2") || slices.Contains(alpn, "http/1.1")
	}
	return false
}

// closeNotifyConn reports when the connection is closed, whoever closes it
// (the HTTP server, a hijacking proxy or the flow itself).
type closeNotifyConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

func (c *closeNotifyConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// oneConnListener hands a single connection to http.Server.Serve and then
// blocks until that connection is closed.
type oneConnListener struct {
	conn net.Conn
	done <-chan struct{}
	next chan net.Conn
}

func newOneConnListener(c net.Conn, done <-chan struct{}) *oneConnListener {
	l := &oneConnListener{conn: c, done: done, next: make(chan net.Conn, 1)}
	l.next <- c
	return l
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.next:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *oneConnListener) Close() error { return nil }

func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// bufConn reads through a bufio.Reader that already holds peeked bytes.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *bufConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

const h2Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// lineClass is the verdict on the first bytes of a stream.
type lineClass int

const (
	// lineMore: the bytes so far could still become a request line.
	lineMore lineClass = iota
	// lineHTTP: a complete HTTP request line (the HTTP/2 preface starts with
	// one, "PRI * HTTP/2.0").
	lineHTTP
	// lineNotHTTP: the bytes cannot be an HTTP request line.
	lineNotHTTP
)

// isTchar reports whether c is an RFC 9110 token character.
func isTchar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// classifyRequestLine tells whether b starts with an HTTP request line:
// `token SP request-target SP HTTP/<d>.<d>` and a line end, with any
// token (RFC 9110 tchar, any case, so PROPFIND and delete count) as the
// method. Go's server also takes a bare LF as the line end, so it counts. It
// decides on the first byte that cannot continue a request line, so another
// protocol is told apart without waiting for its first line to end.
func classifyRequestLine(b []byte) lineClass {
	i := 0
	for i < len(b) && isTchar(b[i]) {
		i++
	}
	switch {
	case i == len(b):
		return lineMore
	case i == 0 || b[i] != ' ':
		return lineNotHTTP
	}
	i++
	start := i
	for i < len(b) && b[i] > ' ' && b[i] != 0x7f {
		i++
	}
	switch {
	case i == len(b):
		return lineMore
	case i == start || b[i] != ' ':
		return lineNotHTTP
	}
	rest := b[i+1:]
	const version = "HTTP/d.d"
	for j := 0; j < len(version); j++ {
		if j >= len(rest) {
			return lineMore
		}
		c := rest[j]
		if version[j] == 'd' && (c < '0' || c > '9') || version[j] != 'd' && c != version[j] {
			return lineNotHTTP
		}
	}
	rest = rest[len(version):]
	switch {
	case len(rest) == 0:
		return lineMore
	case rest[0] == '\n':
		return lineHTTP
	case rest[0] != '\r':
		return lineNotHTTP
	case len(rest) == 1:
		return lineMore
	case rest[1] == '\n':
		return lineHTTP
	}
	return lineNotHTTP
}

// peekResult is what the first decrypted bytes of a stream say.
type peekResult int

const (
	peekHTTP    peekResult = iota // a request line or the HTTP/2 preface
	peekNotHTTP                   // another protocol
	// peekSilent: nothing arrived before the deadline.
	peekSilent
	// peekInconclusive: bytes arrived, but the deadline, the end of the
	// stream or the line cap came before the first line decided.
	peekInconclusive
	// peekClosed: the stream ended or failed without any byte.
	peekClosed
)

// isTimeout reports whether err is a deadline expiring.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// peekRequest waits up to timeout for the first line of a decrypted stream
// and classifies it with classifyRequestLine. The bytes stay in br, which must
// hold at least maxRequestLine.
func peekRequest(c net.Conn, br *bufio.Reader, timeout time.Duration) peekResult {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	for n := 1; ; n = min(br.Buffered()+1, maxRequestLine) {
		b, err := br.Peek(n)
		switch classifyRequestLine(b) {
		case lineHTTP:
			return peekHTTP
		case lineNotHTTP:
			return peekNotHTTP
		}
		switch {
		case len(b) >= maxRequestLine:
			return peekInconclusive
		case err == nil:
		case len(b) > 0:
			return peekInconclusive
		case isTimeout(err):
			return peekSilent
		default:
			return peekClosed
		}
	}
}

// firstLineGuard holds back the guest's first line on a relayed stream until
// classifyRequestLine has decided it. HTTP closes the flow before a byte is
// forwarded; anything else is forwarded as read. It is for streams that stayed
// silent past the peek, which the http rules would otherwise not cover.
type firstLineGuard struct {
	net.Conn
	// deny closes the flow; it gets whether the line was HTTP (false: the
	// cap came first).
	deny func(isHTTP bool)

	pending []byte
	decided bool
	err     error
}

func (g *firstLineGuard) Read(p []byte) (int, error) {
	buf := make([]byte, 4096)
	for !g.decided {
		n, err := g.Conn.Read(buf)
		g.pending = append(g.pending, buf[:n]...)
		switch classifyRequestLine(g.pending) {
		case lineHTTP:
			g.deny(true)
			return 0, errPolicyDenied
		case lineNotHTTP:
			g.decided = true
		default:
			if len(g.pending) >= maxRequestLine {
				g.deny(false)
				return 0, errPolicyDenied
			}
		}
		if err != nil {
			// The stream ended inside a line that never became a request;
			// forward what came and then the error.
			g.decided, g.err = true, err
		}
	}
	if len(g.pending) > 0 {
		n := copy(p, g.pending)
		g.pending = g.pending[n:]
		return n, nil
	}
	if g.err != nil {
		return 0, g.err
	}
	return g.Conn.Read(p)
}

func (g *firstLineGuard) CloseWrite() error {
	if cw, ok := g.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return g.Close()
}

// serveInspected runs an inspected flow: terminate guest TLS (or read plain
// HTTP), then serve requests until the guest connection closes. The guest
// connection is closed on return.
func (f *inspectFlow) serveInspected(guest net.Conn) {
	defer f.close()
	done := make(chan struct{})
	notify := &closeNotifyConn{Conn: guest, done: done}
	var conn net.Conn = notify
	if f.scheme == "https" {
		tc, ok := f.handshake(notify)
		if !ok {
			return
		}
		conn = tc
		if tc.ConnectionState().NegotiatedProtocol == "" {
			// No ALPN: tell HTTP from another protocol by the first bytes.
			br := bufio.NewReaderSize(tc, maxRequestLine)
			stream := &bufConn{Conn: tc, r: br}
			switch peekRequest(tc, br, f.insp.peekTimeout) {
			case peekHTTP:
				conn = stream
			case peekNotHTTP:
				f.relayNotHTTP(stream, false)
				return
			case peekSilent:
				// A server-first protocol stays silent, so relay it, but
				// classify what the guest sends first: a request after a
				// delay must not slip past the http rules.
				f.relayNotHTTP(stream, true)
				return
			case peekInconclusive:
				f.notHTTPDenied(tc, Decision{Reason: "inconclusive-request-line"}, "not HTTP: the first line did not decide")
				return
			default:
				_ = tc.Close()
				return
			}
		}
	}
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:           f,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		Protocols:         &protos,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(newOneConnListener(conn, done)) }()
	<-done
	_ = srv.Close()
}

// handshake completes the guest TLS handshake with a certificate for the
// flow's name. A client that rejects the certificate is reported.
func (f *inspectFlow) handshake(c net.Conn) (*tls.Conn, bool) {
	tc := tls.Server(c, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return f.insp.leaf(f.peerName)
		},
	})
	_ = tc.SetDeadline(time.Now().Add(tlsHandshakeTimeout))
	err := tc.Handshake()
	_ = tc.SetDeadline(time.Time{})
	if err != nil {
		f.b.record(map[string]any{
			"event": "inspect-error", "conn": f.conn, "dst": f.dst.String(), "name": f.peerName,
			"error": err.Error(), "hint": hintGuestTrust,
		})
		_ = tc.Close()
		return nil, false
	}
	return tc, true
}

// notHTTPDenied records a denied non-HTTP stream and closes it.
func (f *inspectFlow) notHTTPDenied(guest net.Conn, d Decision, msg string) {
	ev := map[string]any{
		"event": "inspect-error", "conn": f.conn, "dst": f.dst.String(), "name": f.peerName,
		"error": msg, "reason": d.Reason,
	}
	if d.Rule != "" {
		ev["rule"] = d.Rule
	}
	f.b.record(ev)
	_ = guest.Close()
}

// relayNotHTTP handles a TLS flow without ALPN whose decrypted bytes are not
// HTTP. http rules never open such traffic, so the plain rules decide; an
// allowed flow is relayed over a verified upstream TLS connection. With guard
// set the guest has not sent anything yet, and its first line is checked on
// the way (see firstLineGuard).
func (f *inspectFlow) relayNotHTTP(guest net.Conn, guard bool) {
	d := f.b.peerDecision(f.p.plain(), f.env, f.dst, f.peerName)
	if !d.Allow {
		f.notHTTPDenied(guest, d, "not HTTP")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout+tlsHandshakeTimeout)
	defer cancel()
	up, err := f.dialTLS(ctx, nil)
	if err != nil {
		f.b.record(f.upstreamErrorEvent(nil, err))
		_ = guest.Close()
		return
	}
	if guard {
		guest = &firstLineGuard{Conn: guest, deny: func(isHTTP bool) {
			msg := "HTTP request after the inspection peek timed out"
			if !isHTTP {
				msg = "first line too long for a request line"
			}
			f.b.record(map[string]any{
				"event": "inspect-error", "conn": f.conn, "dst": f.dst.String(), "name": f.peerName,
				"error": msg, "reason": "http-after-silence",
			})
			_ = guest.Close()
			_ = up.Close()
		}}
	}
	splice(guest, up)
}

// isCertError reports whether err is an upstream certificate verification
// failure.
func isCertError(err error) bool {
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid)
}
