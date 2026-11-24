// Package proxy serves workspace web apps over HTTPS on loopback,
// behind an optional per-route login code and a boot-scoped session cookie.
package proxy

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Route maps one external hostname to the dialer of its upstream.
//
// StartPath, when set, is where a signed-in browser lands instead of "/": the
// login and a later GET or HEAD of the bare root redirect to it. It must be a
// same-origin relative path with an optional query, such as
// "/vnc.html?autoconnect=1". The default is "/".
type Route struct {
	Host       string
	Dial       func(context.Context) (net.Conn, error)
	StartPath  string
	Auth       bool
	Generation string
}

// Config supplies an already trusted certificate and the exact hostname allowlist.
// The caller owns certificate issuance, DNS, trust installation and the dialers.
type Config struct {
	Cert   tls.Certificate
	Routes []Route
}

// Server is an authenticating HTTPS reverse proxy.
type Server struct {
	cert       tls.Certificate
	routes     map[string]Route
	mu         sync.Mutex
	codes      map[string]*loginCode
	sessions   map[[32]byte]*session
	unauth     map[string]*session
	started    bool
	base       context.Context
	now        func() time.Time
	sessionTTL time.Duration
}

type loginCode struct {
	hash    [32]byte
	expires time.Time
	used    bool
}

// New returns one unpredictable, single-use, ten-minute login code per host.
// Codes are submitted in a POST body, never in a URL. Routes are copied and may
// be updated with Update; a Server may be served only once.
func New(cfg Config) (*Server, map[string]string, error) {
	g := &Server{
		routes: make(map[string]Route), codes: make(map[string]*loginCode),
		sessions: make(map[[32]byte]*session), now: time.Now, sessionTTL: 8 * time.Hour,
	}
	codes, err := g.Update(cfg)
	if err != nil {
		return nil, nil, err
	}
	return g, codes, nil
}

// Update replaces the certificate and route set of g while preserving active
// sessions and login codes of routes that remain in cfg. Routes removed from
// cfg have their active sessions closed and their login codes removed; newly
// added routes get a fresh ten-minute login code, returned in the map.
func (g *Server) Update(cfg Config) (map[string]string, error) {
	if len(cfg.Routes) == 0 {
		return nil, fmt.Errorf("proxy requires routes")
	}
	cert, err := copyCertificate(cfg.Cert)
	if err != nil {
		return nil, fmt.Errorf("proxy certificate: %w", err)
	}
	nextRoutes := make(map[string]Route, len(cfg.Routes))
	for _, route := range cfg.Routes {
		if !validHostname(route.Host) || route.Dial == nil {
			return nil, fmt.Errorf("invalid proxy route %q", route.Host)
		}
		if err := validStartPath(route.StartPath); err != nil {
			return nil, fmt.Errorf("proxy route %q start path: %w", route.Host, err)
		}
		if _, exists := nextRoutes[route.Host]; exists {
			return nil, fmt.Errorf("duplicate proxy route %q", route.Host)
		}
		if err := cert.Leaf.VerifyHostname(route.Host); err != nil {
			return nil, fmt.Errorf("certificate for route %q: %w", route.Host, err)
		}
		nextRoutes[route.Host] = route
	}

	g.mu.Lock()
	now := g.now()
	codes := make(map[string]string)
	for host, route := range nextRoutes {
		if prev, exists := g.routes[host]; exists && prev.Auth == route.Auth && prev.Generation == route.Generation {
			continue
		}
		if route.Auth {
			code, err := randomToken()
			if err != nil {
				g.mu.Unlock()
				return nil, fmt.Errorf("generate login code: %w", err)
			}
			g.codes[host] = &loginCode{hash: sha256.Sum256([]byte(code)), expires: now.Add(10 * time.Minute)}
			codes[host] = code
		} else {
			delete(g.codes, host)
		}
	}
	if g.unauth == nil {
		g.unauth = make(map[string]*session)
	}
	var revoked []*session
	for host, prev := range g.routes {
		next, keep := nextRoutes[host]
		if !keep {
			delete(g.codes, host)
		}
		if !keep || next.Auth || next.Generation != prev.Generation {
			if s := g.unauth[host]; s != nil {
				revoked = append(revoked, s)
				delete(g.unauth, host)
			}
		}
	}
	for k, s := range g.sessions {
		prev := g.routes[s.host]
		if next, keep := nextRoutes[s.host]; !keep || !next.Auth || next.Generation != prev.Generation {
			revoked = append(revoked, s)
			delete(g.sessions, k)
		}
	}
	g.cert = cert
	g.routes = nextRoutes
	g.mu.Unlock()

	for _, s := range revoked {
		s.close()
	}
	return codes, nil
}

