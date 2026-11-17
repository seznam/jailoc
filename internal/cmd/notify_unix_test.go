//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/seznam/jailoc/internal/config"
)

func TestChooseNotifyTransport(t *testing.T) {
	t.Parallel()

	found := func(paths ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, p := range paths {
				if p == name || p == "/usr/bin/"+name {
					return p, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	tests := []struct {
		name          string
		mode          string
		goos          string
		env           map[string]string
		lookPath      func(string) (string, error)
		wantTransport string
		wantCommand   string
	}{
		{name: "off", mode: config.NotificationsOff, goos: "darwin", lookPath: found(macNotifyCommand)},
		{name: "osc", mode: config.NotificationsOSC, goos: "darwin", lookPath: found(macNotifyCommand), wantTransport: notifyViaOSC},
		{name: "auto on macOS", mode: config.NotificationsAuto, goos: "darwin", lookPath: found(macNotifyCommand), wantTransport: notifyViaNative, wantCommand: macNotifyCommand},
		{name: "auto over SSH", mode: config.NotificationsAuto, goos: "darwin", env: map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22"}, lookPath: found(macNotifyCommand), wantTransport: notifyViaOSC},
		{name: "auto over SSH TTY", mode: config.NotificationsAuto, goos: "linux", env: map[string]string{"SSH_TTY": "/dev/pts/1", "DISPLAY": ":0"}, lookPath: found("/usr/bin/notify-send"), wantTransport: notifyViaOSC},
		{name: "auto on a Linux desktop", mode: config.NotificationsAuto, goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, lookPath: found("/usr/bin/notify-send"), wantTransport: notifyViaNative, wantCommand: "/usr/bin/notify-send"},
		{name: "auto on a Linux D-Bus session only", mode: config.NotificationsAuto, goos: "linux", env: map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"}, lookPath: found("/usr/bin/notify-send"), wantTransport: notifyViaNative, wantCommand: "/usr/bin/notify-send"},
		{name: "native on a Linux D-Bus session only", mode: config.NotificationsNative, goos: "linux", env: map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"}, lookPath: found("/usr/bin/notify-send"), wantTransport: notifyViaNative, wantCommand: "/usr/bin/notify-send"},
		{name: "auto over SSH with a D-Bus session", mode: config.NotificationsAuto, goos: "linux", env: map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"}, lookPath: found("/usr/bin/notify-send"), wantTransport: notifyViaOSC},
		{name: "auto without a display", mode: config.NotificationsAuto, goos: "linux", lookPath: found("/usr/bin/notify-send"), wantTransport: notifyViaOSC},
		{name: "auto without notify-send", mode: config.NotificationsAuto, goos: "linux", env: map[string]string{"DISPLAY": ":0"}, lookPath: found(), wantTransport: notifyViaOSC},
		{name: "native unusable", mode: config.NotificationsNative, goos: "linux", env: map[string]string{"DISPLAY": ":0"}, lookPath: found()},
		{name: "native over SSH", mode: config.NotificationsNative, goos: "darwin", env: map[string]string{"SSH_TTY": "/dev/pts/1"}, lookPath: found(macNotifyCommand), wantTransport: notifyViaNative, wantCommand: macNotifyCommand},
		{name: "auto on another system", mode: config.NotificationsAuto, goos: "freebsd", lookPath: found("notify-send"), wantTransport: notifyViaOSC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tt.env[k] }
			transport, command := chooseNotifyTransport(tt.mode, tt.goos, getenv, tt.lookPath)
			if transport != tt.wantTransport || command != tt.wantCommand {
				t.Fatalf("chooseNotifyTransport() = %q, %q; want %q, %q", transport, command, tt.wantTransport, tt.wantCommand)
			}
		})
	}
}

func TestNativeNotifyArgs(t *testing.T) {
	t.Parallel()

	mac := nativeNotifyArgs("darwin", "jailoc / web", "a<b>&c")
	wantMac := []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "jailoc / web", "a<b>&c"}
	if !slices.Equal(mac, wantMac) {
		t.Fatalf("darwin args = %q, want %q", mac, wantMac)
	}
	linux := nativeNotifyArgs("linux", "jailoc / web", "a<b>&c")
	wantLinux := []string{"--app-name=jailoc", "--", "jailoc / web", "a&lt;b&gt;&amp;c"}
	if !slices.Equal(linux, wantLinux) {
		t.Fatalf("linux args = %q, want %q", linux, wantLinux)
	}
}

func TestNotifyMessage(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		kind     notifyKind
		wantBody string
	}{
		{notifyCompleted, "The agent finished."},
		{notifyInput, "The agent needs your input."},
	} {
		title, body := notifyMessage("web", tt.kind)
		if title != "jailoc / web" || body != tt.wantBody {
			t.Fatalf("notifyMessage(%v) = %q, %q; want %q, %q", tt.kind, title, body, "jailoc / web", tt.wantBody)
		}
	}
}

func TestOSCNotification(t *testing.T) {
	t.Parallel()

	if got := string(oscNotification("jailoc / web", "a;b\x07\x1bc", false)); got != "\x1b]777;notify;jailoc / web;a,bc\x07" {
		t.Fatalf("oscNotification() = %q", got)
	}
	if got := string(oscNotification("t", "b", true)); got != "\x1bPtmux;\x1b\x1b]777;notify;t;b\x07\x1b\\" {
		t.Fatalf("oscNotification(tmux) = %q", got)
	}
}

// recorder records the notifications a notifier delivers.
type recorder struct {
	mu    sync.Mutex
	kinds []notifyKind
	at    []time.Time
}

func (r *recorder) deliver(_ context.Context, k notifyKind, _ func() bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds = append(r.kinds, k)
	r.at = append(r.at, time.Now())
	return nil
}

func (r *recorder) got() []notifyKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.kinds)
}

func awayFocus() *sessionFocus {
	f := &sessionFocus{}
	f.set(focusOut)
	return f
}

func TestNotifierDeliversOnlyWhileAway(t *testing.T) {
	t.Parallel()

	for _, state := range []focusState{focusUnknown, focusIn, focusOut} {
		synctest.Test(t, func(t *testing.T) {
			var r recorder
			f := &sessionFocus{}
			f.set(state)
			n := newNotifier(notifyViaOSC, r.deliver, f, nil)
			n.post(notifyCompleted)
			synctest.Wait()
			n.stop()
			want := []notifyKind(nil)
			if state == focusOut {
				want = []notifyKind{notifyCompleted}
			}
			if got := r.got(); !slices.Equal(got, want) {
				t.Fatalf("focus %v: delivered %v, want %v", state, got, want)
			}
		})
	}
}

func TestNotifierRateLimitsAndDeduplicates(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var r recorder
		n := newNotifier(notifyViaOSC, r.deliver, awayFocus(), nil)
		n.post(notifyCompleted)
		synctest.Wait()
		n.post(notifyInput)
		n.post(notifyInput)
		n.post(notifyCompleted)
		synctest.Wait()
		if got := r.got(); !slices.Equal(got, []notifyKind{notifyCompleted}) {
			t.Fatalf("delivered %v before the interval, want only the first", got)
		}
		time.Sleep(2 * notifyInterval)
		synctest.Wait()
		n.stop()
		if got := r.got(); !slices.Equal(got, []notifyKind{notifyCompleted, notifyInput, notifyCompleted}) {
			t.Fatalf("delivered %v, want completed, input, completed", got)
		}
		for i := 1; i < len(r.at); i++ {
			if d := r.at[i].Sub(r.at[i-1]); d < notifyInterval {
				t.Fatalf("deliveries %d and %d %v apart, want at least %v", i-1, i, d, notifyInterval)
			}
		}
	})
}

