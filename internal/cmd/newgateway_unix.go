//go:build darwin || linux

package cmd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/gatewayhost"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/webgateway"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	// gatewayDefaultListen is where `jailoc gateway` listens by default.
	gatewayDefaultListen = "127.0.0.1:8443"
	// gatewayDomain is the DNS suffix of every route host:
	// <route>.<workspace>.jailoc.local.
	gatewayDomain = "jailoc.local"
	// gatewayPollInterval is how often a gateway checks that a workspace VM
	// is still the one it started serving.
	gatewayPollInterval = 500 * time.Millisecond
)

// errGatewayVMGone ends the routes of a workspace VM that stopped or was
// replaced while the gateway ran. The gateway never follows a replacement:
// it was authorized for one VM boot, and a new boot needs a new gateway.
var errGatewayVMGone = errors.New("the workspace VM stopped or was replaced; restart `jailoc gateway` to serve the new VM")

// errGatewayNoVMs ends a gateway whose workspace VMs are all gone.
var errGatewayNoVMs = errors.New("every served workspace VM stopped or was replaced; restart `jailoc gateway` to serve them again")

type gatewayOptions struct {
	listen         string
	certFile       string
	keyFile        string
	listenerBroker bool
}

// gatewayBrokerTarget is the listener broker `jailoc gateway
// --listener-broker` attaches to: the gateway user's UID, the control socket,
// the UID the broker must run as and the address it listens on.
type gatewayBrokerTarget struct {
	uid        uint32
	socketPath string
	brokerUID  uint32
	addr       *net.TCPAddr
}

// gatewayBrokerAddr is the address the privileged broker listens on for the
// gateway.
var gatewayBrokerAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}

// gatewayBrokerHost resolves the listener broker of the running user. Tests
// replace it, which makes them non-parallel.
var gatewayBrokerHost = defaultGatewayBrokerHost

func defaultGatewayBrokerHost() gatewayBrokerTarget {
	uid := uint32(os.Getuid()) //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
	return gatewayBrokerTarget{uid: uid, socketPath: gatewayhost.BrokerSocketPath(uid), brokerUID: 0, addr: gatewayBrokerAddr}
}

func init() {
	rootCmd.AddCommand(newGatewayCmd())
}

// newGatewayCmd builds `jailoc gateway`, the foreground HTTPS gateway to the
// web routes (desktop and web) of running workspace VMs.
func newGatewayCmd() *cobra.Command {
	var opts gatewayOptions
	cmd := &cobra.Command{
		Use:   "gateway [workspace...]",
		Short: "Serve the web routes of running workspaces over HTTPS",
		Long: "Serve the web routes of running workspaces over HTTPS on a loopback address, in the foreground.\n\n" +
			"Each route is https://<route>.<workspace>.jailoc.local and is protected by a one-time login code, printed once at start.\n" +
			"The gateway starts no VM, opens no browser and changes no host setting: make the names resolve to the listen address\n" +
			"(for example with /etc/hosts entries) and give it a certificate that covers them with --tls-cert and --tls-key.\n" +
			"The gateway does not listen on privileged ports itself: browsers use port 443 for these URLs, so a --listen port\n" +
			"other than 443 needs a forward from 443 to it that you set up.\n\n" +
			"Without workspace arguments the gateway serves every running workspace that has routes. Naming a workspace that is\n" +
			"not running, has no routes, or whose running VM does not match its configuration is an error.\n" +
			"A gateway serves the VM boots it started with: when a VM stops or is replaced, its routes fail and the gateway\n" +
			"must be restarted.\n\n" +
			"With --listener-broker the gateway does not open a listener: it attaches to jailoc-listener-broker, a separate\n" +
			"root-owned program that holds 127.0.0.1:443 and [::1]:443 for your user and passes each connection to the gateway, so\n" +
			"the URLs need no forward and no port. Run it as root with --uid <your uid>; only one gateway can attach at a time.\n" +
			"The flag cannot be combined with --listen, and the gateway refuses to run as root.",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateway(cmd, args, opts)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", gatewayDefaultListen, "loopback address to listen on, host:port (port 0 picks a free port)")
	cmd.Flags().BoolVar(&opts.listenerBroker, "listener-broker", false, "serve through jailoc-listener-broker on 127.0.0.1:443 instead of opening a listener")
	cmd.Flags().StringVar(&opts.certFile, "tls-cert", "", "PEM certificate chain that covers every route host")
	cmd.Flags().StringVar(&opts.keyFile, "tls-key", "", "PEM private key of the certificate")
	_ = cmd.MarkFlagRequired("tls-cert")
	_ = cmd.MarkFlagRequired("tls-key")
	cmd.AddCommand(newGatewayHostProofCmd())
	return cmd
}

func runGateway(cmd *cobra.Command, args []string, opts gatewayOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var listen string
	if opts.listenerBroker {
		if err := checkGatewayBrokerOptions(cmd); err != nil {
			return fmt.Errorf("gateway options: %w", err)
		}
	} else {
		var err error
		if listen, err = parseGatewayListen(opts.listen); err != nil {
			return err
		}
	}
	cert, err := tls.LoadX509KeyPair(opts.certFile, opts.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	names := args
	if len(names) == 0 && workspaceExplicit {
		names = []string{workspaceFlag}
	}
	targets, err := selectGatewayTargets(cfg, names, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	var routes []webgateway.Route
	var instances []*gatewayInstance
	for _, t := range targets {
		inst := newGatewayInstance(runCtx, t, cmd.ErrOrStderr())
		instances = append(instances, inst)
		routes = append(routes, inst.routes()...)
	}
	gw, codes, err := webgateway.New(webgateway.Config{Cert: cert, Routes: routes})
	if err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}
	ln, err := listenGateway(runCtx, opts, listen)
	if err != nil {
		return fmt.Errorf("open gateway listener: %w", err)
	}
	defer func() { _ = ln.Close() }()

	printGatewayRoutes(cmd.OutOrStdout(), ln.Addr(), targets, codes)

	var watchers sync.WaitGroup
	for _, inst := range instances {
		watchers.Add(1)
		go func() {
			defer watchers.Done()
			inst.watch(gatewayPollInterval)
		}()
	}
	go func() {
		watchers.Wait()
		cancelRun(errGatewayNoVMs)
	}()
	err = gw.Serve(runCtx, ln)
	cancelRun(nil)
	watchers.Wait()
	switch {
	case errors.Is(context.Cause(runCtx), errGatewayNoVMs):
		return errGatewayNoVMs
	case ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)):
		return nil
	case err != nil:
		return fmt.Errorf("serve gateway: %w", err)
	}
	return nil
}

