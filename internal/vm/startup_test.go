package vm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestMonitorStartupDetectsLoss(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		lose  func(brokerDone, nicDown chan struct{}, alive *atomic.Bool)
		cause error
	}{
		{"vm exit", func(_, _ chan struct{}, alive *atomic.Bool) { alive.Store(false) }, errStartupVMExited},
		{"broker exit", func(brokerDone, _ chan struct{}, _ *atomic.Bool) { close(brokerDone) }, errStartupBrokerExited},
		{"nic disconnect", func(_, nicDown chan struct{}, _ *atomic.Bool) { close(nicDown) }, errStartupNICDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				brokerDone, nicDown := make(chan struct{}), make(chan struct{})
				var alive atomic.Bool
				alive.Store(true)
				mctx, stop := monitorStartup(t.Context(), startupWatch{BrokerDone: brokerDone, NICDown: nicDown, Alive: alive.Load})
				defer stop()

				time.Sleep(10 * time.Millisecond)
				synctest.Wait()
				if mctx.Err() != nil {
					t.Fatalf("monitored context ended while everything is alive: %v", context.Cause(mctx))
				}
				tt.lose(brokerDone, nicDown, &alive)
				time.Sleep(startupPoll)
				synctest.Wait()
				if mctx.Err() == nil {
					t.Fatalf("monitored context still running %s after the loss", startupPoll)
				}
				if got := context.Cause(mctx); !errors.Is(got, tt.cause) {
					t.Fatalf("cause = %v, want %v", got, tt.cause)
				}
			})
		})
	}
}

func TestMonitorStartupStopEndsWatcher(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var alive atomic.Bool
		alive.Store(true)
		var polls atomic.Int32
		mctx, stop := monitorStartup(t.Context(), startupWatch{
			BrokerDone: make(chan struct{}),
			NICDown:    make(chan struct{}),
			Alive:      func() bool { polls.Add(1); return alive.Load() },
		})
		time.Sleep(startupPoll)
		synctest.Wait()
		stop()
		stop() // idempotent
		before := polls.Load()
		alive.Store(false)
		time.Sleep(5 * startupPoll)
		synctest.Wait()
		if got := polls.Load(); got != before {
			t.Fatalf("watcher polled %d more times after stop", got-before)
		}
		if got := context.Cause(mctx); errors.Is(got, errStartupVMExited) {
			t.Fatalf("stopped watcher reported a VM exit: %v", got)
		}
	})
}

func TestMonitorStartupParentCancellation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(t.Context())
		var alive atomic.Bool
		alive.Store(true)
		mctx, stop := monitorStartup(parent, startupWatch{BrokerDone: make(chan struct{}), NICDown: make(chan struct{}), Alive: alive.Load})
		defer stop()
		cancel()
		synctest.Wait()
		if !errors.Is(mctx.Err(), context.Canceled) {
			t.Fatalf("monitored context err = %v, want context.Canceled", mctx.Err())
		}
		waitErr := fmt.Errorf("wait for VM NIC: %w", mctx.Err())
		got := startupError(parent, mctx, "VM NIC", waitErr, "/state/microvm/console.log")
		if !errors.Is(got, context.Canceled) || got.Error() != waitErr.Error() {
			t.Fatalf("startupError = %v, want the plain cancellation %v", got, waitErr)
		}
		for _, bad := range []error{errStartupVMExited, errStartupBrokerExited, errStartupNICDown} {
			if errors.Is(got, bad) {
				t.Fatalf("parent cancellation reported as %v", bad)
			}
		}
	})
}

func TestStartupError(t *testing.T) {
	t.Parallel()
	const console = "/state/microvm/console.log"
	synctest.Test(t, func(t *testing.T) {
		var alive atomic.Bool
		mctx, stop := monitorStartup(t.Context(), startupWatch{BrokerDone: make(chan struct{}), NICDown: make(chan struct{}), Alive: alive.Load})
		defer stop()
		// The VM is dead from the start; a wait blocked on the context ends
		// with a cancellation error that must not hide the genuine cause.
		wait := func(ctx context.Context) error {
			<-ctx.Done()
			return fmt.Errorf("wait for guest docker: %w", ctx.Err())
		}
		err := startupError(t.Context(), mctx, "guest docker", wait(mctx), console)
		if !errors.Is(err, errStartupVMExited) {
			t.Fatalf("error = %v, want it to wrap %v", err, errStartupVMExited)
		}
		for _, want := range []string{"guest docker", console} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "context canceled") {
			t.Errorf("error %q leaks the cancellation", err)
		}
	})
	t.Run("plain failure keeps its error", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		orig := errors.New("VM NIC did not connect within 30s")
		if got := startupError(ctx, ctx, "VM NIC", orig, console); got != orig { //nolint:errorlint // identity: the error must pass through untouched
			t.Fatalf("startupError = %v, want %v", got, orig)
		}
		if got := startupError(ctx, ctx, "VM NIC", nil, console); got != nil {
			t.Fatalf("startupError(nil) = %v, want nil", got)
		}
	})
}
