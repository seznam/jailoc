//go:build darwin

package gatewayhost

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// xucredVersion is XUCRED_VERSION from xnu bsd/sys/ucred.h, the only layout
// of struct xucred that unix.Xucred describes.
const xucredVersion = 0

// peerUID returns the effective UID the kernel recorded for the peer of the
// connected AF_UNIX socket fd when the connection was made (LOCAL_PEERCRED).
func peerUID(fd int) (uint32, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, fmt.Errorf("read LOCAL_PEERCRED: %w", err)
	}
	if cred.Version != xucredVersion {
		return 0, fmt.Errorf("LOCAL_PEERCRED xucred version %d, want %d", cred.Version, xucredVersion)
	}
	return cred.Uid, nil
}
