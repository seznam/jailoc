package guest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// Wire protocol of the proxy relay. The host writes a 2-byte big-endian guest
// port, the relay answers with one status byte and, on proxyRelayConnected, the
// connection becomes a plain byte stream to 127.0.0.1:<port> in the guest.
// Application bytes may follow the port without waiting for the status.
const (
	proxyRelayConnected   byte = 0
	proxyRelayDenied      byte = 1
	proxyRelayUnavailable byte = 2

	proxyRelayHandshakeTimeout = 5 * time.Second
	proxyRelayDialTimeout      = 5 * time.Second
	proxyRelayMaxConns         = 64
)

// proxyDialFunc opens the guest-local target of an allowed port.
type proxyDialFunc func(ctx context.Context, port uint16) (net.Conn, error)

// dialProxyTarget dials the literal IPv4 loopback address of port; the port is
// the only caller-controlled part of the destination.
func dialProxyTarget(ctx context.Context, port uint16) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
}

// proxyRelayAllowlist builds the set of ports the relay may dial. An empty list
// is valid and denies every port.
func proxyRelayAllowlist(ports []uint16) (map[uint16]bool, error) {
	allowed := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		if p == 0 {
			return nil, errors.New("proxy relay: port 0 is invalid")
		}
		allowed[p] = true
	}
	return allowed, nil
}

// proxyRelay relays host connections to allowlisted guest loopback ports. The
// zero value is not usable; create it with newProxyRelay.
type proxyRelay struct {
	logger  *slog.Logger
	allowed map[uint16]bool // guarded by mu
	dial    proxyDialFunc

	handshakeTimeout time.Duration
	dialTimeout      time.Duration
	maxConns         int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	stopped   bool
	active    int
	listeners []net.Listener
	conns     map[net.Conn]struct{}
	// spliced maps each target connection to its accepted peer and port.
	spliced map[net.Conn]splice
}

// splice records the accepted connection and the port of a target connection.
type splice struct {
	client net.Conn
	port   uint16
}

func newProxyRelay(logger *slog.Logger, allowed map[uint16]bool, dial proxyDialFunc) *proxyRelay {
	ctx, cancel := context.WithCancel(context.Background())
	return &proxyRelay{
		logger:           logger,
		allowed:          allowed,
		dial:             dial,
		handshakeTimeout: proxyRelayHandshakeTimeout,
		dialTimeout:      proxyRelayDialTimeout,
		maxConns:         proxyRelayMaxConns,
		ctx:              ctx,
		cancel:           cancel,
		conns:            map[net.Conn]struct{}{},
		spliced:          map[net.Conn]splice{},
	}
}

// Serve accepts connections on l in the background until Stop.
func (r *proxyRelay) Serve(l net.Listener) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		_ = l.Close()
		return
	}
	r.listeners = append(r.listeners, l)
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		r.acceptLoop(l)
	}()
}

func (r *proxyRelay) acceptLoop(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		if !r.admit(c) {
			_ = c.Close()
			continue
		}
		go func() {
			defer r.wg.Done()
			defer r.release(c)
			r.handle(c)
		}()
	}
}

// admit tracks c and reserves a connection slot, or reports false when the
// relay is stopped or full. A true result obliges the caller to release c.
func (r *proxyRelay) admit(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.active >= r.maxConns {
		return false
	}
	r.active++
	r.conns[c] = struct{}{}
	r.wg.Add(1)
	return true
}

func (r *proxyRelay) release(c net.Conn) {
	_ = c.Close()
	r.mu.Lock()
	delete(r.conns, c)
	r.active--
	r.mu.Unlock()
}

// portAllowed reports whether port is in the current allowlist.
func (r *proxyRelay) portAllowed(port uint16) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowed[port]
}

// track registers target t, spliced to the accepted connection c on port, for
// shutdown and for SetPorts. False means the relay is stopped or port is no
// longer allowed, and the caller must close t.
func (r *proxyRelay) track(c, t net.Conn, port uint16) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || !r.allowed[port] {
		return false
	}
	r.conns[t] = struct{}{}
	r.spliced[t] = splice{client: c, port: port}
	return true
}

func (r *proxyRelay) untrack(t net.Conn) {
	r.mu.Lock()
	delete(r.conns, t)
	delete(r.spliced, t)
	r.mu.Unlock()
}

