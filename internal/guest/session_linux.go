//go:build linux

package guest

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/seznam/jailoc/internal/guest/session"
)

// SessionsDir holds the sockets and metadata of detachable sessions.
const SessionsDir = "/run/jailoc/sessions"

// Session runs the "session" subcommand of jailoc-guest:
//
//	session open [--new] [--id ID] [--rows N --cols N] [-C DIR] [-e K=V]... -- CMD...
//	session list
//	session stop ID
//	session serve ID
//
// open attaches stdin and stdout, which carry the session frame protocol,
// to the session started in DIR (or ID), creating one when none runs.
func Session(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("session: missing subcommand")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("session: locate jailoc-guest: %w", err)
	}
	switch args[0] {
	case "serve":
		if len(args) != 2 {
			return errors.New("session serve: want ID")
		}
		return session.Serve(SessionsDir, args[1])
	case "stop":
		if len(args) != 2 {
			return errors.New("session stop: want ID")
		}
		return session.Stop(SessionsDir, args[1])
	case "list":
		infos, err := session.List(SessionsDir)
		if err != nil {
			return err
		}
		for i := range infos {
			infos[i].Argv, infos[i].Env = nil, nil
		}
		if infos == nil {
			infos = []session.Info{}
		}
		return json.NewEncoder(stdout).Encode(infos)
	case "open":
		return openSession(self, args[1:], stdin, stdout)
	default:
		return fmt.Errorf("session: unknown subcommand %q", args[0])
	}
}

func openSession(self string, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("session open", flag.ContinueOnError)
	newSession := fs.Bool("new", false, "always start a new session")
	id := fs.String("id", "", "attach to this session")
	dir := fs.String("C", "", "working directory")
	rows := fs.Uint("rows", 24, "initial rows")
	cols := fs.Uint("cols", 80, "initial columns")
	var env multiFlag
	fs.Var(&env, "e", "KEY=VALUE added to the program environment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd := fs.Args()
	if *id == "" && !*newSession {
		found, err := session.Find(SessionsDir, *dir)
		if err != nil {
			return err
		}
		if found != nil {
			*id = found.ID
		}
	}
	if *id == "" {
		if len(cmd) == 0 {
			return errors.New("session open: no command")
		}
		argv := []string{self, "run", "-C", *dir}
		for _, kv := range env {
			argv = append(argv, "-e", kv)
		}
		argv = append(append(argv, "--"), cmd...)
		created, err := session.Create(SessionsDir, session.Options{
			Name: strings.Join(cmd, " "), Dir: *dir, Argv: argv, Env: os.Environ(),
			Rows: uint16(min(*rows, 0xffff)), Cols: uint16(min(*cols, 0xffff)), //nolint:gosec // G115: clamped
		}, []string{self, "session", "serve"})
		if err != nil {
			return err
		}
		*id = created
	}
	if err := session.WriteFrame(stdout, session.Frame{Type: session.FrameInfo, Payload: []byte(*id)}); err != nil {
		return err
	}
	return session.Attach(SessionsDir, *id, stdin, stdout)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