// listenGateway opens the listener the gateway serves on: its own on listen,
// or the one fed by the listener broker.
func listenGateway(ctx context.Context, opts gatewayOptions, listen string) (net.Listener, error) {
	if opts.listenerBroker {
		return attachGatewayBroker(ctx)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", listen, err)
	}
	return ln, nil
}

// checkGatewayBrokerOptions refuses --listener-broker together with an
// explicit --listen and when running as root: the broker is the privileged
// part, the gateway that parses HTTP and TLS must not be.
func checkGatewayBrokerOptions(cmd *cobra.Command) error {
	if cmd.Flags().Changed("listen") {
		return errors.New("--listener-broker serves on 127.0.0.1:443 through the broker; do not combine it with --listen")
	}
	if gatewayBrokerHost().uid == 0 {
		return errors.New("--listener-broker: refusing to run the gateway as root; run it as your own user (only jailoc-listener-broker runs as root)")
	}
	return nil
}

// attachGatewayBroker attaches to the listener broker of the running user.
// The returned listener reports the broker's address, so printed URLs have
// no port.
func attachGatewayBroker(ctx context.Context) (net.Listener, error) {
	t := gatewayBrokerHost()
	ln, err := gatewayhost.AttachBroker(ctx, t.socketPath, t.brokerUID, t.addr)
	switch {
	case err == nil:
		return ln, nil
	case errors.Is(err, gatewayhost.ErrBrokerBusy):
		return nil, errors.New("another gateway is attached to the listener broker; stop it first, only one gateway can serve port 443 at a time")
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return nil, fmt.Errorf("the listener broker is not running for this user (no answer on %s); a root user must start jailoc-listener-broker --uid %d", t.socketPath, t.uid)
	}
	return nil, fmt.Errorf("attach to the listener broker: %w", err)
}

// parseGatewayListen validates a --listen value: a loopback IP address and a
// port, where port 0 picks a free one. Names are refused, since they could
// resolve to a non-loopback address.
func parseGatewayListen(s string) (string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("--listen %q: want host:port such as %s: %w", s, gatewayDefaultListen, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", fmt.Errorf("--listen %q: host must be a loopback IP address such as 127.0.0.1 or ::1, not a name", s)
	}
	if !ip.Unmap().IsLoopback() {
		return "", fmt.Errorf("--listen %q: %s is not a loopback address; the gateway listens on loopback only", s, ip)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return "", fmt.Errorf("--listen %q: port must be a number from 0 to 65535", s)
	}
	return net.JoinHostPort(ip.Unmap().String(), port), nil
}

// gatewayHost is the host name of a web route of a workspace.
func gatewayHost(route, workspace string) string {
	return route + "." + workspace + "." + gatewayDomain
}

// gatewayTarget is a workspace VM the gateway serves, as it was when the
// gateway captured it.
type gatewayTarget struct {
	ws       *workspace.Resolved
	stateDir string
	id       vmIdentity
}

