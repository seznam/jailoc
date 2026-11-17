//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/seznam/jailoc/internal/config"
)

// Transports of session notifications.
const (
	notifyViaNative = "native" // a desktop notification by a host command
	notifyViaOSC    = "osc"    // an OSC 777 notification for the terminal
)

const (
	// macNotifyCommand shows desktop notifications on macOS.
	macNotifyCommand = "/usr/bin/osascript"
	// notifyInterval is the shortest time between two notifications.
	notifyInterval = 3 * time.Second
	// notifyTimeout bounds one delivery.
	notifyTimeout = 5 * time.Second
	// notifyDrain bounds the delivery of pending notifications after the
	// agent exited.
	notifyDrain = 2 * time.Second
	// notifyStopWait bounds the wait for a delivery that ignores its
	// cancellation when the notifier stops.
	notifyStopWait = time.Second
)

// chooseNotifyTransport returns how to deliver notifications in the given
// mode, and the command of native ones; no transport turns them off. auto
// uses native notifications when the host shows them and the user is not
// connected over SSH, whose desktop is elsewhere; otherwise the terminal's.
// native that the host cannot show is off.
func chooseNotifyTransport(mode, goos string, getenv func(string) string, lookPath func(string) (string, error)) (transport, command string) {
	native := ""
	switch goos {
	case "darwin":
		native, _ = lookPath(macNotifyCommand)
	case "linux":
		if getenv("WAYLAND_DISPLAY") != "" || getenv("DISPLAY") != "" || getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
			native, _ = lookPath("notify-send")
		}
	}
	ssh := getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != ""
	switch {
	case mode == config.NotificationsOSC:
		return notifyViaOSC, ""
	case mode == config.NotificationsNative && native != "":
		return notifyViaNative, native
	case mode == config.NotificationsAuto && native != "" && !ssh:
		return notifyViaNative, native
	case mode == config.NotificationsAuto:
		return notifyViaOSC, ""
	}
	return "", ""
}

// nativeNotifyArgs returns the arguments of the native notification command.
// The text is passed as arguments, never as code; notify-send renders markup
// in the body, so it is escaped.
func nativeNotifyArgs(goos, title, body string) []string {
	if goos == "darwin" {
		return []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body}
	}
	markup := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return []string{"--app-name=jailoc", "--", title, markup.Replace(body)}
}

// runNativeNotify runs the native notification command. Its output is
// discarded, and errors name only the command.
func runNativeNotify(ctx context.Context, command string, args []string) error {
	cmd := exec.CommandContext(ctx, command, args...) //nolint:gosec // G204: fixed notification command, text passed as arguments
	// The output is discarded, so no I/O can keep Wait from returning after
	// the kill; WaitDelay bounds it all the same.
	cmd.WaitDelay = notifyStopWait
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(command), err)
	}
	return nil
}

// notifyMessage returns the generic title and body of a notification about
// the agent in workspace ws; it never carries session content.
func notifyMessage(ws string, k notifyKind) (title, body string) {
	title = "jailoc / " + ws
	if k == notifyInput {
		return title, "The agent needs your input."
	}
	return title, "The agent finished."
}

// oscNotification returns the OSC 777 notification for the terminal, wrapped
// for tmux to pass it on to the terminal it runs in.
func oscNotification(title, body string, tmux bool) []byte {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r == ';':
				return ','
			case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
				return -1
			}
			return r
		}, s)
	}
	seq := []byte("\x1b]777;notify;" + clean(title) + ";" + clean(body) + "\x07")
	if tmux {
		return tmuxPassthrough(seq)
	}
	return seq
}

type notifyItem struct {
	kind  notifyKind
	epoch uint64
}

// notifyDeliver delivers a notification of kind k. current reports whether
// it is still wanted: the delivery is not canceled and the terminal stayed
// unfocused since the report. Deliveries check it as late as they can.
type notifyDeliver func(ctx context.Context, k notifyKind, current func() bool) error

