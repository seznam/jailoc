//go:build linux

package proxyhost

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// peerUID returns the UID the kernel recorded for the peer of the connected
// AF_UNIX socket fd when the connection was made (SO_PEERCRED).
func peerUID(fd int) (uint32, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, fmt.Errorf("read SO_PEERCRED: %w", err)
	}
	return cred.Uid, nil
}
