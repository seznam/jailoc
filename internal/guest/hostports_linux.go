package guest

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"

	"github.com/stacklok/go-microvm/net/topology"
)

// startHostPortMirror serves port on the guest loopback and relays every
// connection to the same port on the broker gateway, which forwards it to the
// host service, so tools configured for localhost:port work unchanged. IPv4
// loopback is required; IPv6 loopback is best effort. The returned function
// stops serving and closes open connections.
func startHostPortMirror(logger *slog.Logger, port uint16) (func(), error) {
	p := strconv.Itoa(int(port))
	l4, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", p))
	if err != nil {
		return nil, fmt.Errorf("host port %d: listen on loopback: %w", port, err)
	}
	listeners := []net.Listener{l4}
	if l6, err := net.Listen("tcp", net.JoinHostPort("::1", p)); err == nil {
		listeners = append(listeners, l6)
	}
	upstream := net.JoinHostPort(topology.GatewayIP, p)
	ctx, cancel := context.WithCancel(context.Background())
	for _, l := range listeners {
		go serveRelay(ctx, logger, l, upstream, "host port "+p)
	}
	return func() {
		cancel()
		for _, l := range listeners {
			_ = l.Close()
		}
	}, nil
}

// setHostPorts reconciles the loopback mirrors with ports. A port the guest
// cannot bind, because a guest process already listens on it, is logged and
// skipped: the host service stays reachable on the gateway.
func (l *liveState) setHostPorts(ports []uint16) {
	want := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		want[p] = true
	}
	for p, stop := range l.hostPorts {
		if !want[p] {
			stop()
			delete(l.hostPorts, p)
		}
	}
	for _, p := range ports {
		if _, ok := l.hostPorts[p]; ok {
			continue
		}
		stop, err := startHostPortMirror(l.logger, p)
		if err != nil {
			l.logger.Warn("host port loopback mirror disabled; use the gateway address", "port", p, "gateway", topology.GatewayIP, "error", err)
			continue
		}
		l.hostPorts[p] = stop
	}
}
