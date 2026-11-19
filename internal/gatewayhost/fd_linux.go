//go:build linux

package gatewayhost

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// requireTCPProtocol refuses stream sockets of other protocols, such as SCTP
// or AF_UNIX, which share SOCK_STREAM on Linux.
func requireTCPProtocol(fd int) error {
	proto, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PROTOCOL)
	if err != nil {
		return fmt.Errorf("read socket protocol: %w", err)
	}
	if proto != unix.IPPROTO_TCP {
		return fmt.Errorf("socket protocol %d is not TCP", proto)
	}
	return nil
}

// socketIsListening reports whether fd is a listening socket (SO_ACCEPTCONN).
func socketIsListening(fd int) (bool, error) {
	listening, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil {
		return false, fmt.Errorf("get SO_ACCEPTCONN: %w", err)
	}
	return listening != 0, nil
}