// SetPorts replaces the allowlist and closes every connection spliced to a
// port that is no longer allowed. On an invalid list the allowlist stays as it
// was.
func (r *proxyRelay) SetPorts(ports []uint16) error {
	allowed, err := proxyRelayAllowlist(ports)
	if err != nil {
		return err
	}
	r.replaceAllowed(func(map[uint16]bool) map[uint16]bool { return allowed })
	return nil
}

// RetainPorts removes from the allowlist every port not in ports, adding none,
// and closes the connections spliced to the removed ports. On an invalid list
// the allowlist stays as it was.
func (r *proxyRelay) RetainPorts(ports []uint16) error {
	keep, err := proxyRelayAllowlist(ports)
	if err != nil {
		return err
	}
	r.replaceAllowed(func(cur map[uint16]bool) map[uint16]bool {
		next := make(map[uint16]bool, len(cur))
		for p := range cur {
			if keep[p] {
				next[p] = true
			}
		}
		return next
	})
	return nil
}

// replaceAllowed sets the allowlist to next(current) and closes every
// connection spliced to a port that left it.
func (r *proxyRelay) replaceAllowed(next func(map[uint16]bool) map[uint16]bool) {
	r.mu.Lock()
	allowed := next(r.allowed)
	r.allowed = allowed
	var closing []net.Conn
	for t, s := range r.spliced {
		if !allowed[s.port] {
			closing = append(closing, t, s.client)
		}
	}
	r.mu.Unlock()
	for _, c := range closing {
		_ = c.Close()
	}
}

func (r *proxyRelay) activeConns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Stop closes the listeners and every accepted and target connection, then
// waits for all relay goroutines. It is idempotent.
func (r *proxyRelay) Stop() {
	r.mu.Lock()
	r.stopped = true
	listeners := r.listeners
	conns := make([]net.Conn, 0, len(r.conns))
	for c := range r.conns {
		conns = append(conns, c)
	}
	r.mu.Unlock()
	r.cancel()
	for _, l := range listeners {
		_ = l.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	r.wg.Wait()
}

// handle runs the handshake on c and splices it to the target.
func (r *proxyRelay) handle(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(r.handshakeTimeout))
	// Read exactly the header: bytes behind it are application data that
	// belong to the target, so nothing may read ahead.
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	port := binary.BigEndian.Uint16(hdr[:])
	if !r.portAllowed(port) {
		r.logger.Warn("proxy relay: port denied", "port", port)
		r.respond(c, proxyRelayDenied)
		return
	}
	dctx, cancel := context.WithTimeout(r.ctx, r.dialTimeout)
	t, err := r.dial(dctx, port)
	cancel()
	if err != nil {
		r.logger.Warn("proxy relay: target unavailable", "port", port, "error", err)
		r.respond(c, proxyRelayUnavailable)
		return
	}
	defer func() { _ = t.Close() }()
	// The allowlist may have changed while dialing: SetPorts only closes
	// connections it already knows, so the port is checked again here.
	if !r.track(c, t, port) {
		r.logger.Warn("proxy relay: port denied", "port", port)
		r.respond(c, proxyRelayDenied)
		return
	}
	defer r.untrack(t)
	if !r.respond(c, proxyRelayConnected) {
		return
	}
	spliceProxy(c, t)
}

// respond writes the status byte and reports whether it was delivered.
func (r *proxyRelay) respond(c net.Conn, status byte) bool {
	_ = c.SetWriteDeadline(time.Now().Add(r.handshakeTimeout))
	_, err := c.Write([]byte{status})
	_ = c.SetWriteDeadline(time.Time{})
	return err == nil
}

// spliceProxy copies both directions until each ends. A clean end of one
// direction half-closes the peer so the other direction keeps flowing; a
// failure closes both connections.
func spliceProxy(a, b net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		if _, err := io.Copy(dst, src); err != nil {
			_ = a.Close()
			_ = b.Close()
			return
		}
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// startProxyRelayOn is startProxyRelay with an explicit listen address.
func startProxyRelayOn(logger *slog.Logger, ports []uint16, addr string) (*proxyRelay, error) {
	allowed, err := proxyRelayAllowlist(ports)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("proxy relay: listen on %s: %w", addr, err)
	}
	r := newProxyRelay(logger, allowed, dialProxyTarget)
	r.Serve(l)
	return r, nil
}