func TestNotifierDropsQueuedAfterRefocus(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var r recorder
		f := awayFocus()
		n := newNotifier(notifyViaOSC, r.deliver, f, nil)
		n.post(notifyCompleted)
		synctest.Wait()
		n.post(notifyInput)
		f.set(focusIn)
		f.set(focusOut)
		time.Sleep(2 * notifyInterval)
		synctest.Wait()
		n.stop()
		if got := r.got(); !slices.Equal(got, []notifyKind{notifyCompleted}) {
			t.Fatalf("delivered %v, want the input notification queued before the refocus dropped", got)
		}
	})
}

func TestNotifierStopCancelsDelivery(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		canceled := make(chan error, 1)
		deliver := func(ctx context.Context, _ notifyKind, _ func() bool) error {
			<-ctx.Done()
			canceled <- ctx.Err()
			return ctx.Err()
		}
		n := newNotifier(notifyViaNative, deliver, awayFocus(), nil)
		n.post(notifyCompleted)
		synctest.Wait()
		n.stop()
		if err := <-canceled; !errors.Is(err, context.Canceled) {
			t.Fatalf("delivery context ended with %v, want context.Canceled", err)
		}
		n.post(notifyInput) // must not block or panic after stop
	})
}

func TestNotifierDeliveryTimesOut(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var r recorder
		calls := 0
		deliver := func(ctx context.Context, k notifyKind, current func() bool) error {
			calls++
			if calls == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return r.deliver(ctx, k, current)
		}
		n := newNotifier(notifyViaNative, deliver, awayFocus(), nil)
		n.post(notifyCompleted)
		synctest.Wait()
		n.post(notifyInput)
		time.Sleep(notifyTimeout + notifyInterval)
		synctest.Wait()
		n.stop()
		if got := r.got(); !slices.Equal(got, []notifyKind{notifyInput}) {
			t.Fatalf("delivered %v after a hung delivery, want input", got)
		}
	})
}