// selectGatewayTargets resolves the workspaces to serve. With names it
// serves exactly those and fails for one that is not running or has no
// routes; without names it serves every configured workspace, in name order,
// that has routes and runs, and notes the others on w. A running VM that
// does not match the configured routes fails either way.
func selectGatewayTargets(cfg *config.Config, names []string, w io.Writer) ([]gatewayTarget, error) {
	explicit := len(names) > 0
	if !explicit {
		names = slices.Sorted(maps.Keys(cfg.Workspaces))
	} else {
		names = slices.Compact(slices.Sorted(slices.Values(names)))
	}
	var targets []gatewayTarget
	for _, name := range names {
		ws, err := workspace.Resolve(cfg, name)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q: %w", name, err)
		}
		if len(ws.Web) == 0 {
			if explicit {
				return nil, fmt.Errorf("workspace %q has no web routes; add a [workspaces.%s.web] table", name, name)
			}
			continue
		}
		stateDir := vmStateDir(name)
		id, err := readVMIdentity(stateDir)
		switch {
		case errors.Is(err, errVMNotRunning) && !explicit:
			_, _ = fmt.Fprintf(w, "Skipping workspace %s: not running\n", name)
			continue
		case errors.Is(err, errVMNotRunning):
			return nil, fmt.Errorf("workspace %q is not running; start it with `jailoc up %s`", name, name)
		case err != nil:
			return nil, fmt.Errorf("workspace %q: %w", name, err)
		}
		if err := checkGatewayVM(ws, stateDir); err != nil {
			return nil, err
		}
		targets = append(targets, gatewayTarget{ws: ws, stateDir: stateDir, id: id})
	}
	if len(targets) == 0 {
		return nil, errors.New("no running workspace with web routes; start one with `jailoc up`")
	}
	return targets, nil
}

// checkGatewayVM verifies that the running VM of ws was started with the
// routes the configuration now has, so that mapping a route to its guest
// port is safe: the desktop runs when the desktop route is configured, and
// the guest relay allows every route port. A VM that does not match needs a
// restart first.
func checkGatewayVM(ws *workspace.Resolved, stateDir string) error {
	running, err := vm.ReadSpec(stateDir)
	if err != nil {
		return fmt.Errorf("workspace %q: read running VM configuration: %w", ws.Name, err)
	}
	if running.Guest == nil {
		return fmt.Errorf("workspace %q: running VM has no guest configuration; run `jailoc restart %s`", ws.Name, ws.Name)
	}
	if changed := webChanges(ws, running.Guest); len(changed) > 0 {
		return fmt.Errorf("workspace %q: the running VM does not match the configured %s; run `jailoc restart %s` first (this ends running sessions)", ws.Name, joinList(changed), ws.Name)
	}
	return nil
}

// webChanges names the web settings of ws that differ from the running guest
// configuration g and need a restart.
func webChanges(ws *workspace.Resolved, g *guest.Config) []string {
	if !slices.Equal(guestWebPorts(g), webRelayPorts(ws.Web)) {
		return []string{"web"}
	}
	return nil
}

// printGatewayRoutes prints each route's URL and its login code once, on w.
// Codes are only printed here; they appear in no log and no URL. The gateway
// accepts requests for https://<host> only, which browsers send to port 443,
// so a listen address on another port needs a forward from 443.
func printGatewayRoutes(w io.Writer, addr net.Addr, targets []gatewayTarget, codes map[string]string) {
	_, _ = fmt.Fprintf(w, "Gateway listening on %s (loopback only)\n", addr)
	if addrPort(addr) != 443 {
		_, _ = fmt.Fprintf(w, "Browsers use port 443 for these URLs: forward 127.0.0.1:443 to %s, and resolve the host names to 127.0.0.1\n", addr)
	}
	for _, t := range targets {
		_, _ = fmt.Fprintf(w, "Workspace %s:\n", t.ws.Name)
		for _, r := range t.ws.Web {
			host := gatewayHost(r.Name, t.ws.Name)
			_, _ = fmt.Fprintf(w, "  %-10s %s\n             login code %s (only valid for %s)\n", r.Name, gatewayURL(host), codes[host], gatewayOrigin(host))
		}
	}
}

