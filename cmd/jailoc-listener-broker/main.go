//go:build darwin || linux

// Command jailoc-listener-broker is the privileged half of the opt-in
// listener broker of `jailoc proxy --listener-broker`. Run as root, it
// binds 127.0.0.1 and [::1] at an HTTP port (default 80) and an HTTPS port
// (default 443) for one user and hands every accepted connection to that
// user's proxy over a Unix socket; it parses no HTTP or TLS. It takes --uid N
// and optionally --http-port P and --https-port P, and refuses to run from a
// location that is not root-owned.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/seznam/jailoc/internal/proxyhost"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := proxyhost.RunListenerBroker(ctx, os.Args[1:], os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "jailoc-listener-broker:", err)
		os.Exit(1)
	}
}
