package guest

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/stacklok/go-microvm/net/topology"
)

// startAgentRelay serves SSHAgentSocket for the agent user and relays every
// connection to the broker gateway, which forwards it to the host SSH agent.
// The returned function stops serving and removes the socket.
func startAgentRelay(logger *slog.Logger, acct account) (func(), error) {
	if err := os.Remove(SSHAgentSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("ssh agent: remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", SSHAgentSocket)
	if err != nil {
		return nil, fmt.Errorf("ssh agent: listen: %w", err)
	}
	if err := os.Chown(SSHAgentSocket, int(acct.uid), int(acct.gid)); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("ssh agent: chown socket: %w", err)
	}
	if err := os.Chmod(SSHAgentSocket, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("ssh agent: chmod socket: %w", err)
	}
	upstream := net.JoinHostPort(topology.GatewayIP, strconv.Itoa(SSHAgentPort))
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				u, err := net.DialTimeout("tcp", upstream, 10*time.Second)
				if err != nil {
					logger.Warn("ssh agent: connect to host", "error", err)
					return
				}
				defer func() { _ = u.Close() }()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(u, c); done <- struct{}{} }()
				go func() { _, _ = io.Copy(c, u); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	return func() {
		_ = l.Close()
		_ = os.Remove(SSHAgentSocket)
	}, nil
}
