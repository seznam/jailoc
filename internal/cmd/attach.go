package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/password"
	"github.com/seznam/jailoc/internal/workspace"
)

func attachHostArgs(serverURL, password, dir, session string, cont bool) []string {
	args := []string{"attach", serverURL}
	if password != "" {
		args = append(args, "--password", password)
	}
	if dir != "" {
		args = append(args, "--dir", dir)
	}
	if session != "" {
		args = append(args, "--session", session)
	}
	if cont {
		args = append(args, "--continue")
	}
	return args
}

func attachExecArgs(serverURL, dir, session string, cont bool) []string {
	args := []string{"opencode", "attach", serverURL}
	if dir != "" {
		args = append(args, "--dir", dir)
	}
	if session != "" {
		args = append(args, "--session", session)
	}
	if cont {
		args = append(args, "--continue")
	}
	return args
}

// hostTUIConfigEnv returns OPENCODE_TUI_CONFIG env var entries for host attach
// when jailoc's generated tui.json should be used as a fallback. It does not
// override an explicit env var, and it does not point OpenCode at a missing
// generated config file.
func hostTUIConfigEnv(configPath string) []string {
	if os.Getenv("OPENCODE_TUI_CONFIG") != "" {
		return nil
	}
	if _, err := os.Stat(configPath); err != nil {
		return nil
	}

	ocDir, err := openCodeConfigDir()
	if err != nil {
		return nil
	}
	userTUI := filepath.Join(ocDir, "tui.json")
	if _, err := os.Stat(userTUI); err == nil {
		return nil // user has their own tui.json — don't override
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil // unreadable or other filesystem error — don't override
	}
	return []string{"OPENCODE_TUI_CONFIG=" + configPath}
}

// openCodeConfigDir returns the directory OpenCode uses for its config:
// $XDG_CONFIG_HOME/opencode if set, otherwise $HOME/.config/opencode.
// This matches OpenCode's own resolution regardless of platform (os.UserConfigDir
// returns ~/Library/Application Support on macOS, which OpenCode does not use).
func openCodeConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func execTUIConfigEnv(configPath string) []string {
	return []string{"OPENCODE_TUI_CONFIG=" + configPath}
}

// terminalEnv returns host terminal environment variables (TERM, COLORTERM)
// that should be forwarded to the container exec session for proper color support.
func terminalEnv() []string {
	var env []string
	for _, key := range []string{"TERM", "COLORTERM", "CLICOLOR_FORCE", "TERM_PROGRAM", "COLORFGBG", "LANG"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	if os.Getenv("CLICOLOR_FORCE") == "" {
		env = append(env, "CLICOLOR_FORCE=1")
	}
	// Provide COLORFGBG fallback so termenv skips OSC terminal queries for
	// foreground/background color detection (which timeout through Docker exec PTY).
	if os.Getenv("COLORFGBG") == "" {
		env = append(env, "COLORFGBG=15;0")
	}
	// Default COLORTERM so OpenCode renders with full 24-bit color palette.
	// Many terminals (iTerm2, Alacritty, kitty) set this, but Terminal.app and
	// Docker exec environments often don't.
	if os.Getenv("COLORTERM") == "" {
		env = append(env, "COLORTERM=truecolor")
	}
	// Ensure UTF-8 locale reaches the container so TUI box-drawing characters
	// and icons render correctly (macOS Terminal.app sets LANG on the host but
	// Docker exec doesn't inherit it without explicit forwarding).
	if os.Getenv("LANG") == "" {
		env = append(env, "LANG=C.UTF-8")
	}
	return env
}

func envWithOverrides(base []string, overrides ...string) []string {
	if len(overrides) == 0 {
		return append([]string{}, base...)
	}

	overrideKeys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		if key, _, ok := strings.Cut(entry, "="); ok && key != "" {
			overrideKeys[key] = struct{}{}
		}
	}

	filtered := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		if key, _, ok := strings.Cut(entry, "="); ok {
			if _, exists := overrideKeys[key]; exists {
				continue
			}
		}
		filtered = append(filtered, entry)
	}

	return append(filtered, overrides...)
}

