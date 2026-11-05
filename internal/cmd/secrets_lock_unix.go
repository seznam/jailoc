//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// withSecretCacheLock runs fn while holding the workspace's secret cache
// lock, which serializes index updates and purges across jailoc processes.
func withSecretCacheLock(ctx context.Context, wsName string, fn func() error) error {
	path := vmStateDir(wsName) + ".secret-cache.lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create secret cache lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open secret cache lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: file descriptors fit in int
		if err == nil {
			return fn()
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock secret cache: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("lock secret cache: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
