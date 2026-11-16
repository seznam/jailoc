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
		ca: ca, caKey: key, leafKey: leafKey, upstream: pool, now: time.Now,
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

// classifyHTTP tells whether b starts an HTTP request line or the HTTP/2
// client preface. more reports that more bytes could still decide.
func classifyHTTP(b []byte) (isHTTP, more bool) {
	s := string(b)
	if strings.HasPrefix(h2Preface, s) {
		if len(s) == len(h2Preface) {
			return true, false
		}
		more = true
	}
	for _, m := range httpMethods {
		line := m + " "
		if strings.HasPrefix(s, line) {
			return true, false
		}
		if strings.HasPrefix(line, s) {
			more = true
		}
	}
	return false, more
}

// peekHTTP waits up to timeout for the first bytes on a decrypted stream and
// reports whether they start HTTP. The bytes stay in br.
func peekHTTP(c net.Conn, br *bufio.Reader, timeout time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	for n := 1; ; n++ {
		b, err := br.Peek(n)
		if isHTTP, more := classifyHTTP(b); isHTTP {
			return true
		} else if !more {
			return false
		}
		if err != nil {
			return false
		}
	}
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
			br := bufio.NewReaderSize(tc, 4096)
			if !peekHTTP(tc, br, peekTimeout) {
				f.relayNotHTTP(&bufConn{Conn: tc, r: br})
				return
			}
			conn = &bufConn{Conn: tc, r: br}
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

// relayNotHTTP handles a TLS flow without ALPN whose decrypted bytes are not
// HTTP. http rules never open such traffic, so the plain rules decide; an
// allowed flow is relayed over a verified upstream TLS connection.
func (f *inspectFlow) relayNotHTTP(guest net.Conn) {
	d := f.b.peerDecision(f.p.plain(), f.env, f.dst, f.peerName)
	if !d.Allow {
		ev := map[string]any{
			"event": "inspect-error", "conn": f.conn, "dst": f.dst.String(), "name": f.peerName,
			"error": "not HTTP", "reason": d.Reason,
		}
		if d.Rule != "" {
			ev["rule"] = d.Rule
		}
		f.b.record(ev)
		_ = guest.Close()
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