func attachOnHost(ctx context.Context, ws *workspace.Resolved, dir string, passwordMode string, session string, cont bool) error {
	binary, err := config.ResolveBinary()
	if err != nil {
		return fmt.Errorf("resolve opencode binary: %w", err)
	}

	serverArg := fmt.Sprintf("http://localhost:%d", ws.Port)
	interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // G115: uintptr→int is safe for file descriptors
	resolver := password.DefaultResolver(interactive, passwordMode)
	pw, _, err := resolver.Resolve(ws.Name)
	if err != nil {
		return err
	}
	args := attachHostArgs(serverArg, pw, dir, session, cont)

	tuiPath := filepath.Join(jailocCacheDir(), "tui.json")
	env := envWithOverrides(os.Environ(),
		"JAILOC=1",
		"JAILOC_WORKSPACE="+ws.Name,
	)
	if tuiEnv := hostTUIConfigEnv(tuiPath); len(tuiEnv) > 0 {
		env = envWithOverrides(env, tuiEnv...)
	}

	// PTY keeps isTTY=true for the child (required by opentui) while letting
	// us intercept stdout through the exitRewriter.
	p, err := gopty.New()
	if err != nil {
		if errors.Is(err, gopty.ErrUnsupported) {
			return errPTYUnsupported
		}
		return fmt.Errorf("create pty: %w", err)
	}
	defer func() {
		_ = p.Close()
	}()

	// Set initial size BEFORE starting — fixes race where child reads
	// default PTY dimensions at startup.
	fd := int(os.Stdin.Fd()) //nolint:gosec // Fd() fits in int on all supported platforms
	if term.IsTerminal(fd) {
		if w, h, sizeErr := term.GetSize(fd); sizeErr == nil {
			_ = p.Resize(w, h)
		}
	}

	cmd := p.Command(binary, args...)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command with pty: %w", err)
	}

	waitDone := make(chan struct{})
	closeWaitDone := sync.OnceFunc(func() { close(waitDone) })
	defer closeWaitDone()

	if sigWinch != nil {
		// Forward terminal resizes to the PTY.
		sigCh := make(chan os.Signal, 1)
		sigDone := make(chan struct{})
		signal.Notify(sigCh, sigWinch)
		go func() {
			defer close(sigDone)
			for {
				select {
				case <-sigCh:
					if w, h, err := term.GetSize(fd); err == nil {
						_ = p.Resize(w, h)
					}
				case <-ctx.Done():
					return
				case <-waitDone:
					return
				}
			}
		}()
		defer func() {
			signal.Stop(sigCh)
			closeWaitDone()
			<-sigDone
		}()
	} else if term.IsTerminal(fd) {
		// No SIGWINCH (Windows): poll for terminal size changes.
		pollDone := make(chan struct{})
		go func() {
			defer close(pollDone)
			var lastW, lastH int
			ticker := time.NewTicker(resizePollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if w, h, err := term.GetSize(fd); err == nil && (w != lastW || h != lastH) {
						_ = p.Resize(w, h)
						lastW, lastH = w, h
					}
				case <-ctx.Done():
					return
				case <-waitDone:
					return
				}
			}
		}()
		defer func() {
			closeWaitDone()
			<-pollDone
		}()
	}

	// Raw mode so keystrokes pass through verbatim (Ctrl-C, arrows, etc.).
	if term.IsTerminal(fd) {
		oldState, rawErr := term.MakeRaw(fd)
		if rawErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("set raw terminal: %w", rawErr)
		}
		defer func() { _ = term.Restore(fd, oldState) }()
	}

	go func() { _, _ = io.Copy(p, os.Stdin) }()

	// Cancel the child process when the context is done (e.g. container stops).
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Signal(terminateSignal)
				select {
				case <-waitDone:
				case <-time.After(attachWaitDelay):
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
				}
			}
		case <-waitDone:
		}
	}()

	rw := &exitRewriter{w: os.Stdout}
	_, copyErr := io.Copy(rw, p)

	// Close the PTY so the child receives SIGHUP if it's still running.
	_ = p.Close()

	err = cmd.Wait()
	closeWaitDone()
	// PTY reads commonly return EIO when the slave side closes on normal
	// process exit — treat it as expected EOF. On Windows, pipe close may
	// surface as os.ErrClosed. Surface other copy errors only when the
	// process itself exited cleanly.
	if copyErr != nil && err == nil && !isExpectedPTYClose(copyErr) {
		err = fmt.Errorf("copy pty output: %w", copyErr)
	}
	if ferr := rw.Flush(); ferr != nil && err == nil {
		err = fmt.Errorf("flush exit rewriter: %w", ferr)
	}
	return attachResult(ctx, err)
}

