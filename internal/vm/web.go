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

// Status bytes the guest web relay answers the handshake with.
const (
	webConnected   byte = 0
	webDenied      byte = 1
	webUnavailable byte = 2
)

var (
	// ErrWebDenied reports a port the guest web relay does not allow.
	ErrWebDenied = errors.New("port denied by the web relay")
	// ErrWebUnavailable reports an allowed port nothing serves in the guest.
	ErrWebUnavailable = errors.New("port unavailable in the guest")
)

// WebSocket is the host unix socket the network broker forwards to the guest
// web relay.
func WebSocket(stateDir string) string { return filepath.Join(stateDir, "web.sock") }

// DialWeb opens a byte stream to the allowlisted guest loopback port through
// the web relay: it writes the 2-byte big-endian port and reads one status
// byte. ctx bounds and cancels the handshake only; once DialWeb returns, the
// connection is independent of ctx, as an http.Transport expects from a dial
// context that it cancels after the dial.
func DialWeb(ctx context.Context, stateDir string, port uint16) (net.Conn, error) {
	if port == 0 {
		return nil, errors.New("web port 0 is invalid")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", WebSocket(stateDir))
	if err != nil {
		return nil, fmt.Errorf("connect to web relay: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Cancellation interrupts the blocked handshake I/O.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	status, err := webHandshake(conn, port)
	interrupted := !stop()
	if cerr := handshakeCtxErr(ctx, err, interrupted); cerr != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("web handshake port %d: %w", port, cerr)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("web handshake port %d: %w", port, err)
	}
	switch status {
	case webConnected:
	case webDenied:
		_ = conn.Close()
		return nil, fmt.Errorf("web port %d: %w", port, ErrWebDenied)
	case webUnavailable:
		_ = conn.Close()
		return nil, fmt.Errorf("web port %d: %w", port, ErrWebUnavailable)
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("web port %d: unknown status %d", port, status)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clear web handshake deadline: %w", err)
	}
	return conn, nil
}

func webHandshake(conn net.Conn, port uint16) (byte, error) {
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
