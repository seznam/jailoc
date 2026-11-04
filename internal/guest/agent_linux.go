package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/stacklok/go-microvm/net/topology"
)

// startAgentRelay serves SSHAgentSocket for the agent user and relays every
// connection to the broker gateway, which forwards it to the host SSH agent.
// The returned function stops serving, closes open connections and removes
// the socket.
func startAgentRelay(logger *slog.Logger, acct account) (func(), error) {
	return startSocketRelay(logger, acct, SSHAgentSocket, SSHAgentPort, "ssh agent")
}

// gpgAgentSocket is the socket gpg run by uid connects to: gpg prefers the
// socket directory under /run/user/<uid> to ~/.gnupg when the former exists.
func gpgAgentSocket(uid uint32) string {
	return "/run/user/" + strconv.FormatUint(uint64(uid), 10) + "/gnupg/S.gpg-agent"
}

// startGPGAgentRelay serves the agent user's gpg agent socket and relays
// every connection to the broker gateway, which forwards it to the host gpg
// agent's extra socket. With the socket in place gpg never starts an agent
// of its own in the guest.
func startGPGAgentRelay(logger *slog.Logger, acct account) (func(), error) {
	sock := gpgAgentSocket(acct.uid)
	runtimeDir := filepath.Dir(filepath.Dir(sock))
	if err := os.MkdirAll(filepath.Dir(runtimeDir), 0o755); err != nil { //nolint:gosec // G301: /run/user, like systemd-logind creates it
		return nil, fmt.Errorf("gpg agent: create %s: %w", filepath.Dir(runtimeDir), err)
	}
	// gpg uses the directories only when the agent user owns them and
	// nobody else can enter them.
	for _, dir := range []string{runtimeDir, filepath.Dir(sock)} {
		if err := ownedDir(dir, acct, 0o700); err != nil {
			return nil, fmt.Errorf("gpg agent: %w", err)
		}
	}
	return startSocketRelay(logger, acct, sock, GPGAgentPort, "gpg agent")
}

// startSocketRelay serves the unix socket sock for the agent user and relays
// every connection to gateway port port. The returned function stops
// serving, closes open connections and removes the socket.
func startSocketRelay(logger *slog.Logger, acct account, sock string, port int, name string) (func(), error) {
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: remove stale socket: %w", name, err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("%s: listen: %w", name, err)
	}
	if err := os.Chown(sock, int(acct.uid), int(acct.gid)); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("%s: chown socket: %w", name, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("%s: chmod socket: %w", name, err)
	}
	upstream := net.JoinHostPort(topology.GatewayIP, strconv.Itoa(port))
	ctx, cancel := context.WithCancel(context.Background())
	go serveRelay(ctx, logger, l, upstream, name)
	return func() {
		cancel()
		_ = l.Close()
		_ = os.Remove(sock)
	}, nil
}

// serveRelay relays every connection accepted on l to upstream until l is
// closed. Cancelling ctx closes the open connections too, so that disabling
// a relay ends its use at once.
func serveRelay(ctx context.Context, logger *slog.Logger, l net.Listener, upstream, name string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			var d net.Dialer
			dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
			u, err := d.DialContext(dctx, "tcp", upstream)
			dcancel()
			if err != nil {
				logger.Warn(name+": connect to host", "error", err)
				return
			}
			defer func() { _ = u.Close() }()
			stop := context.AfterFunc(ctx, func() {
				_ = c.Close()
				_ = u.Close()
			})
			defer stop()
			spliceConns(c, u)
		}()
	}
}

// spliceConns copies both directions until each ends. When one side stops
// sending, its peer's write side is closed so EOF-delimited protocols still
// get their response.
func spliceConns(a, b net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
