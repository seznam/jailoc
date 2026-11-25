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

// reloadLockPoll is how often a waiting reload or admission retries its lock.
const reloadLockPoll = 20 * time.Millisecond

// withReloadLock runs fn while holding the workspace's reload lock, which
// serializes whole reload transactions across jailoc processes. The lock file
// lives next to the state directory so removing the state does not drop it.
// fn is not run when ctx is already canceled or is canceled while waiting.
func withReloadLock(ctx context.Context, wsName string, fn func() error) error {
	return withFileLock(ctx, vmStateDir(wsName)+".reload.lock", fmt.Sprintf("reload lock of workspace %q", wsName), fn)
}

// withAdmissionLock runs fn while holding the lock that serializes, across
// jailoc processes, the build input checks with the starts of workspace VMs
// (see checkVMAdmission).
func withAdmissionLock(ctx context.Context, fn func() error) error {
	return withFileLock(ctx, filepath.Join(vmRootDir(), "admission.lock"), "admission lock", fn)
}

// withFileLock runs fn while holding an exclusive lock on the file at path,
// creating it. what names the lock in errors. fn is not run when ctx is
// already canceled or is canceled while waiting.
func withFileLock(ctx context.Context, path, what string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("take %s: %w", what, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory of %s: %w", what, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open %s: %w", what, err)
	}
	defer func() { _ = f.Close() }() // closing the descriptor releases the flock
	for {
		// A ready poll timer can win the select against a simultaneous
		// cancel, so check the context before every attempt and again once
		// the lock is held: a canceled caller never starts fn.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for %s: %w", what, err)
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: file descriptors fit in int
		if err == nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("wait for %s: %w", what, err)
			}
			return fn()
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("take %s: %w", what, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w", what, ctx.Err())
		case <-time.After(reloadLockPoll):
		}
	}
}
