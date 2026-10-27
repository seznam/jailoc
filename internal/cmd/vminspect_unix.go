//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func trafficVM(ctx context.Context, ws *workspace.Resolved, v logView, all bool, w io.Writer) error {
	f := newTrafficFormatter(all)
	return tailLog(ctx, w, []string{vm.TrafficLog(vmStateDir(ws.Name))}, v, f.line)
}

func execLogVM(ctx context.Context, ws *workspace.Resolved, v logView, all bool, w io.Writer) error {
	log := vm.ExecLog(vmStateDir(ws.Name))
	f := execFormatter{all: all}
	return tailLog(ctx, w, []string{log + ".1", log}, v, f.line)
}

// execVM runs args (a login shell when empty) as the agent user in the
// running workspace VM, attached to the local terminal.
func execVM(cmd *cobra.Command, ws *workspace.Resolved, args []string) error {
	ctx := cmd.Context()
	st, err := runningVM(vmStateDir(ws.Name))
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("workspace %q is not running; start it with jailoc up", ws.Name)
	}
	if err := checkGuestCurrent(ws.Name); err != nil {
		return err
	}
	if err := ensureSecrets(ctx, ws); err != nil {
		return err
	}
	client, err := vm.DialSSH(ctx, vmStateDir(ws.Name))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	if len(args) == 0 {
		args = []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -l`}
	}
	env := terminalEnv()
	if os.Getenv("TERM") == "" {
		env = append(env, "TERM=xterm-256color")
	}
	cmdline := []string{guest.BinaryPath, "run", "-C", execDir(ws)}
	for _, kv := range env {
		cmdline = append(cmdline, "-e", kv)
	}
	cmdline = append(append(cmdline, "--"), args...)

	fd := int(os.Stdin.Fd()) //nolint:gosec // G115: file descriptors fit in int
	sess.Stdin, sess.Stdout, sess.Stderr = os.Stdin, os.Stdout, os.Stderr
	if term.IsTerminal(fd) {
		w, h, err := term.GetSize(fd)
		if err != nil {
			w, h = 80, 24
		}
		termName := os.Getenv("TERM")
		if termName == "" {
			termName = "xterm-256color"
		}
		if err := sess.RequestPty(termName, h, w, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			return fmt.Errorf("request terminal: %w", err)
		}
		old, err := term.MakeRaw(fd)
		if err == nil {
			defer func() {
				resetTerminalModes(os.Stdout)
				_ = term.Restore(fd, old)
			}()
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if w, h, err := term.GetSize(fd); err == nil {
					_ = sess.WindowChange(h, w)
				}
			}
		}()
	}
	err = sess.Run(shellJoin(cmdline))
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exitErr):
		cmd.SilenceErrors = true
		return exitStatusError{code: exitErr.ExitStatus()}
	case errors.Is(err, io.EOF) || strings.Contains(err.Error(), "remote command exited without exit status"):
		return fmt.Errorf("connection to workspace %q lost", ws.Name)
	default:
		return fmt.Errorf("run in workspace %q: %w", ws.Name, err)
	}
}

// execDir is the current directory when the workspace shares it, else the
// first workspace path.
func execDir(ws *workspace.Resolved) string {
	cwd, err := os.Getwd()
	if err == nil {
		for _, p := range ws.Paths {
			if rel, err := filepath.Rel(p, cwd); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return cwd
			}
		}
	}
	if len(ws.Paths) > 0 {
		return ws.Paths[0]
	}
	return "/home/agent"
}