func TestNotifierDrain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		bound    time.Duration
		want     []notifyKind
		wantTook time.Duration
	}{
		{name: "delivers pending", bound: 2 * notifyInterval, want: []notifyKind{notifyCompleted, notifyInput}, wantTook: notifyInterval},
		{name: "bounded", bound: notifyInterval / 2, want: []notifyKind{notifyCompleted}, wantTook: notifyInterval / 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				var r recorder
				n := newNotifier(notifyViaOSC, r.deliver, awayFocus(), nil)
				n.post(notifyCompleted)
				n.post(notifyInput)
				start := time.Now()
				n.drain(tt.bound)
				if took := time.Since(start); took != tt.wantTook {
					t.Fatalf("drain took %v, want %v", took, tt.wantTook)
				}
				if got := r.got(); !slices.Equal(got, tt.want) {
					t.Fatalf("delivered %v, want %v", got, tt.want)
				}
				n.post(notifyCompleted)
			})
		})
	}
}

func TestNotifierStopIsBounded(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		deliver := func(context.Context, notifyKind, func() bool) error {
			<-release // ignores the cancellation
			return nil
		}
		ended := make(chan struct{})
		n := newNotifier(notifyViaNative, deliver, awayFocus(), func() { close(ended) })
		n.post(notifyCompleted)
		synctest.Wait()
		start := time.Now()
		n.stop()
		if took := time.Since(start); took != notifyStopWait {
			t.Fatalf("stop took %v with a delivery ignoring its cancellation, want %v", took, notifyStopWait)
		}
		select {
		case <-ended:
			t.Fatal("done ran while the delivery still runs")
		default:
		}
		close(release)
		<-ended
	})
}

func TestNotifierRunsDoneOnStop(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var r recorder
		ran := false
		n := newNotifier(notifyViaOSC, r.deliver, awayFocus(), func() { ran = true })
		n.stop()
		if !ran {
			t.Fatal("done did not run when the notifier stopped")
		}
	})
}

func TestNotifierRevalidatesFocusAtDelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(f *sessionFocus, cancel func())
		want   bool
	}{
		{name: "unchanged", change: func(*sessionFocus, func()) {}, want: true},
		{name: "focused", change: func(f *sessionFocus, _ func()) { f.set(focusIn) }, want: false},
		{name: "focused and away again", change: func(f *sessionFocus, _ func()) { f.set(focusIn); f.set(focusOut) }, want: false},
		{name: "unknown", change: func(f *sessionFocus, _ func()) { f.set(focusUnknown) }, want: false},
		{name: "stopping", change: func(_ *sessionFocus, cancel func()) { cancel() }, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				f := awayFocus()
				got := make(chan bool, 1)
				var n *notifier
				deliver := func(_ context.Context, _ notifyKind, current func() bool) error {
					tt.change(f, n.cancel)
					got <- current()
					return nil
				}
				n = newNotifier(notifyViaOSC, deliver, f, nil)
				n.post(notifyInput)
				if g := <-got; g != tt.want {
					t.Fatalf("current() at delivery = %v, want %v", g, tt.want)
				}
				n.stop()
			})
		})
	}
}
