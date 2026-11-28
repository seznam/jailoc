//go:build darwin || linux

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// errInstallBusy marks an install attempt that found another updater holding
// the install lock of the executable's directory.
var errInstallBusy = errors.New("another update is installing into this directory")

// withInstallLock runs fn while holding an exclusive flock on dir, which
// serializes the final identity check and replacement across jailoc processes.
// The lock lives on the directory descriptor, so it survives renaming the
// executable and needs no lock file. It never blocks: a held lock fails with
// errInstallBusy, and fn is not run when ctx is already canceled.
func withInstallLock(ctx context.Context, dir string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lock install directory %q: %w", dir, err)
	}

	d, err := os.Open(dir) //nolint:gosec // dir is the parent of the resolved executable
	if err != nil {
		return fmt.Errorf("open install directory %q: %w", dir, err)
	}
	defer func() { _ = d.Close() }() // closing the descriptor releases the flock

	if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: file descriptors fit in int
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("%w: %q is locked, retry shortly", errInstallBusy, dir)
		}

		return fmt.Errorf("lock install directory %q: %w", dir, err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lock install directory %q: %w", dir, err)
	}

	return fn()
}