func (g *Server) route(host string) (Route, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.routes[host]
	return r, ok
}

// Serve takes ownership of a TCP listener bound to a loopback IP. The listener
// may use any local port, but the external HTTPS authority must use port 443.
// Cancellation revokes all sessions and closes HTTP and upgraded connections.
func (g *Server) Serve(ctx context.Context, ln net.Listener) error {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return fmt.Errorf("proxy listener must be loopback TCP")
	}
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return fmt.Errorf("proxy already served")
	}
	boot, cancel := context.WithCancel(ctx)
	g.started, g.base = true, boot
	g.mu.Unlock()
	defer cancel()
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			if _, ok := g.routes[hello.ServerName]; !ok {
				return nil, fmt.Errorf("unrecognized proxy SNI")
			}
			cert := g.cert
			return &cert, nil
		},
	}
	srv := &http.Server{
		Handler: g, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute,
		MaxHeaderBytes: 32 << 10, TLSConfig: tlsConfig,
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		BaseContext:  func(net.Listener) context.Context { return boot },
		ErrorLog:     log.New(io.Discard, "", 0),
	}
	done := make(chan error, 1)
	context.AfterFunc(boot, func() {
		g.mu.Lock()
		active := make([]*session, 0, len(g.sessions)+len(g.unauth))
		for _, s := range g.sessions {
			active = append(active, s)
		}
		for _, s := range g.unauth {
			active = append(active, s)
		}
		clear(g.sessions)
		clear(g.unauth)
		g.mu.Unlock()
		for _, s := range active {
			s.close()
		}
		done <- srv.Close()
	})
	err := srv.Serve(tls.NewListener(ln, tlsConfig))
	cancel()
	if closeErr := <-done; closeErr != nil {
		return fmt.Errorf("close proxy: %w", closeErr)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve proxy: %w", err)
	}
	return nil
}

// validStartPath accepts an empty path or an absolute same-origin path that is
// neither the root nor under the gateway's own /.jailoc/ namespace, so that
// redirecting to it can neither loop nor leave the origin. Percent escapes are
// refused in the path, and dot segments, backslashes, fragments and control
// characters everywhere; the query must parse.
func validStartPath(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > 1024 || p[0] != '/' || strings.HasPrefix(p, "//") {
		return fmt.Errorf("%q is not an absolute path", p)
	}
	for _, c := range []byte(p) {
		if c <= ' ' || c == 0x7f || c == '\\' || c == '#' {
			return fmt.Errorf("%q contains a forbidden character", p)
		}
	}
	path, query, hasQuery := strings.Cut(p, "?")
	if strings.Contains(path, "%") {
		return fmt.Errorf("%q: escapes are not allowed in the path", p)
	}
	if path == "/" || path == "/.jailoc" || strings.HasPrefix(path, "/.jailoc/") {
		return fmt.Errorf("%q is not a landing page", p)
	}
	for _, seg := range strings.Split(path[1:], "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%q contains a dot segment", p)
		}
	}
	if hasQuery {
		if _, err := url.ParseQuery(query); err != nil {
			return fmt.Errorf("%q: invalid query: %w", p, err)
		}
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return fmt.Errorf("%q is not a same-origin path", p)
	}
	return nil
}

func validHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func copyCertificate(cert tls.Certificate) (tls.Certificate, error) {
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, fmt.Errorf("missing leaf")
	}
	chain := make([][]byte, len(cert.Certificate))
	for i, der := range cert.Certificate {
		chain[i] = bytes.Clone(der)
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse leaf: %w", err)
	}
	key, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return tls.Certificate{}, fmt.Errorf("missing signing key")
	}
	public, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal public key: %w", err)
	}
	if !bytes.Equal(public, leaf.RawSubjectPublicKeyInfo) {
		return tls.Certificate{}, fmt.Errorf("leaf and key mismatch")
	}
	return tls.Certificate{Certificate: chain, PrivateKey: key, Leaf: leaf, OCSPStaple: bytes.Clone(cert.OCSPStaple)}, nil
}

func randomToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}
