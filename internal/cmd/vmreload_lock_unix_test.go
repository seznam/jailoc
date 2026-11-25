//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/workspace"
)

func TestWithReloadLockSerializesCallbacks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var active, overlaps, runs atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			err := withReloadLock(context.Background(), "alpha", func() error {
				if active.Add(1) > 1 {
					overlaps.Add(1)
				}
				time.Sleep(10 * time.Millisecond)
				active.Add(-1)
				runs.Add(1)
				return nil
			})
			if err != nil {
				t.Errorf("withReloadLock: %v", err)
			}
		})
	}
	wg.Wait()
	if runs.Load() != 8 {
		t.Fatalf("callbacks run = %d, want 8", runs.Load())
	}
	if overlaps.Load() != 0 {
		t.Fatalf("%d callbacks overlapped", overlaps.Load())
	}
}

func TestWithReloadLockWorkspacesIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := withReloadLock(context.Background(), "alpha", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ran := false
		if err := withReloadLock(ctx, "beta", func() error { ran = true; return nil }); err != nil {
			t.Errorf("other workspace: %v", err)
		}
		if !ran {
			t.Error("other workspace callback did not run while alpha was locked")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withReloadLock: %v", err)
	}
}

func TestWithReloadLockReleasedAfterCallbackError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	boom := errors.New("boom")
	if err := withReloadLock(context.Background(), "alpha", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("callback error = %v, want boom", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := withReloadLock(ctx, "alpha", func() error { return nil }); err != nil {
		t.Fatalf("lock after failed callback: %v", err)
	}
}

func TestWithReloadLockCanceledWaiterDoesNotRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := withReloadLock(context.Background(), "alpha", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		ran := false
		err := withReloadLock(ctx, "alpha", func() error { ran = true; return nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiter error = %v, want deadline exceeded", err)
		}
		if ran {
			t.Error("canceled waiter ran its callback")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withReloadLock: %v", err)
	}
}

// The holder cancels the waiter and releases the lock around the moment the
// waiter's poll timer fires; the canceled waiter must never run its callback.
func TestWithReloadLockCancelWhileLockFrees(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		var ran atomic.Bool
		waiterErr := make(chan error, 1)
		err := withReloadLock(context.Background(), "alpha", func() error {
			go func() {
				waiterErr <- withReloadLock(ctx, "alpha", func() error { ran.Store(true); return nil })
			}()
			time.Sleep(reloadLockPoll)
			cancel()
			return nil
		})
		if err != nil {
			cancel()
			t.Fatalf("holder: %v", err)
		}
		if err := <-waiterErr; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want context.Canceled", err)
		}
		if ran.Load() {
			t.Fatal("canceled waiter ran its callback after the lock freed")
		}
	}
}

func TestWithReloadLockCanceledBeforeUncontendedLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	err := withReloadLock(ctx, "alpha", func() error { ran = true; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if ran {
		t.Fatal("callback ran with a canceled context")
	}
}

func TestWithReloadLockFileOutsideStateDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := withReloadLock(context.Background(), "alpha", func() error { return nil }); err != nil {
		t.Fatalf("withReloadLock: %v", err)
	}
	path := vmStateDir("alpha") + ".reload.lock"
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %v, want 0600", fi.Mode().Perm())
	}
	if _, err := os.Stat(vmStateDir("alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock created the state dir: %v", err)
	}
}

// reloadVM must take the lock before it reads the VM state, so a reload that
// waits behind another one returns the context error, not a state error.
func TestReloadVMWaitsForReloadLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "alpha"}
	err := withReloadLock(context.Background(), "alpha", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		rerr := reloadVM(ctx, nil, ws, false)
		if !errors.Is(rerr, context.DeadlineExceeded) {
			t.Errorf("reloadVM behind a held lock = %v, want deadline exceeded", rerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withReloadLock: %v", err)
	}
}

func TestReloadVMCanceledContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := reloadVM(ctx, nil, &workspace.Resolved{Name: "alpha"}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reloadVM with canceled context = %v, want context.Canceled", err)
	}
}

func TestReloadVMReleasesLockOnFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := reloadVM(context.Background(), nil, &workspace.Resolved{Name: "alpha"}, false)
	if err == nil {
		t.Fatal("reloadVM of a stopped workspace succeeded")
	}
	assertContains(t, err.Error(), "not running")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := withReloadLock(ctx, "alpha", func() error { return nil }); err != nil {
		t.Fatalf("lock after failed reload: %v", err)
	}
}
