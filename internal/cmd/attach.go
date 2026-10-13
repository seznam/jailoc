package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
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

// openCodeClientArgs builds the OpenCode 2 CLI arguments that connect the TUI
// to an existing server. The working directory is positional and must exist on
// the machine running the CLI. The server password is passed via the
// OPENCODE_PASSWORD environment variable, never on the command line.
func openCodeClientArgs(serverURL, dir, session string, cont bool) []string {
	args := []string{"--server", serverURL}
	if session != "" {
		args = append(args, "--session", session)
	}
	if cont {
		args = append(args, "--continue")
	}
	if dir != "" {
		args = append(args, dir)
	}
	return args
}

func attachHostArgs(serverURL, dir, session string, cont bool) []string {
	return openCodeClientArgs(serverURL, dir, session, cont)
}

func attachExecArgs(serverURL, dir, session string, cont bool) []string {
	return append([]string{"opencode"}, openCodeClientArgs(serverURL, dir, session, cont)...)
}

// cliConfigContentEnv is the OpenCode 2 env var holding inline CLI config JSON.
// Its content is merged over the user's cli.json, but arrays (like "plugins")
// replace rather than merge.
const cliConfigContentEnv = "OPENCODE_CLI_CONFIG_CONTENT"

// hostPluginEnv returns an OPENCODE_CLI_CONFIG_CONTENT entry that adds the
// jailoc CLI plugin at pluginDir to the host-side OpenCode TUI. It returns nil
// when the plugin directory is missing; see cliPluginEnv for the merge rules.
func hostPluginEnv(pluginDir string) []string {
	if _, err := os.Stat(pluginDir); err != nil {
		return nil
	}
	ocDir, err := openCodeConfigDir()
	if err != nil {
		return nil
	}
	return cliPluginEnv("file://"+filepath.ToSlash(pluginDir), os.Getenv(cliConfigContentEnv), ocDir)
}

// defaultContainerOCConfigDir is where the OpenCode client inside the
// container looks for its configuration when XDG_CONFIG_HOME is unset.
const defaultContainerOCConfigDir = "/home/agent/.config/opencode"

// containerOCConfigDir returns the OpenCode config directory the in-container
// client uses, honoring an absolute XDG_CONFIG_HOME from the workspace env
// (KEY=VALUE entries; the last one wins, matching how they reach the container).
func containerOCConfigDir(env []string) string {
	xdg := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "XDG_CONFIG_HOME="); ok {
			xdg = value
		}
	}
	if !path.IsAbs(xdg) {
		return defaultContainerOCConfigDir
	}
	return path.Join(xdg, "opencode")
}

// execPluginEnv returns an OPENCODE_CLI_CONFIG_CONTENT entry that adds the
// jailoc CLI plugin bind-mounted into the container to the exec'd OpenCode
// TUI. OpenCode 2 does not discover CLI plugins from its system config
// directory, so the plugin must be configured explicitly. The user's plugins
// are read from the host directory the workspace mounts at the container's
// OpenCode config directory, so they match what the in-container client sees.
func execPluginEnv(mounts, env []string) []string {
	return cliPluginEnv("file://"+tuiPluginContainerDir, "", mountedHostPath(mounts, containerOCConfigDir(env)))
}

// mountedHostPath returns the host path backing containerPath through the
// deepest bind mount that covers it, or "" when no mount covers it.
func mountedHostPath(mounts []string, containerPath string) string {
	best, bestLen := "", -1
	for _, spec := range mounts {
		m, err := config.ParseMount(spec)
		if err != nil || m.Host == "" {
			continue
		}
		container := path.Clean(m.Container)
		if containerPath != container && !strings.HasPrefix(containerPath, strings.TrimSuffix(container, "/")+"/") {
			continue
		}
		if len(container) <= bestLen {
			continue
		}
		rel := strings.TrimPrefix(containerPath, container)
		best, bestLen = filepath.Join(m.Host, filepath.FromSlash(rel)), len(container)
	}
	return best
}

