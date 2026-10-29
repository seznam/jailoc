//go:build darwin || linux

package cmd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/guest/session"
	"github.com/seznam/jailoc/internal/guestbin"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// detachMatcher finds the detach key sequence in terminal input that may
// arrive split across reads. Input that only starts like the sequence is
// held back until the next key decides. As in tmux, typing the first key
// twice sends it once to the agent.
type detachMatcher struct {
	keys []byte
	held int
}

// feed returns the input to forward to the agent and whether the sequence
// was completed; input after a completed sequence is dropped.
func (m *detachMatcher) feed(chunk []byte) (out []byte, detach bool) {
	for _, b := range chunk {
		switch {
		case b == m.keys[m.held]:
			m.held++
			if m.held == len(m.keys) {
				m.held = 0
				return out, true
			}
		case m.held == 1 && b == m.keys[0]:
			out = append(out, b)
			m.held = 0
		default:
			out = append(out, m.keys[:m.held]...)
			m.held = 0
			if b == m.keys[0] && len(m.keys) > 1 {
				m.held = 1
			} else {
				out = append(out, b)
			}
		}
	}
	return out, false
}

type sessionOutcome int

const (
	outcomeLost sessionOutcome = iota
	outcomeExited
	outcomeDetached
	outcomeKicked
)

