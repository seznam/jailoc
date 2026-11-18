package guest

import (
	"log/slog"
	"net"
	"strconv"

	"github.com/stacklok/go-microvm/net/topology"
)

// startWebRelay serves the web relay on the guest NIC address, where the host
// network broker forwards its private unix socket. It relays only to the
// ports in r.Ports on the guest loopback. The returned function stops serving
// and closes every open connection.
func startWebRelay(logger *slog.Logger, r *WebRelay) (stop func(), err error) {
	return startWebRelayOn(logger, r.Ports, net.JoinHostPort(topology.GuestIP, strconv.Itoa(WebRelayPort)))
}