// cliPluginEnv returns an OPENCODE_CLI_CONFIG_CONTENT entry that adds the
// plugin spec to the OpenCode TUI. Because the inline config replaces the
// "plugins" array, the user's existing plugins (from inherited inline config,
// or else from cli.json in ocDir) are carried over; an empty ocDir means there
// is no user config. It returns nil whenever injecting could drop or misread
// user configuration: unparseable config, or a legacy tui.json that OpenCode
// has not yet migrated to cli.json.
func cliPluginEnv(spec, inherited, ocDir string) []string {
	if strings.TrimSpace(inherited) != "" {
		merged, err := withPlugin([]byte(inherited), spec)
		if err != nil {
			return nil
		}
		return []string{cliConfigContentEnv + "=" + string(merged)}
	}

	var userCfg []byte
	if ocDir != "" {
		data, err := os.ReadFile(filepath.Join(ocDir, "cli.json")) //nolint:gosec // G304: path is the user's own OpenCode config
		switch {
		case err == nil:
			userCfg = data
		case errors.Is(err, os.ErrNotExist):
			if legacyTUIConfigExists(ocDir) {
				return nil
			}
		default:
			return nil
		}
	}

	plugins, err := pluginsFromConfig(userCfg)
	if err != nil {
		return nil
	}
	content, err := json.Marshal(map[string][]json.RawMessage{"plugins": appendPlugin(plugins, spec)})
	if err != nil {
		return nil
	}
	return []string{cliConfigContentEnv + "=" + string(content)}
}

// legacyTUIConfigExists reports whether an OpenCode 1 tui.json(c) is present.
// OpenCode 2 migrates it into cli.json on first start; injecting inline config
// before that would hide the migrated plugin list.
func legacyTUIConfigExists(ocDir string) bool {
	for _, name := range []string{"tui.json", "tui.jsonc", "cli.jsonc"} {
		if _, err := os.Stat(filepath.Join(ocDir, name)); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

// withPlugin adds spec to the "plugins" array of an inline CLI config object,
// preserving every other key.
func withPlugin(raw []byte, spec string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("parse %s: %w", cliConfigContentEnv, err)
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	plugins, err := pluginsFromConfig(raw)
	if err != nil {
		return nil, err
	}
	list, err := json.Marshal(appendPlugin(plugins, spec))
	if err != nil {
		return nil, fmt.Errorf("marshal plugins: %w", err)
	}
	obj["plugins"] = list
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", cliConfigContentEnv, err)
	}
	return out, nil
}

// pluginsFromConfig extracts the raw "plugins" entries from a CLI config
// document. Empty input yields no plugins.
func pluginsFromConfig(raw []byte) ([]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var cfg struct {
		Plugins []json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse cli config: %w", err)
	}
	return cfg.Plugins, nil
}

func appendPlugin(plugins []json.RawMessage, spec string) []json.RawMessage {
	for _, p := range plugins {
		var s string
		if json.Unmarshal(p, &s) == nil && s == spec {
			return plugins
		}
	}
	quoted, _ := json.Marshal(spec) //nolint:errchkjson // marshaling a string cannot fail
	return append(append([]json.RawMessage{}, plugins...), quoted)
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
	args := attachHostArgs(serverArg, dir, session, cont)

	overrides := []string{
		"JAILOC=1",
		"JAILOC_WORKSPACE=" + ws.Name,
	}
	if pw != "" {
		// OpenCode 2 clients read either variable; set both so a stale value
		// inherited from the user's shell cannot win.
		overrides = append(overrides, "OPENCODE_PASSWORD="+pw, "OPENCODE_SERVER_PASSWORD="+pw)
	}
	overrides = append(overrides, hostPluginEnv(filepath.Join(jailocCacheDir(), tuiPluginDirName))...)
	env := envWithOverrides(os.Environ(), overrides...)

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

	// Close the parent's copy of the PTY slave fd. The child has already dup'd
	// it onto stdin/stdout/stderr via fork-exec; keeping our copy open would
	// prevent the master from ever returning EOF/EIO when the child exits,
	// causing io.Copy(rw, p) below to block forever (e.g. after the user
	// terminates opencode from inside the TUI).
	if unixP, ok := p.(gopty.UnixPty); ok {
		if err := unixP.Slave().Close(); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("close pty slave: %w", err)
		}
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

func attachExec(ctx context.Context, client *docker.Client, ws *workspace.Resolved, dir string, session string, cont bool) error {
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
							select {
							case <-resizeCh:
							default:
							}
							select {
							case resizeCh <- size:
							default:
							}
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
							select {
							case <-resizeCh:
							default:
							}
							select {
							case resizeCh <- size:
							default:
							}
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
	execEnv := append(execPluginEnv(ws.Mounts, ws.Env), terminalEnv()...)
	err = client.ExecInteractive(ctx, containerID, attachExecArgs(serverURL, dir, session, cont), execEnv, os.Stdin, rw, resizeCh)
	if ferr := rw.Flush(); ferr != nil && err == nil {
		err = fmt.Errorf("flush exit rewriter: %w", ferr)
	}
	return attachResult(ctx, err)
}

const (
	attachPollInterval = 500 * time.Millisecond
	attachWaitDelay    = 2 * time.Second
	resizePollInterval = 250 * time.Millisecond
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
