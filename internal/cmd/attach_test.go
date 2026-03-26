package cmd

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMonitorAttach(t *testing.T) {
	t.Parallel()

	t.Run("cancels when container stops", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "", nil
		}, "original", time.Millisecond)

		cause := waitForCause(t, ctx)
		if cause == nil || !strings.Contains(cause.Error(), "stopped") {
			t.Fatalf("got cause %v, want stopped error", cause)
		}
	})

	t.Run("cancels when container restarts", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "replacement", nil
		}, "original", time.Millisecond)

		cause := waitForCause(t, ctx)
		if cause == nil || !strings.Contains(cause.Error(), "restarted") {
			t.Fatalf("got cause %v, want restarted error", cause)
		}
	})

	t.Run("cancels when container lookup fails", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)

		go monitorAttach(ctx, cancel, func(context.Context) (string, error) {
			return "", errors.New("boom")
		}, "original", time.Millisecond)

		cause := waitForCause(t, ctx)
		if cause == nil || !strings.Contains(cause.Error(), "monitor opencode container") {
			t.Fatalf("got cause %v, want monitor error", cause)
		}
	})
}

func TestAttachResultPrefersWatchdogCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("opencode container restarted during attach"))

	err := attachResult(ctx, nil)
	if err == nil {
		t.Fatal("expected watchdog cause, got nil")
	}
	if !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("got error %q, want restarted cause", err)
	}
}

func TestRunCommandWithContextReturnsExitError(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", "exit 7") //nolint:gosec // static test command
	err := runCommandWithContext(context.Background(), cmd, nil, 0)
	if err == nil {
		t.Fatal("expected exit error, got nil")
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("got %T, want *exec.ExitError", err)
	}
}

func TestRunCommandWithContextCancelsCommand(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command("sh", "-c", "sleep 10") //nolint:gosec // static test command
	terminate := func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := runCommandWithContext(ctx, cmd, terminate, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !strings.Contains(err.Error(), "signal: terminated") {
		t.Fatalf("got error %q, want terminated signal", err)
	}
}

func waitForCause(t *testing.T, ctx context.Context) error {
	t.Helper()

	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for context cancellation")
		return nil
	}
}
