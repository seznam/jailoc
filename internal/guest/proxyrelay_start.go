package guest

import (
	"log/slog"
	"net"
	"strconv"

	"github.com/stacklok/go-microvm/net/topology"
)

// startProxyRelay serves the proxy relay on the guest NIC address, where the host
// network broker forwards its private unix socket. It relays only to the
// ports in r.Ports on the guest loopback, none when the list is empty. The
// returned relay replaces its ports with SetPorts and, with Stop, stops serving
// and closes every open connection.
func startProxyRelay(logger *slog.Logger, r *ProxyRelay) (*proxyRelay, error) {
	return startProxyRelayOn(logger, r.Ports, net.JoinHostPort(topology.GuestIP, strconv.Itoa(ProxyRelayPort)))
}
