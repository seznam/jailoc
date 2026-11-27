// Package browser selects and launches the host OS URL opener.
package browser

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

const (
	// maxLive bounds the opener children that may be alive at once.
	maxLive = 8
	// launchGrace is how long OpenURL waits for an opener to exit before it
	// reports success and leaves the still-running child alone.
	launchGrace = 2 * time.Second
)

// Command returns the executable that opens URLs on goos: the fixed
// /usr/bin/open on macOS, xdg-open from PATH on Linux. No shell, no $BROWSER.
func Command(goos string, lookPath func(string) (string, error)) (string, error) {
	switch goos {
	case "darwin":
		return "/usr/bin/open", nil
	case "linux":
		path, err := lookPath("xdg-open")
		if err != nil {
			return "", errors.New("xdg-open was not found in PATH")
		}
		return path, nil
	}
	return "", fmt.Errorf("opening a browser is not supported on %s", goos)
}

// Launcher starts the host opener automatically with a bounded number of live
// children. Create it with NewLauncher.
type Launcher struct {
	slots   chan struct{}
	grace   time.Duration
	command func() (string, error)
}

// NewLauncher returns a Launcher for this host with room for 8 live openers.
func NewLauncher() *Launcher {
	return &Launcher{
		slots:   make(chan struct{}, maxLive),
		grace:   launchGrace,
		command: func() (string, error) { return Command(runtime.GOOS, exec.LookPath) },
	}
}

// OpenURL starts the opener with rawURL as its only argument and waits up to
// two seconds for it. A clean exit within that time, or a child still running
// after it, returns nil: the latter does not mean the sign-in completed. The
// child is never killed, because xdg-open may stay in the foreground for the
// whole browser lifetime; its slot is released only when the child has exited
// and been reaped. rawURL must be validated by the caller. Errors never carry
// the URL.
func (l *Launcher) OpenURL(ctx context.Context, rawURL string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("open the browser: %w", err)
	}
	opener, err := l.command()
	if err != nil {
		return err
	}
	return l.launch(ctx, opener, rawURL)
}

func (l *Launcher) launch(ctx context.Context, opener, rawURL string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("open the browser: %w", err)
	}
	select {
	case l.slots <- struct{}{}:
	default:
		return errors.New("too many browser openers are still running")
	}
	if err := ctx.Err(); err != nil {
		<-l.slots
		return fmt.Errorf("open the browser: %w", err)
	}

	c := exec.Command(opener, rawURL) //nolint:gosec // G204: fixed opener, the URL is one validated argument without a shell
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	if err := ctx.Err(); err != nil {
		<-l.slots
		return fmt.Errorf("open the browser: %w", err)
	}
	if err := c.Start(); err != nil {
		<-l.slots
		return errors.New("the browser opener cannot be started")
	}

	// One waiter per live child reaps it and frees the slot only on exit.
	result := make(chan error, 1)
	go func() {
		err := c.Wait()
		<-l.slots
		result <- err
	}()

	timer := time.NewTimer(l.grace)
	defer timer.Stop()
	select {
	case err := <-result:
		if err != nil {
			return errors.New("the browser opener failed")
		}
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for the browser opener: %w", ctx.Err())
	}
}
