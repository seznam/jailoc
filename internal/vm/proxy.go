package vm

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Status bytes the guest proxy relay answers the handshake with.
const (
	proxyConnected   byte = 0
	proxyDenied      byte = 1
	proxyUnavailable byte = 2
)

var (
	// ErrProxyDenied reports a port the guest proxy relay does not allow.
	ErrProxyDenied = errors.New("port denied by the proxy relay")
	// ErrProxyUnavailable reports an allowed port nothing serves in the guest.
	ErrProxyUnavailable = errors.New("port unavailable in the guest")
)

// ProxySocket is the host unix socket the network broker forwards to the guest
// proxy relay.
func ProxySocket(stateDir string) string { return filepath.Join(stateDir, "proxy.sock") }

// DialProxy opens a byte stream to the allowlisted guest loopback port through
// the proxy relay: it writes the 2-byte big-endian port and reads one status
// byte. ctx bounds and cancels the handshake only; once DialProxy returns, the
// connection is independent of ctx, as an http.Transport expects from a dial
// context that it cancels after the dial.
func DialProxy(ctx context.Context, stateDir string, port uint16) (net.Conn, error) {
	if port == 0 {
		return nil, errors.New("proxy port 0 is invalid")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", ProxySocket(stateDir))
	if err != nil {
		return nil, fmt.Errorf("connect to proxy relay: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Cancellation interrupts the blocked handshake I/O.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	status, err := proxyHandshake(conn, port)
	interrupted := !stop()
	if cerr := handshakeCtxErr(ctx, err, interrupted); cerr != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy handshake port %d: %w", port, cerr)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy handshake port %d: %w", port, err)
	}
	switch status {
	case proxyConnected:
	case proxyDenied:
		_ = conn.Close()
		return nil, fmt.Errorf("proxy port %d: %w", port, ErrProxyDenied)
	case proxyUnavailable:
		_ = conn.Close()
		return nil, fmt.Errorf("proxy port %d: %w", port, ErrProxyUnavailable)
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("proxy port %d: unknown status %d", port, status)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clear proxy handshake deadline: %w", err)
	}
	return conn, nil
}

func proxyHandshake(conn net.Conn, port uint16) (byte, error) {
	if _, err := conn.Write(binary.BigEndian.AppendUint16(nil, port)); err != nil {
		return 0, fmt.Errorf("write port: %w", err)
	}
	var status [1]byte
	if _, err := io.ReadFull(conn, status[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, fmt.Errorf("truncated response: %w", err)
		}
		return 0, fmt.Errorf("read status: %w", err)
	}
	return status[0], nil
}

// handshakeCtxErr returns the context error that ended a handshake: ctx was
// cancelled while it ran (interrupted), or the ctx deadline hit the I/O
// deadline of conn before ctx itself reported it.
func handshakeCtxErr(ctx context.Context, err error, interrupted bool) error {
	if cerr := ctx.Err(); cerr != nil && (err != nil || interrupted) {
		return cerr
	}
	if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
		if _, ok := ctx.Deadline(); ok {
			return context.DeadlineExceeded
		}
	}
	return nil
}