// runSessionVM starts the workspace if needed and attaches the terminal to
// a detachable agent session in the VM: the one started in the same
// directory, or a new one. Closing the terminal or typing the detach keys
// detaches and leaves the agent running.
func runSessionVM(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, opts vmSessionOptions) error {
	if st, err := runningVM(vmStateDir(ws.Name)); err != nil {
		return err
	} else if st == nil {
		workspaceExplicit = true
		if err := runUpVM(ctx, cfg, ws); err != nil {
			return err
		}
	} else if err := checkGuestCurrent(ws.Name); err != nil {
		return err
	} else if err := checkAgentCurrent(ws); err != nil {
		return err
	} else if err := ensureSecrets(ctx, ws); err != nil {
		return err
	}
	client, err := vm.DialSSH(ctx, vmStateDir(ws.Name))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if opts.attach == "" {
		if err := checkAgentBinary(client, ws); err != nil {
			return err
		}
	}
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	fd := int(os.Stdin.Fd()) //nolint:gosec // G115: file descriptors fit in int
	interactive := term.IsTerminal(fd)
	rows, cols := 24, 80
	if interactive {
		if w, h, err := term.GetSize(fd); err == nil {
			rows, cols = h, w
		}
	}

	cmdline := []string{guest.BinaryPath, "session", "open", "--rows", strconv.Itoa(rows), "--cols", strconv.Itoa(cols), "-C", opts.dir}
	if opts.attach != "" {
		cmdline = append(cmdline, "--id", opts.attach)
	} else if opts.newAgent || opts.session != "" || opts.cont {
		cmdline = append(cmdline, "--new")
	}
	env := terminalEnv()
	if os.Getenv("TERM") == "" {
		env = append(env, "TERM=xterm-256color")
	}
	for _, kv := range env {
		cmdline = append(cmdline, "-e", kv)
	}
	cmdline = append(append(cmdline, "--"), ws.Agent.Command(ws.AgentArgs, opts.session, opts.cont)...)

	stdin, err := sess.StdinPipe()
	if err != nil {
		return fmt.Errorf("open session input: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open session output: %w", err)
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	if err := sess.Start(shellJoin(cmdline)); err != nil {
		return fmt.Errorf("start session in workspace %q: %w", ws.Name, err)
	}

	id, outcome, code := relaySession(fd, interactive, opts.detach, stdin, stdout, uint16(min(rows, 0xffff)), uint16(min(cols, 0xffff))) //nolint:gosec // G115: clamped
	// The guest command exits only after its input closes.
	_ = stdin.Close()
	_ = sess.Wait()

	switch outcome {
	case outcomeExited:
		if code != 0 {
			return fmt.Errorf("%s exited with status %d", ws.Agent.Binary, code)
		}
		return nil
	case outcomeDetached:
		_, _ = color.New(color.FgCyan).Printf("Detached from session %s; the agent keeps running. Run jailoc in %s to re-attach.\n", id, opts.dir)
		return nil
	case outcomeKicked:
		_, _ = color.New(color.FgYellow).Printf("Session %s was attached from another terminal.\n", id)
		return nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("session in workspace %q: %s", ws.Name, msg)
	}
	if id != "" {
		return fmt.Errorf("connection to workspace %q lost; if the VM still runs, session %s keeps running — run jailoc to re-attach", ws.Name, id)
	}
	return fmt.Errorf("connection to workspace %q lost; the VM stopped or was stopped by `jailoc down`", ws.Name)
}

// relaySession connects the local terminal to the session protocol on
// in/out until the session ends, the user detaches or another terminal
// takes the session over.
func relaySession(fd int, interactive bool, detachKeys []byte, in io.WriteCloser, out io.Reader, rows, cols uint16) (id string, outcome sessionOutcome, code int) {
	var writeMu sync.Mutex
	send := func(f session.Frame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return session.WriteFrame(in, f)
	}
	if err := send(session.SizeFrame(session.FrameHello, rows, cols)); err != nil {
		return "", outcomeLost, 0
	}

	if interactive {
		old, err := term.MakeRaw(fd)
		if err == nil {
			defer func() {
				// The agent cannot turn its terminal features off when the
				// session detaches or the VM dies, so reset them here.
				if term.IsTerminal(int(os.Stdout.Fd())) { //nolint:gosec // G115: file descriptors fit in int
					resetTerminalModes(os.Stdout)
				}
				_ = term.Restore(fd, old)
			}()
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if w, h, err := term.GetSize(fd); err == nil {
					_ = send(session.SizeFrame(session.FrameResize, uint16(min(h, 0xffff)), uint16(min(w, 0xffff)))) //nolint:gosec // G115: clamped
				}
			}
		}()
	}

	detached := make(chan struct{})
	matcher := &detachMatcher{keys: detachKeys}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk, detach := append([]byte(nil), buf[:n]...), false
				if interactive && len(detachKeys) > 0 {
					chunk, detach = matcher.feed(chunk)
				}
				if len(chunk) > 0 && send(session.Frame{Type: session.FrameData, Payload: chunk}) != nil {
					return
				}
				if detach {
					close(detached)
					_ = in.Close()
					return
				}
			}
			if err != nil {
				_ = in.Close()
				return
			}
		}
	}()

	for {
		f, err := session.ReadFrame(out)
		if err != nil {
			select {
			case <-detached:
				return id, outcomeDetached, 0
			default:
				return id, outcomeLost, 0
			}
		}
		switch f.Type {
		case session.FrameInfo:
			id = string(f.Payload)
		case session.FrameData:
			_, _ = os.Stdout.Write(f.Payload)
		case session.FrameExit:
			code, _ = session.ParseExit(f.Payload)
			return id, outcomeExited, code
		case session.FrameKicked:
			return id, outcomeKicked, 0
		}
	}
}

