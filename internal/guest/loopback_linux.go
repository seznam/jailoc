//go:build linux

package guest

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/stacklok/go-microvm/net/topology"
)

const (
	loopbackHeaderTimeout = 5 * time.Second
	loopbackDialTimeout   = 2 * time.Second
	loopbackStatusOK      = 0x00
	loopbackStatusFailed  = 0x01
)

// startLoopbackRelay serves LoopbackRelayPort on the guest NIC and relays
// host-initiated connections from the broker gateway to guest loopback
// (127.0.0.1, falling back to ::1). This lets published ports and ephemeral
// OAuth callbacks reach guest services bound only to loopback without
// occupying user ports on 192.168.127.2.
func startLoopbackRelay(logger *slog.Logger) (func(), error) {
	addr := net.JoinHostPort(topology.GuestIP, strconv.Itoa(LoopbackRelayPort))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("loopback relay: listen on %s: %w", addr, err)
	}
	gateway := net.ParseIP(topology.GatewayIP)
	ctx, cancel := context.WithCancel(context.Background())
	var d net.Dialer
	go serveLoopbackRelay(ctx, logger, l, gateway, d.DialContext)
	return func() {
		cancel()
		_ = l.Close()
	}, nil
}

func serveLoopbackRelay(
	ctx context.Context,
	logger *slog.Logger,
	l net.Listener,
	allowedPeer net.IP,
	dial func(ctx context.Context, network, addr string) (net.Conn, error),
) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go handleLoopbackRelayConn(ctx, logger, c, allowedPeer, dial)
	}
}

func handleLoopbackRelayConn(
	ctx context.Context,
	logger *slog.Logger,
	c net.Conn,
	allowedPeer net.IP,
	dial func(ctx context.Context, network, addr string) (net.Conn, error),
) {
	defer func() { _ = c.Close() }()
	if allowedPeer != nil {
		tcpAddr, ok := c.RemoteAddr().(*net.TCPAddr)
		if !ok || !tcpAddr.IP.Equal(allowedPeer) {
			logger.Warn("loopback relay: rejected unauthorized peer", "remote", c.RemoteAddr().String())
			return
		}
	}
	_ = c.SetReadDeadline(time.Now().Add(loopbackHeaderTimeout))
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	port := binary.BigEndian.Uint16(hdr[:])
	if port == 0 {
		_, _ = c.Write([]byte{loopbackStatusFailed})
		return
	}

	target, err := dialLoopbackTarget(ctx, dial, port)
	if err != nil {
		_, _ = c.Write([]byte{loopbackStatusFailed})
		return
	}
	defer func() { _ = target.Close() }()

	if _, err := c.Write([]byte{loopbackStatusOK}); err != nil {
		return
	}
	stop := context.AfterFunc(ctx, func() {
		_ = c.Close()
		_ = target.Close()
	})
	defer stop()
	spliceConns(c, target)
}

func dialLoopbackTarget(
	ctx context.Context,
	dial func(ctx context.Context, network, addr string) (net.Conn, error),
	port uint16,
) (net.Conn, error) {
	p := strconv.Itoa(int(port))
	var lastErr error
	for _, host := range []string{"127.0.0.1", "::1"} {
		dctx, cancel := context.WithTimeout(ctx, loopbackDialTimeout)
		conn, err := dial(dctx, "tcp", net.JoinHostPort(host, p))
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
