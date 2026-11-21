//go:build darwin

package gatewayhost

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// requireTCPProtocol has nothing to query on Darwin, which lacks SO_PROTOCOL.
// TCP is the only SOCK_STREAM protocol of AF_INET and AF_INET6 there, and
// validateLoopbackTCPListenerFD rejects every other address family.
func requireTCPProtocol(int) error {
	return nil
}

// tcpsListen is the XNU TCPS_LISTEN value of tcp_state (bsd/netinet/tcp_fsm.h).
const tcpsListen = 1

// socketIsListening reports whether fd is a listening TCP socket. Darwin
// rejects SO_ACCEPTCONN in getsockopt with ENOPROTOOPT, so the kernel TCP
// state from TCP_CONNECTION_INFO is read instead: tcp_ctloutput copies
// tp->t_state into tcpi_state for any TCP socket, listeners included.
func socketIsListening(fd int) (bool, error) {
	info, err := unix.GetsockoptTCPConnectionInfo(fd, unix.IPPROTO_TCP, unix.TCP_CONNECTION_INFO)
	if err != nil {
		return false, fmt.Errorf("get TCP_CONNECTION_INFO: %w", err)
	}
	return info.State == tcpsListen, nil
}