// listSessionsVM prints the agent sessions running in the workspace VM.
func listSessionsVM(ctx context.Context, ws *workspace.Resolved) error {
	infos, err := vmSessions(ctx, ws)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Printf("No agent sessions in workspace %s\n", ws.Name)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATE\tSTARTED\tDIRECTORY\tCOMMAND")
	for _, info := range infos {
		state := "detached"
		if info.Attached {
			state = "attached"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", info.ID, state, info.Created.Local().Format(time.DateTime), info.Dir, info.Name)
	}
	return tw.Flush()
}

// stopSessionVM ends an agent session in the workspace VM.
func stopSessionVM(ctx context.Context, ws *workspace.Resolved, id string) error {
	if _, err := vmCommand(ctx, ws, []string{guest.BinaryPath, "session", "stop", id}); err != nil {
		return err
	}
	_, _ = color.New(color.FgGreen).Printf("Stopped session %s\n", id)
	return nil
}

func vmSessions(ctx context.Context, ws *workspace.Resolved) ([]session.Info, error) {
	out, err := vmCommand(ctx, ws, []string{guest.BinaryPath, "session", "list"})
	if err != nil {
		return nil, err
	}
	var infos []session.Info
	if err := json.Unmarshal(out, &infos); err != nil {
		return nil, fmt.Errorf("parse sessions of workspace %q: %w", ws.Name, err)
	}
	return infos, nil
}

// vmCommand runs a command in the running workspace VM and returns its
// output.
func vmCommand(ctx context.Context, ws *workspace.Resolved, cmdline []string) ([]byte, error) {
	st, err := runningVM(vmStateDir(ws.Name))
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("workspace %q is not running", ws.Name)
	}
	client, err := vm.DialSSH(ctx, vmStateDir(ws.Name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	if err := sess.Run(shellJoin(cmdline)); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s", strings.TrimPrefix(strings.TrimSpace(stderr.String()), "jailoc-guest: "))
		}
		return nil, fmt.Errorf("run in workspace %q: %w", ws.Name, err)
	}
	return stdout.Bytes(), nil
}

// checkGuestCurrent reports a running workspace VM that was started with a
// different jailoc-guest than the one embedded in this jailoc, for example
// after upgrading jailoc: the guest may not support the session protocol.
func checkGuestCurrent(name string) error {
	spec, err := vm.ReadSpec(vmStateDir(name))
	if err != nil || spec.GuestBinary == "" {
		return nil //nolint:nilerr // a missing spec is reported by the session itself
	}
	current, err := guestbin.Path(filepath.Join(vmRootDir(), "guest"))
	if err != nil {
		return nil //nolint:nilerr // no embedded guest: the runner-side guest is used as is
	}
	if filepath.Clean(spec.GuestBinary) == filepath.Clean(current) {
		return nil
	}
	return fmt.Errorf("workspace %q runs a jailoc-guest from another jailoc version; run `jailoc restart %s` to update it (this ends its running sessions)", name, name)
}

// checkAgentCurrent reports a running workspace VM that was started for
// another agent than the configured one: its image may lack the agent.
func checkAgentCurrent(ws *workspace.Resolved) error {
	spec, err := vm.ReadSpec(vmStateDir(ws.Name))
	if err != nil {
		return nil //nolint:nilerr // a missing spec is reported by the session itself
	}
	// Specs written before jailoc ran other agents than OpenCode name none.
	if running := cmp.Or(spec.Agent, agent.Default); running != ws.Agent.Name {
		return fmt.Errorf("workspace %q runs %s, but its configuration selects %s; run `jailoc restart %s` to switch (this ends its running sessions)", ws.Name, running, ws.Agent.Name, ws.Name)
	}
	return nil
}

// checkAgentBinary reports a workspace VM whose image lacks the agent, for
// example an image used as is or one built with install = false.
func checkAgentBinary(client *ssh.Client, ws *workspace.Resolved) error {
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	err = sess.Run(shellJoin([]string{guest.BinaryPath, "run", "--", "/bin/sh", "-c", `command -v "$0" >/dev/null`, ws.Agent.Binary}))
	var exitErr *ssh.ExitError
	switch {
	case errors.As(err, &exitErr):
		return fmt.Errorf("workspace %q: %s is not installed in the VM (no %s on PATH); install it in the workspace image, or let jailoc install it: it does so in the images it builds unless agents.%s install = false", ws.Name, ws.Agent.Title, ws.Agent.Binary, ws.Agent.Name)
	case err != nil:
		return fmt.Errorf("check agent in workspace %q: %w", ws.Name, err)
	}
	return nil
}
