//go:build darwin || linux

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// withSecretCacheLock runs fn while holding the workspace's secret cache
// lock, which serializes index updates and purges across jailoc processes.
func withSecretCacheLock(wsName string, fn func() error) error {
	path := vmStateDir(wsName) + ".secret-cache.lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create secret cache lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open secret cache lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: file descriptors fit in int
		return fmt.Errorf("lock secret cache: %w", err)
	}
	return fn()
}
