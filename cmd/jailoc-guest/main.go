//go:build linux

// Command jailoc-guest is the jailoc agent inside a backend "vm" workspace
// VM. Without arguments it initialises the guest and serves SSH sessions;
// "secrets" stores secrets read from stdin; "run [-C DIR] [-e KEY=VALUE]...
// -- CMD..." executes a command with the workspace environment; "session"
// manages detachable agent sessions.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/seznam/jailoc/internal/guest"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "jailoc-guest: %v\n", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return guest.Init(logger)
	}
	switch args[0] {
	case "secrets":
		return guest.WriteSecrets(os.Stdin)
	case guest.UsernsHoldCommand:
		return guest.HoldUserNamespace(os.Stdin)
	case "session":
		return guest.Session(args[1:], os.Stdin, os.Stdout)
	case "run":
		var dir string
		var env []string
		rest := args[1:]
		for len(rest) > 0 {
			switch {
			case rest[0] == "--":
				return guest.Exec(dir, env, rest[1:])
			case rest[0] == "-C" && len(rest) > 1:
				dir, rest = rest[1], rest[2:]
			case rest[0] == "-e" && len(rest) > 1:
				env, rest = append(env, rest[1]), rest[2:]
			default:
				return guest.Exec(dir, env, rest)
			}
		}
		return fmt.Errorf("run: no command")
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
