package vm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// startupPoll is how often the startup watcher checks that the VM runner is
// still alive; broker exit and NIC loss are noticed at once.
const startupPoll = 250 * time.Millisecond

// Genuine causes a startup wait is cancelled with.
var (
	errStartupBrokerExited = errors.New("network broker exited")
	errStartupNICDown      = errors.New("VM network connection closed")
	errStartupVMExited     = errors.New("VM runner exited")
)

// startupWatch lists what must stay up while a VM boots.
type startupWatch struct {
	// BrokerDone is closed when the network broker exits.
	BrokerDone <-chan struct{}
	// NICDown is closed when the VMM closes its NIC connection.
	NICDown <-chan struct{}
	// Alive reports whether the VM runner process is still running.
	Alive func() bool
	// Interval is the Alive poll interval; zero means startupPoll.
	Interval time.Duration
}

// monitorStartup returns a context derived from parent that is cancelled with
// one of the errStartup* causes as soon as the broker, the NIC or the VM is
// lost, so waits for the guest fail promptly instead of running into their
// timeout. Cancelling parent cancels it too, without a startup cause. stop
// ends the watcher goroutine and is safe to call more than once; it must run
// before the VM is handed to the supervisor.
func monitorStartup(parent context.Context, w startupWatch) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	interval := w.Interval
	if interval <= 0 {
		interval = startupPoll
	}
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-parent.Done():
				return
			case <-w.BrokerDone:
				cancel(errStartupBrokerExited)
				return
			case <-w.NICDown:
				cancel(errStartupNICDown)
				return
			case <-ticker.C:
				if !w.Alive() {
					cancel(errStartupVMExited)
					return
				}
			}
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			close(quit)
			<-done
			cancel(nil) // release the context; keeps any cause set earlier
		})
	}
}

// startupError turns the outcome of a startup wait into the error Run
// returns. When the watcher cancelled the wait because the VM, its broker or
// its NIC was lost, that loss is the genuine cause and the wait's own error,
// typically a bare cancellation, is dropped. A cancelled parent is the
// caller's shutdown, not a VM failure, and any other error passes through.
func startupError(parent, monitored context.Context, phase string, err error, consoleLog string) error {
	if parent.Err() != nil {
		return err
	}
	cause := context.Cause(monitored)
	if !errors.Is(cause, errStartupBrokerExited) && !errors.Is(cause, errStartupNICDown) && !errors.Is(cause, errStartupVMExited) {
		return err
	}
	out := fmt.Errorf("%w while waiting for %s (guest console log: %s)", cause, phase, consoleLog)
	if err != nil && !errors.Is(err, context.Canceled) {
		out = fmt.Errorf("%w: %w", out, err)
	}
	return out
}
