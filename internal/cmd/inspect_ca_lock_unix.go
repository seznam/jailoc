//go:build darwin || linux

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// withInspectCALock runs fn while holding the workspace's inspection CA lock,
// which serializes CA generation across jailoc processes.
func withInspectCALock(wsName string, fn func() error) error {
	path := vmStateDir(wsName) + ".inspect-ca.lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create inspection CA lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open inspection CA lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX) //nolint:gosec // G115: file descriptors fit in int
		if err == nil {
			return fn()
		}
		if !errors.Is(err, syscall.EINTR) {
			return fmt.Errorf("lock inspection CA: %w", err)
		}
	}
}