// addrPort returns the port of a TCP listen address, 0 for any other.
func addrPort(addr net.Addr) int {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

// gatewayOrigin is the origin the gateway accepts for host: without a port,
// as the web routes are served on 443.
func gatewayOrigin(host string) string { return "https://" + host }

// gatewayURL is the address of the root page of host.
func gatewayURL(host string) string { return gatewayOrigin(host) + "/" }

var errVMNotRunning = errors.New("the VM is not running")

// vmIdentity identifies one boot of a workspace VM: its supervisor and
// network broker processes and the private web socket they created. A VM
// that stopped and started again differs in at least the socket.
type vmIdentity struct {
	pid, brokerPID int
	sock           os.FileInfo
}

// readVMIdentity reads the identity of the running VM in stateDir from its
// state file and web socket, without running any process. It returns
// errVMNotRunning when the supervisor is not ready or gone.
func readVMIdentity(stateDir string) (vmIdentity, error) {
	st, err := vm.ReadState(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return vmIdentity{}, errVMNotRunning
	}
	if err != nil {
		return vmIdentity{}, err
	}
	if !st.Ready || !processAlive(st.PID) {
		return vmIdentity{}, errVMNotRunning
	}
	sock, err := os.Stat(vm.WebSocket(stateDir))
	if err != nil {
		return vmIdentity{}, fmt.Errorf("the VM has no web socket (run `jailoc restart` if it predates the web routes): %w", err)
	}
	return vmIdentity{pid: st.PID, brokerPID: st.BrokerPID, sock: sock}, nil
}

func (a vmIdentity) same(b vmIdentity) bool {
	if a.pid != b.pid || a.brokerPID != b.brokerPID {
		return false
	}
	if a.sock == nil || b.sock == nil {
		return a.sock == nil && b.sock == nil
	}
	return os.SameFile(a.sock, b.sock)
}

// gatewayInstance is one workspace VM boot served by the gateway. Its
// context ends for good when the VM stops or is replaced.
type gatewayInstance struct {
	target gatewayTarget
	ctx    context.Context
	cancel context.CancelCauseFunc
	gone   sync.Once
	errw   io.Writer
	// read and dial are the seams of the state file poll and the guest
	// relay dial.
	read func(stateDir string) (vmIdentity, error)
	dial func(ctx context.Context, stateDir string, port uint16) (net.Conn, error)
}

func newGatewayInstance(parent context.Context, t gatewayTarget, errw io.Writer) *gatewayInstance {
	ctx, cancel := context.WithCancelCause(parent)
	return &gatewayInstance{target: t, ctx: ctx, cancel: cancel, errw: errw, read: readVMIdentity, dial: vm.DialWeb}
}

// routes returns the gateway routes of the instance, one per web route. Routes
// share the instance context, so ending the instance fails them all.
func (i *gatewayInstance) routes() []webgateway.Route {
	out := make([]webgateway.Route, 0, len(i.target.ws.Web))
	for _, r := range i.target.ws.Web {
		route := &gatewayRoute{inst: i, port: uint16(r.Port), ctx: i.ctx} //nolint:gosec // G115: validated to 1-65535
		out = append(out, webgateway.Route{Host: gatewayHost(r.Name, i.target.ws.Name), Dial: route.dialContext})
	}
	return out
}

// check ends the instance when the VM in its state directory is not the one
// that was captured, and reports whether the instance is still alive.
func (i *gatewayInstance) check() error {
	if i.ctx.Err() != nil {
		return context.Cause(i.ctx)
	}
	cur, err := i.read(i.target.stateDir)
	if err != nil || !i.target.id.same(cur) {
		i.markGone()
		return errGatewayVMGone
	}
	return nil
}

// markGone ends the instance: its routes fail from now on and their open
// connections close.
func (i *gatewayInstance) markGone() {
	i.gone.Do(func() {
		_, _ = fmt.Fprintf(i.errw, "Workspace %s: %v\n", i.target.ws.Name, errGatewayVMGone)
		i.cancel(errGatewayVMGone)
	})
}

// watch polls the VM state every interval until the instance ends or its
// context does.
func (i *gatewayInstance) watch(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-i.ctx.Done():
			return
		case <-ticker.C:
			if err := i.check(); err != nil {
				return
			}
		}
	}
}

// gatewayRoute dials one guest port of an instance.
type gatewayRoute struct {
	inst *gatewayInstance
	port uint16
	ctx  context.Context
}

// dialContext connects to the guest port through the web relay of the VM the
// instance captured. It checks the VM identity before and after the dial, so
// a replacement VM that reuses the state directory is never reached, and
// ties the connection to the route context.
func (r *gatewayRoute) dialContext(ctx context.Context) (net.Conn, error) {
	if err := r.inst.check(); err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	conn, err := r.inst.dial(dialCtx, r.inst.target.stateDir, r.port)
	if err != nil {
		return nil, fmt.Errorf("dial guest port %d of workspace %s: %w", r.port, r.inst.target.ws.Name, err)
	}
	if err := r.inst.check(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return newRouteConn(conn, r.ctx), nil
}

// routeConn closes its connection when the route context ends.
type routeConn struct {
	net.Conn
	stop func() bool
}

func newRouteConn(conn net.Conn, ctx context.Context) *routeConn {
	return &routeConn{Conn: conn, stop: context.AfterFunc(ctx, func() { _ = conn.Close() })}
}

func (c *routeConn) Close() error {
	c.stop()
	return c.Conn.Close()
}