// notifier delivers notifications one at a time, at most one per
// notifyInterval and each kind queued once, and only while the terminal
// stays unfocused from the report to the delivery.
type notifier struct {
	transport string
	deliver   notifyDeliver
	focus     *sessionFocus
	done      func() // runs when the notifier ends, nil for none
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	ended     chan struct{}

	mu      sync.Mutex
	queue   []notifyItem
	closing bool
}

// newNotifier starts a notifier that delivers with deliver and runs done,
// if not nil, when it ends.
func newNotifier(transport string, deliver notifyDeliver, focus *sessionFocus, done func()) *notifier {
	ctx, cancel := context.WithCancel(context.Background())
	n := &notifier{transport: transport, deliver: deliver, focus: focus, done: done, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), ended: make(chan struct{})}
	go n.run()
	return n
}

// post queues a notification of kind k if the terminal is unfocused.
func (n *notifier) post(k notifyKind) {
	away, epoch := n.focus.away()
	if !away {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closing {
		return // the run loop pops what is queued; leave it be
	}
	// Items queued before a focus change are stale: run drops them, so they
	// must not block a fresh notification of the same kind. Epochs only grow.
	n.queue = slices.DeleteFunc(n.queue, func(it notifyItem) bool { return it.epoch < epoch })
	if slices.ContainsFunc(n.queue, func(it notifyItem) bool { return it.kind == k }) {
		return
	}
	n.queue = append(n.queue, notifyItem{kind: k, epoch: epoch})
	n.signal()
}

func (n *notifier) signal() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// pending reports whether a notification is queued, and whether the
// notifier stops once the queue is empty.
func (n *notifier) pending() (queued, closing bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.queue) > 0, n.closing
}

func (n *notifier) pop() notifyItem {
	n.mu.Lock()
	defer n.mu.Unlock()
	it := n.queue[0]
	n.queue = n.queue[1:]
	return it
}

func (n *notifier) run() {
	defer close(n.ended)
	if n.done != nil {
		defer n.done()
	}
	var last time.Time
	for {
		queued, closing := n.pending()
		switch {
		case !queued && closing:
			return
		case !queued:
			select {
			case <-n.ctx.Done():
				return
			case <-n.wake:
			}
			continue
		}
		if wait := notifyInterval - time.Since(last); !last.IsZero() && wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-n.ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		it := n.pop()
		if !n.focus.awayAt(it.epoch) {
			continue
		}
		ctx, cancel := context.WithTimeout(n.ctx, notifyTimeout)
		current := func() bool { return ctx.Err() == nil && n.focus.awayAt(it.epoch) }
		err := n.deliver(ctx, it.kind, current)
		cancel()
		last = time.Now()
		switch {
		case err == nil || n.ctx.Err() != nil:
		case errors.Is(err, errNotifyDropped):
			slog.Debug("session notification dropped", "transport", n.transport, "reason", err)
		default:
			slog.Warn("session notification failed", "transport", n.transport, "error", err)
		}
	}
}

// stop drops queued notifications, cancels a running delivery and waits
// for the notifier to end, at most notifyStopWait for a delivery that
// ignores the cancellation.
func (n *notifier) stop() {
	n.mu.Lock()
	n.closing = true
	n.mu.Unlock()
	n.cancel()
	t := time.NewTimer(notifyStopWait)
	defer t.Stop()
	select {
	case <-n.ended:
	case <-t.C:
		slog.Warn("session notifier did not stop in time; leaving its delivery behind", "transport", n.transport, "waited", notifyStopWait)
	}
}

// drain delivers the queued notifications for at most bound, then stops.
func (n *notifier) drain(bound time.Duration) {
	n.mu.Lock()
	n.closing = true
	n.signal()
	n.mu.Unlock()
	t := time.NewTimer(bound)
	defer t.Stop()
	select {
	case <-n.ended:
	case <-t.C:
	}
	n.stop()
}
