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

// reloadLockPoll is how often a waiting reload retries the lock.
const reloadLockPoll = 20 * time.Millisecond

// withReloadLock runs fn while holding the workspace's reload lock, which
// serializes whole reload transactions across jailoc processes. The lock file
// lives next to the state directory so removing the state does not drop it.
// fn is not run when ctx is already canceled or is canceled while waiting.
func withReloadLock(ctx context.Context, wsName string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lock reload of workspace %q: %w", wsName, err)
	}
	path := vmStateDir(wsName) + ".reload.lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create reload lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open reload lock: %w", err)
	}
	defer func() { _ = f.Close() }() // closing the descriptor releases the flock
	for {
		// A ready poll timer can win the select against a simultaneous
		// cancel, so check the context before every attempt and again once
		// the lock is held: a canceled caller never starts fn.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for reload lock of workspace %q: %w", wsName, err)
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: file descriptors fit in int
		if err == nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("wait for reload lock of workspace %q: %w", wsName, err)
			}
			return fn()
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock reload of workspace %q: %w", wsName, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for reload lock of workspace %q: %w", wsName, ctx.Err())
		case <-time.After(reloadLockPoll):
		}
	}
}
