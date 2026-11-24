//go:build darwin || linux

package proxyhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	fdExchangeTimeout = 5 * time.Second

	// maxReceivedFDs sizes the receive buffer so that a few extra descriptors
	// still arrive intact and are counted and closed, rather than only being
	// reported as truncated.
	maxReceivedFDs = 4
)

// receivedRights returns every SCM_RIGHTS descriptor in oob, including those
// from a buffer that also holds unexpected or malformed messages.
func receivedRights(oob []byte) ([]int, error) {
	var msgs []unix.SocketControlMessage
	var perr error
	// Same bound as unix.ParseSocketControlMessage: shorter trailing bytes
	// cannot hold a header.
	for len(oob) >= unix.SizeofCmsghdr {
		hdr, data, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			perr = fmt.Errorf("malformed ancillary data: %w", err)
			// Under MSG_CTRUNC the kernel may externalize descriptors up to
			// the buffer limit while leaving hdr.Len at the full un-truncated
			// length; salvage any complete descriptors in the remaining bytes.
			if hdr.Level == unix.SOL_SOCKET && hdr.Type == unix.SCM_RIGHTS && len(oob) > unix.CmsgLen(0) {
				msgs = append(msgs, unix.SocketControlMessage{Header: hdr, Data: oob[unix.CmsgLen(0):]})
			}
			break
		}
		msgs = append(msgs, unix.SocketControlMessage{Header: hdr, Data: data})
		oob = rest
	}
	fds, err := rightsFromMessages(msgs)
	if err := errors.Join(perr, err); err != nil {
		return fds, fmt.Errorf("receive rights: %w", err)
	}
	return fds, nil
}

// rightsFromMessages collects descriptors from all SCM_RIGHTS messages and
// reports any other message as an error. The caller closes the descriptors.
func rightsFromMessages(msgs []unix.SocketControlMessage) ([]int, error) {
	var fds []int
	var errs []error
	for i := range msgs {
		m := msgs[i]
		if m.Header.Level != unix.SOL_SOCKET || m.Header.Type != unix.SCM_RIGHTS {
			errs = append(errs, fmt.Errorf("unexpected ancillary message level %d type %d", m.Header.Level, m.Header.Type))
			continue
		}
		if extra := len(m.Data) % 4; extra != 0 {
			errs = append(errs, fmt.Errorf("SCM_RIGHTS data has %d trailing bytes", extra))
			m.Data = m.Data[:len(m.Data)-extra]
		}
		got, err := unix.ParseUnixRights(&m)
		if err != nil {
			errs = append(errs, fmt.Errorf("parse SCM_RIGHTS: %w", err))
			continue
		}
		for _, fd := range got {
			// Go's ReadMsgUnix sets close-on-exec; repeating it also covers
			// descriptors Go's own ancillary parse skipped.
			unix.CloseOnExec(fd)
		}
		fds = append(fds, got...)
	}
	if err := errors.Join(errs...); err != nil {
		return fds, fmt.Errorf("parse rights messages: %w", err)
	}
	return fds, nil
}

func closeFDs(fds []int) error {
	var errs []error
	for _, fd := range fds {
		if err := unix.Close(fd); err != nil {
			errs = append(errs, fmt.Errorf("close received descriptor %d: %w", fd, err))
		}
	}
	return errors.Join(errs...)
}

// validateLoopbackTCPListenerFD checks that fd is a TCP stream socket
// listening on a loopback address (127.0.0.0/8 or exactly ::1) with a
// non-zero port, and returns that address.
func validateLoopbackTCPListenerFD(fd int) (*net.TCPAddr, error) {
	sotype, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		return nil, fmt.Errorf("read socket type: %w", err)
	}
	if sotype != unix.SOCK_STREAM {
		return nil, fmt.Errorf("socket type %d is not a stream socket", sotype)
	}
	if err := requireTCPProtocol(fd); err != nil {
		return nil, fmt.Errorf("check socket protocol: %w", err)
	}
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return nil, fmt.Errorf("read socket address: %w", err)
	}
	ap, err := loopbackSocketAddr(sa, "listener")
	if err != nil {
		return nil, fmt.Errorf("validate listener address: %w", err)
	}
	if ap.Port() == 0 {
		return nil, fmt.Errorf("listener address %s has no port", ap)
	}
	listening, err := socketIsListening(fd)
	if err != nil {
		return nil, fmt.Errorf("read listening state: %w", err)
	}
	if !listening {
		return nil, fmt.Errorf("socket on %s is not listening", ap)
	}
	return net.TCPAddrFromAddrPort(ap), nil
}