func attachExec(ctx context.Context, client *docker.Client, dir string, session string, cont bool) error {
	containerID, err := client.CurrentContainerID(ctx)
	if err != nil {
		return fmt.Errorf("resolve opencode container for exec: %w", err)
	}
	if containerID == "" {
		return fmt.Errorf("workspace is not running; run 'jailoc up' first")
	}

	fd := int(os.Stdin.Fd()) //nolint:gosec // Fd() fits in int on all supported platforms
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("set raw terminal: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	resizeCh := make(chan docker.TerminalSize, 1)
	defer close(resizeCh)

	if w, h, sizeErr := term.GetSize(fd); sizeErr == nil {
		resizeCh <- docker.TerminalSize{Width: uint(w), Height: uint(h)} //nolint:gosec // terminal dimensions are always positive
	}

	done := make(chan struct{})
	if sigWinch != nil {
		sigCh := make(chan os.Signal, 1)
		sigDone := make(chan struct{})
		signal.Notify(sigCh, sigWinch)
		go func() {
			defer close(sigDone)
			for {
				select {
				case <-sigCh:
					if w, h, err := term.GetSize(fd); err == nil {
						size := docker.TerminalSize{Width: uint(w), Height: uint(h)} //nolint:gosec // terminal dimensions are always positive
						select {
						case resizeCh <- size:
						default:
							<-resizeCh
							resizeCh <- size
						}
					}
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}()
		defer func() {
			signal.Stop(sigCh)
			close(done)
			<-sigDone
		}()
	} else if term.IsTerminal(fd) {
		pollDone := make(chan struct{})
		go func() {
			defer close(pollDone)
			var lastW, lastH int
			ticker := time.NewTicker(resizePollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if w, h, err := term.GetSize(fd); err == nil && (w != lastW || h != lastH) {
						size := docker.TerminalSize{Width: uint(w), Height: uint(h)} //nolint:gosec // terminal dimensions are always positive
						select {
						case resizeCh <- size:
						default:
							<-resizeCh
							resizeCh <- size
						}
						lastW, lastH = w, h
					}
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}()
		defer func() {
			close(done)
			<-pollDone
		}()
	}

	serverURL := fmt.Sprintf("http://localhost:%d", workspace.BasePort)
	rw := &exitRewriter{w: os.Stdout}
	execEnv := append(execTUIConfigEnv("/etc/jailoc-tui.json"), terminalEnv()...)
	err = client.ExecInteractive(ctx, containerID, attachExecArgs(serverURL, dir, session, cont), execEnv, os.Stdin, rw, resizeCh)
	if ferr := rw.Flush(); ferr != nil && err == nil {
		err = fmt.Errorf("flush exit rewriter: %w", ferr)
	}
	return attachResult(ctx, err)
}

const (
	attachPollInterval  = 500 * time.Millisecond
	attachWaitDelay     = 2 * time.Second
	resizePollInterval  = 250 * time.Millisecond
)

var errUnhealthy = errors.New("opencode process unhealthy inside container")
var errPTYUnsupported = errors.New("PTY not supported on this platform")

func isExpectedPTYClose(err error) bool {
	return errors.Is(err, errEIO) || errors.Is(err, os.ErrClosed)
}

func startAttachWatch(parent context.Context, client *docker.Client, workspaceName string) (context.Context, func(), error) {
	containerID, err := client.CurrentContainerID(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve opencode container: %w", err)
	}
	if containerID == "" {
		return nil, nil, fmt.Errorf("workspace %q is not running; run 'jailoc up' first", workspaceName)
	}

	attachCtx, cancel := context.WithCancelCause(parent)
	go monitorAttach(attachCtx, cancel, client.CurrentContainerID, client.HealthStatus, containerID, attachPollInterval)

	return attachCtx, func() { cancel(nil) }, nil
}

func attachResult(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}

	return err
}

func monitorAttach(ctx context.Context, cancel context.CancelCauseFunc, currentContainerID func(context.Context) (string, error), healthStatus func(context.Context) (string, error), expectedContainerID string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			containerID, err := currentContainerID(ctx)
			if err != nil {
				cancel(fmt.Errorf("monitor opencode container: %w", err))
				return
			}
			if containerID == "" {
				cancel(fmt.Errorf("opencode container stopped during attach"))
				return
			}
			if containerID != expectedContainerID {
				cancel(fmt.Errorf("opencode container restarted during attach"))
				return
			}
			health, err := healthStatus(ctx)
			if err != nil {
				continue // transient health check failure — ignore
			}
			if health == "unhealthy" {
				cancel(errUnhealthy)
				return
			}
		}
	}
}

func runCommandWithContext(ctx context.Context, cmd *exec.Cmd, terminate func() error, waitDelay time.Duration) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command %q: %w", cmd.Path, err)
	}

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- cmd.Wait()
	}()

	select {
	case err := <-resultCh:
		return err
	case <-ctx.Done():
		if terminate != nil {
			if err := terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return fmt.Errorf("cancel command %q: %w", cmd.Path, err)
			}
		}

		if waitDelay <= 0 {
			return <-resultCh
		}

		select {
		case err := <-resultCh:
			return err
		case <-time.After(waitDelay):
			if cmd.Process != nil {
				if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					return fmt.Errorf("kill command %q: %w", cmd.Path, err)
				}
			}
			return <-resultCh
		}
	}
}

