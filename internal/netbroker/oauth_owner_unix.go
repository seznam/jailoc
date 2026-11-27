//go:build darwin || linux

package netbroker

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// requireOwnedByCurrentUser fails unless fi, a directory the pending socket
// lives in, belongs to the effective user.
func requireOwnedByCurrentUser(dir string, fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine parent directory owner")
	}
	if uint64(st.Uid) != uint64(os.Geteuid()) { //nolint:gosec // G115: euid is non-negative
		return fmt.Errorf("parent directory %s is not owned by the current user", dir)
	}
	return nil
}
