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

// Wire protocol of the web relay. The host writes a 2-byte big-endian guest
// port, the relay answers with one status byte and, on webRelayConnected, the
// connection becomes a plain byte stream to 127.0.0.1:<port> in the guest.
// Application bytes may follow the port without waiting for the status.
const (
	webRelayConnected   byte = 0
	webRelayDenied      byte = 1
	webRelayUnavailable byte = 2

	webRelayHandshakeTimeout = 5 * time.Second
	webRelayDialTimeout      = 5 * time.Second
	webRelayMaxConns         = 64
)

// webDialFunc opens the guest-local target of an allowed port.
type webDialFunc func(ctx context.Context, port uint16) (net.Conn, error)

// dialWebTarget dials the literal IPv4 loopback address of port; the port is
// the only caller-controlled part of the destination.
func dialWebTarget(ctx context.Context, port uint16) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
}

// webRelayAllowlist builds the immutable set of ports the relay may dial.
func webRelayAllowlist(ports []uint16) (map[uint16]bool, error) {
	if len(ports) == 0 {
		return nil, errors.New("web relay: no ports")
	}
	allowed := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		if p == 0 {
			return nil, errors.New("web relay: port 0 is invalid")
		}
		allowed[p] = true
	}
	return allowed, nil
}

// webRelay relays host connections to allowlisted guest loopback ports. The
// zero value is not usable; create it with newWebRelay.
type webRelay struct {
	logger  *slog.Logger
	allowed map[uint16]bool
	dial    webDialFunc

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
}

func newWebRelay(logger *slog.Logger, allowed map[uint16]bool, dial webDialFunc) *webRelay {
	ctx, cancel := context.WithCancel(context.Background())
	return &webRelay{
		logger:           logger,
		allowed:          allowed,
		dial:             dial,
		handshakeTimeout: webRelayHandshakeTimeout,
		dialTimeout:      webRelayDialTimeout,
		maxConns:         webRelayMaxConns,
		ctx:              ctx,
		cancel:           cancel,
		conns:            map[net.Conn]struct{}{},
	}
}

// Serve accepts connections on l in the background until Stop.
func (r *webRelay) Serve(l net.Listener) {
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

func (r *webRelay) acceptLoop(l net.Listener) {
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
func (r *webRelay) admit(c net.Conn) bool {
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

func (r *webRelay) release(c net.Conn) {
	_ = c.Close()
	r.mu.Lock()
	delete(r.conns, c)
	r.active--
	r.mu.Unlock()
}

// track registers an extra connection for shutdown; false means the relay is
// stopped and the caller must close it.
func (r *webRelay) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	r.conns[c] = struct{}{}
	return true
}

func (r *webRelay) untrack(c net.Conn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

func (r *webRelay) activeConns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Stop closes the listeners and every accepted and target connection, then
// waits for all relay goroutines. It is idempotent.
func (r *webRelay) Stop() {
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
func (r *webRelay) handle(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(r.handshakeTimeout))
	// Read exactly the header: bytes behind it are application data that
	// belong to the target, so nothing may read ahead.
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	port := binary.BigEndian.Uint16(hdr[:])
	if !r.allowed[port] {
		r.logger.Warn("web relay: port denied", "port", port)
		r.respond(c, webRelayDenied)
		return
	}
	dctx, cancel := context.WithTimeout(r.ctx, r.dialTimeout)
	t, err := r.dial(dctx, port)
	cancel()
	if err != nil {
		r.logger.Warn("web relay: target unavailable", "port", port, "error", err)
		r.respond(c, webRelayUnavailable)
		return
	}
	defer func() { _ = t.Close() }()
	if !r.track(t) {
		return
	}
	defer r.untrack(t)
	if !r.respond(c, webRelayConnected) {
		return
	}
	spliceWeb(c, t)
}

// respond writes the status byte and reports whether it was delivered.
func (r *webRelay) respond(c net.Conn, status byte) bool {
	_ = c.SetWriteDeadline(time.Now().Add(r.handshakeTimeout))
	_, err := c.Write([]byte{status})
	_ = c.SetWriteDeadline(time.Time{})
	return err == nil
}

// spliceWeb copies both directions until each ends. A clean end of one
// direction half-closes the peer so the other direction keeps flowing; a
// failure closes both connections.
func spliceWeb(a, b net.Conn) {
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

// startWebRelayOn is startWebRelay with an explicit listen address.
func startWebRelayOn(logger *slog.Logger, ports []uint16, addr string) (func(), error) {
	allowed, err := webRelayAllowlist(ports)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("web relay: listen on %s: %w", addr, err)
	}
	r := newWebRelay(logger, allowed, dialWebTarget)
	r.Serve(l)
	return r.Stop, nil
}
