package proxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const sessionCookie = "__Host-jailoc"

type session struct {
	host    string
	expires time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	conns   map[*sessionConn]struct{}
}

func (s *session) track(conn net.Conn) (net.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		// Revocation won the race with a completed dial or hijack.
		_ = conn.Close()
		return nil, fmt.Errorf("proxy session expired")
	}
	tracked := &sessionConn{Conn: conn, owner: s}
	s.conns[tracked] = struct{}{}
	return tracked, nil
}

func (s *session) close() {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	conns := make([]*sessionConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		// Closing revoked connections is best-effort; no operation follows it.
		_ = conn.Close()
	}
}

type sessionConn struct {
	net.Conn
	owner *session
	once  sync.Once
	err   error
}

func (c *sessionConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.conns, c)
		c.owner.mu.Unlock()
	})
	if c.err != nil {
		return fmt.Errorf("close session connection: %w", c.err)
	}
	return nil
}

func (g *Server) authenticated(r *http.Request, host string) *session {
	var token string
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			if token != "" {
				return nil
			}
			token = c.Value
		}
	}
	if token == "" {
		return nil
	}
	g.mu.Lock()
	s := g.sessions[sha256.Sum256([]byte(token))]
	g.mu.Unlock()
	if s == nil || s.host != host {
		return nil
	}
	if s.ctx.Err() != nil || !g.now().Before(s.expires) {
		s.close()
		return nil
	}
	return s
}

func setSessionCookie(w http.ResponseWriter, token string) {
	cookie := &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60,
	}
	if token == "" {
		cookie.MaxAge = -1
	}
	http.SetCookie(w, cookie)
}
