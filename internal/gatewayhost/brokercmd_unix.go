//go:build darwin || linux

package gatewayhost

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// brokerCmdInstallHint is appended to every refusal about the location of
// the executable.
const brokerCmdInstallHint = "install jailoc-listener-broker into a root-owned directory such as /usr/local/libexec or " +
	"/Library/PrivilegedHelperTools; the check covers ownership and mode only, it does not verify the binary's contents"

// brokerCmdBind is one loopback address the privileged broker listens on.
type brokerCmdBind struct {
	network string // "tcp4" or "tcp6"
	addr    string // host:port
}

// brokerCmdDeps are the seams of the jailoc-listener-broker command. The
// production values are in RunListenerBroker; tests substitute unprivileged
// ones (the current user for root, ephemeral ports, temporary directories).
type brokerCmdDeps struct {
	// uids returns the real and effective UID.
	uids func() (ruid, euid int)
	// executable returns the path of the running binary.
	executable func() (string, error)
	// trustedOwner owns the executable and every directory above it; trustTop
	// is the last directory of that chain.
	trustedOwner uint32
	trustTop     string
	// binds are the addresses to listen on, all required, in order.
	binds  []brokerCmdBind
	listen func(network, addr string) (*net.TCPListener, error)
	// socketPath returns the control socket path of the served user.
	socketPath func(uid uint32) string
	stderr     io.Writer
}

// RunListenerBroker runs the privileged listener broker of jailoc-listener-broker
// for the user named by args ("--uid N"). It listens on 127.0.0.1:443 and
// [::1]:443, forwards each accepted connection to the gateway of that user
// over the broker control socket, and returns nil after ctx ends. It refuses
// to run unless it is root and its binary sits in a root-owned location.
// Diagnostics go to stderr and never contain connection data.
func RunListenerBroker(ctx context.Context, args []string, stderr io.Writer) error {
	return brokerCmdRun(ctx, args, brokerCmdDeps{
		uids:         func() (int, int) { return os.Getuid(), os.Geteuid() },
		executable:   os.Executable,
		trustedOwner: 0,
		trustTop:     "/",
		binds:        []brokerCmdBind{{"tcp4", "127.0.0.1:443"}, {"tcp6", "[::1]:443"}},
		listen:       brokerCmdListen,
		socketPath:   BrokerSocketPath,
		stderr:       stderr,
	})
}

func brokerCmdRun(ctx context.Context, args []string, d brokerCmdDeps) error {
	ruid, euid := d.uids()
	if ruid != 0 || euid != 0 {
		return fmt.Errorf("jailoc-listener-broker must run as root (real uid %d, effective uid %d); it binds port 443 for another user", ruid, euid)
	}
	uid, err := brokerCmdParseArgs(args)
	if err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if err := brokerCmdCheckSelf(d); err != nil {
		return fmt.Errorf("check own executable: %w", err)
	}

	var logMu sync.Mutex
	logf := func(format string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		// A failed diagnostic write cannot be reported anywhere else.
		_, _ = fmt.Fprintf(d.stderr, "jailoc-listener-broker: "+format+"\n", a...)
	}

	listeners, err := brokerCmdBindAll(d)
	if err != nil {
		return fmt.Errorf("bind loopback listeners: %w", err)
	}
	control, err := ListenBrokerSocket(d.socketPath(uid), uid)
	if err != nil {
		return errors.Join(fmt.Errorf("listen on the control socket: %w", err), brokerCmdCloseAll(listeners))
	}
	b, err := NewBroker(BrokerConfig{OwnerUID: uid, Listeners: listeners, Control: control, Logf: logf})
	if err != nil {
		cerr := control.Close()
		if cerr != nil {
			cerr = fmt.Errorf("close control socket: %w", cerr)
		}
		return errors.Join(fmt.Errorf("start listener broker: %w", err), cerr, brokerCmdCloseAll(listeners))
	}
	for _, ln := range listeners {
		logf("listening on %s", ln.Addr())
	}
	logf("serving gateways of uid %d on %s", uid, d.socketPath(uid))
	if err := b.Serve(ctx); err != nil {
		return fmt.Errorf("serve listener broker: %w", err)
	}
	logf("stopped")
	return nil
}