func loopbackSocketAddr(sa unix.Sockaddr, kind string) (netip.AddrPort, error) {
	var ap netip.AddrPort
	switch sa := sa.(type) {
	case *unix.SockaddrInet4:
		ap = netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(sa.Port)) //nolint:gosec // ports are 16-bit.
		if !ap.Addr().IsLoopback() {
			return netip.AddrPort{}, fmt.Errorf("%s address %s is not a loopback address", kind, ap)
		}
	case *unix.SockaddrInet6:
		ap = netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), uint16(sa.Port)) //nolint:gosec // ports are 16-bit.
		// Exactly ::1: IPv4-mapped and wildcard addresses are refused.
		if ap.Addr() != netip.IPv6Loopback() {
			return netip.AddrPort{}, fmt.Errorf("%s address %s is not a loopback address", kind, ap)
		}
	default:
		return netip.AddrPort{}, fmt.Errorf("socket address %T is not TCP over IPv4 or IPv6", sa)
	}
	return ap, nil
}

// verifyUnixPeer checks that conn is a stream socket whose peer, as recorded
// by the kernel at connect time, runs as want. PIDs are not consulted.
func verifyUnixPeer(conn *net.UnixConn, want uint32) error {
	rc, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	var uid uint32
	var perr error
	if err := rc.Control(func(s uintptr) {
		fd := int(s) //nolint:gosec // file descriptors fit in int.
		sotype, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil {
			perr = fmt.Errorf("read socket type: %w", err)
			return
		}
		if sotype != unix.SOCK_STREAM {
			perr = fmt.Errorf("unix socket type %d is not a stream socket", sotype)
			return
		}
		uid, perr = peerUID(fd)
	}); err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	if perr != nil {
		return fmt.Errorf("peer credentials: %w", perr)
	}
	if uid != want {
		return fmt.Errorf("peer uid %d does not match expected uid %d", uid, want)
	}
	return nil
}

// bindDeadline bounds I/O on conn by fdExchangeTimeout and the context
// deadline, and makes context cancellation interrupt blocked I/O by moving
// the deadline into the past. The returned function stops that and clears
// the deadline so the caller can keep using conn.
func bindDeadline(ctx context.Context, conn *net.UnixConn) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context ended before deadline binding: %w", err)
	}
	if err := conn.SetDeadline(ioDeadline(ctx, time.Now())); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(fired)
		// An error means conn is already closed, which unblocks pending I/O
		// just the same; the I/O path reports the context error.
		_ = conn.SetDeadline(time.Unix(1, 0))
	})
	return func() error {
		if !stop() {
			// The callback has started; wait so it cannot overwrite the
			// cleared deadline below.
			<-fired
		}
		if err := conn.SetDeadline(time.Time{}); err != nil {
			return fmt.Errorf("clear deadline: %w", err)
		}
		return nil
	}, nil
}

// ioDeadline is the earlier of now+fdExchangeTimeout and the context deadline.
func ioDeadline(ctx context.Context, now time.Time) time.Time {
	deadline := now.Add(fdExchangeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		return d
	}
	return deadline
}

// exchangeErr attributes an I/O timeout to the context when the context
// caused it. The conn deadline can expire just before ctx.Done is closed, so
// a passed context deadline counts too.
func exchangeErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%w: %w", cerr, err)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
			return fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
		}
		return fmt.Errorf("no response within %s: %w", fdExchangeTimeout, err)
	}
	return fmt.Errorf("descriptor exchange: %w", err)
}
