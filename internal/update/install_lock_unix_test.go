//go:build darwin || linux

package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSelfUpdateInstallLockBusy(t *testing.T) {
	t.Parallel()

	f := newSUFixture(t, &fakeAPI{list: standardList()})

	dir, err := os.Open(filepath.Dir(f.exe))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: file descriptors fit in int
		t.Fatal(err)
	}

	rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
	if !errors.Is(err, errInstallBusy) || rep.Installed {
		t.Fatalf("selfUpdate() = %+v, %v; want errInstallBusy", rep, err)
	}
	f.assertUntouched(t)

	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_UN); err != nil { //nolint:gosec // G115: file descriptors fit in int
		t.Fatal(err)
	}
	if _, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps); err != nil {
		t.Fatalf("selfUpdate() after unlock: %v", err)
	}
	if got := f.binary(t); got != suNewBinary {
		t.Errorf("executable = %q, want %q", got, suNewBinary)
	}
}

func TestWithInstallLockCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := false
	err := withInstallLock(ctx, t.TempDir(), func() error { ran = true; return nil })
	if !errors.Is(err, context.Canceled) || ran {
		t.Fatalf("withInstallLock() = %v, ran=%v; want context.Canceled without running fn", err, ran)
	}
}