// brokerCmdParseArgs accepts exactly "--uid N" (or "--uid=N") with N a
// non-zero 32-bit UID. Nothing else is configurable.
func brokerCmdParseArgs(args []string) (uint32, error) {
	const usage = "usage: jailoc-listener-broker --uid N"
	var uid uint32
	seen := 0
	flags := flag.NewFlagSet("jailoc-listener-broker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Func("uid", "UID of the user whose gateway is served", func(v string) error {
		seen++
		if seen > 1 {
			return errors.New("--uid was given more than once")
		}
		n, err := strconv.ParseUint(v, 10, 31)
		if err != nil {
			return fmt.Errorf("--uid %q is not a decimal 31-bit UID", v)
		}
		if n == 0 {
			return errors.New("--uid 0 is refused: the broker serves an unprivileged user, never root")
		}
		uid = uint32(n)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return 0, fmt.Errorf("invalid arguments (%s): %w", usage, err)
	}
	if flags.NArg() > 0 {
		return 0, fmt.Errorf("unexpected arguments %q; the only accepted argument is --uid (%s)", flags.Args(), usage)
	}
	if seen == 0 {
		return 0, fmt.Errorf("--uid is required (%s)", usage)
	}
	return uid, nil
}

// brokerCmdCheckSelf refuses to continue unless the running binary and its
// directories are root-owned and not writable by others, so an unprivileged
// user cannot get their own code run as root through this entrypoint.
func brokerCmdCheckSelf(d brokerCmdDeps) error {
	exe, err := d.executable()
	if err != nil {
		return fmt.Errorf("find own executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve own executable path: %w", err)
	}
	if err := brokerCmdCheckTrusted(exe, d.trustedOwner, d.trustTop); err != nil {
		return fmt.Errorf("refusing to run: %w; %s", err, brokerCmdInstallHint)
	}
	return nil
}

// brokerCmdCheckTrusted verifies that path, which must be absolute and free
// of symlinks, is a regular file and that it and every directory from its
// parent up to and including top are owned by owner and not writable by
// group or others. It looks at ownership and mode only.
func brokerCmdCheckTrusted(path string, owner uint32, top string) error {
	if !filepath.IsAbs(path) || !filepath.IsAbs(top) {
		return fmt.Errorf("executable path %q and trust anchor %q must be absolute", path, top)
	}
	path, top = filepath.Clean(path), filepath.Clean(top)
	rel, err := filepath.Rel(top, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("executable %s is not below %s", path, top)
	}
	if err := brokerCmdCheckNode(path, owner, false); err != nil {
		return fmt.Errorf("check executable: %w", err)
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if err := brokerCmdCheckNode(dir, owner, true); err != nil {
			return fmt.Errorf("check executable directory: %w", err)
		}
		if dir == top {
			return nil
		}
	}
}

func brokerCmdCheckNode(path string, owner uint32, dir bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", path)
	case dir && !fi.IsDir():
		return fmt.Errorf("%s is not a directory", path)
	case !dir && !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: no ownership information", path)
	}
	if st.Uid != owner {
		return fmt.Errorf("%s is owned by uid %d, want uid %d", path, st.Uid, owner)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s has mode %v: group or other writable", path, fi.Mode().Perm())
	}
	return nil
}

// brokerCmdBindAll opens every address of d.binds. All are required: when one
// fails, the ones opened before are closed and the failing address is
// named.
func brokerCmdBindAll(d brokerCmdDeps) ([]*net.TCPListener, error) {
	var opened []*net.TCPListener
	for _, bind := range d.binds {
		ln, err := d.listen(bind.network, bind.addr)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("listen on %s %s: %w", bind.network, bind.addr, err), brokerCmdCloseAll(opened))
		}
		opened = append(opened, ln)
	}
	return opened, nil
}

func brokerCmdCloseAll(lns []*net.TCPListener) error {
	var errs []error
	for _, ln := range lns {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("close listener %s: %w", ln.Addr(), err))
		}
	}
	return errors.Join(errs...)
}

// brokerCmdListen listens on one TCP address of the given network.
func brokerCmdListen(network, addr string) (*net.TCPListener, error) {
	if network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("unsupported network %q", network)
	}
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return nil, fmt.Errorf("parse address: %w", err)
	}
	ln, err := net.ListenTCP(network, net.TCPAddrFromAddrPort(ap))
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	return ln, nil
}
