//go:build darwin || linux

package cmd

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

// sessionNotify configures the notifications of an attached session. They
// are read on the host at every attach; the guest is not involved beyond the
// TUI plugin's reports in the agent output.
type sessionNotify struct {
	// plugin is set when the agent's TUI plugin reports with notifySequences,
	// which are then removed from its output whether or not they notify.
	plugin    bool
	transport string // notifyViaNative or notifyViaOSC; empty: no notifications
	command   string // the native notification command
	workspace string
}

// sessionNotifyFor returns the notifications of a session of ws. Only the
// OpenCode TUI plugins report, and only an interactive session notifies.
func sessionNotifyFor(ws *workspace.Resolved, interactive bool) sessionNotify {
	n := sessionNotify{
		plugin:    ws.Agent.TUIPlugin == agent.OpenCode1TUI || ws.Agent.TUIPlugin == agent.OpenCode2TUI,
		workspace: ws.Name,
	}
	if !n.plugin || !interactive {
		return n
	}
	n.transport, n.command = chooseNotifyTransport(ws.Notifications, runtime.GOOS, os.Getenv, exec.LookPath)
	if n.transport == "" && ws.Notifications == config.NotificationsNative {
		slog.Warn("session notifications off: the host cannot show native notifications", "workspace", ws.Name)
	}
	slog.Info("session notifications", "workspace", ws.Name, "mode", ws.Notifications, "transport", cmp.Or(n.transport, "off"))
	return n
}

// start turns on focus reporting in the terminal status writes to and
// starts the notifier. It returns nils when the session does not notify.
// Terminal notifications go through their own non-blocking file
// description of the terminal, between agent output chunks.
func (n sessionNotify) start(status *startupStatus) (*notifier, *focusInput, *focusOutput) {
	var deliver notifyDeliver
	var done func()
	switch n.transport {
	case notifyViaNative:
		deliver = func(ctx context.Context, k notifyKind, current func() bool) error {
			if !current() {
				return fmt.Errorf("%w: the terminal is focused again or the session ended", errNotifyDropped)
			}
			title, body := notifyMessage(n.workspace, k)
			return runNativeNotify(ctx, n.command, nativeNotifyArgs(runtime.GOOS, title, body))
		}
	case notifyViaOSC:
		tty, err := openNotifyTerminal(int(os.Stdout.Fd())) //nolint:gosec // G115: file descriptors fit in int
		if err != nil {
			slog.Warn("session notifications off", "workspace", n.workspace, "error", err)
			return nil, nil, nil
		}
		done = func() {
			if err := tty.close(); err != nil {
				slog.Warn("session notifications", "workspace", n.workspace, "error", err)
			}
		}
		tmux := os.Getenv("TMUX") != ""
		deliver = func(_ context.Context, k notifyKind, current func() bool) error {
			title, body := notifyMessage(n.workspace, k)
			return status.tryWriteBetween(oscNotification(title, body, tmux), tty.write, current)
		}
	default:
		return nil, nil, nil
	}
	if err := status.writeBetween([]byte(enableFocusReports)); err != nil {
		slog.Warn("session notifications off: enable focus reports", "workspace", n.workspace, "error", err)
		if done != nil {
			done()
		}
		return nil, nil, nil
	}
	focus := &sessionFocus{}
	return newNotifier(n.transport, deliver, focus, done), &focusInput{focus: focus}, &focusOutput{focus: focus}
}