// writeAll writes the entire slice to w, retrying on short writes.
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		data = data[n:]
		if err != nil {
			return err
		}
	}
	return nil
}

// exitRewriter wraps an io.Writer and replaces occurrences of "opencode -s "
// with "jailoc -s " in the output stream. It handles partial matches that
// span Write boundaries by buffering a small suffix.
type exitRewriter struct {
	w   io.Writer
	buf []byte
}

var (
	exitMatch   = []byte("opencode -s ")
	exitReplace = []byte("jailoc -s ")
)

func (r *exitRewriter) Write(p []byte) (int, error) {
	data := append(r.buf, p...) //nolint:gocritic // append merges buf and p; may reuse buf's backing array
	r.buf = r.buf[:0]

	for {
		idx := bytes.Index(data, exitMatch)
		if idx >= 0 {
			if idx > 0 {
				if err := writeAll(r.w, data[:idx]); err != nil {
					return len(p), err
				}
			}
			if err := writeAll(r.w, exitReplace); err != nil {
				return len(p), err
			}
			data = data[idx+len(exitMatch):]
			continue
		}

		// Keep any suffix that could be the start of exitMatch.
		keep := 0
		for i := 1; i < len(exitMatch) && i <= len(data); i++ {
			if bytes.Equal(data[len(data)-i:], exitMatch[:i]) {
				keep = i
			}
		}

		flush := data[:len(data)-keep]
		if len(flush) > 0 {
			if err := writeAll(r.w, flush); err != nil {
				return len(p), err
			}
		}
		r.buf = append(r.buf[:0], data[len(data)-keep:]...)
		break
	}

	return len(p), nil
}

// Flush writes any buffered partial-match bytes to the underlying writer.
func (r *exitRewriter) Flush() error {
	if len(r.buf) > 0 {
		err := writeAll(r.w, r.buf)
		r.buf = r.buf[:0]
		return err
	}
	return nil
}
