//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/proxy"
	"github.com/seznam/jailoc/internal/proxyhost"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	// proxyDefaultListen is where `jailoc proxy` listens by default.
	proxyDefaultListen = "127.0.0.1:8443"
	// proxyDomain is the DNS suffix of every route host:
	// <route>.<workspace>.jailoc.local.
	proxyDomain = "jailoc.local"
	// proxyPollInterval is how often a proxy checks that a workspace VM
	// is still the one it started serving.
	proxyPollInterval = 500 * time.Millisecond
)

// errProxyVMGone ends the routes of a workspace VM that stopped or was
// replaced while the proxy ran. The proxy never follows a replacement:
// it was authorized for one VM boot, and a new boot needs a new proxy.
var errProxyVMGone = errors.New("the workspace VM stopped or was replaced; restart `jailoc proxy` to serve the new VM")

// errProxyNoVMs ends a proxy whose workspace VMs are all gone.
var errProxyNoVMs = errors.New("every served workspace VM stopped or was replaced; restart `jailoc proxy` to serve them again")

type proxyOptions struct {
	listen         string
	certFile       string
	keyFile        string
	listenerBroker bool
	foreground     bool
}

// proxyBrokerTarget is the listener broker `jailoc proxy
// --listener-broker` attaches to: the proxy user's UID, the control socket,
// the UID the broker must run as and the address it listens on.
type proxyBrokerTarget struct {
	uid        uint32
	socketPath string
	brokerUID  uint32
	addr       *net.TCPAddr
}

// proxyBrokerAddr is the address the privileged broker listens on for the
// proxy.
var proxyBrokerAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}

// proxyBrokerHost resolves the listener broker of the running user. Tests
// replace it, which makes them non-parallel.
var proxyBrokerHost = defaultProxyBrokerHost

func defaultProxyBrokerHost() proxyBrokerTarget {
	uid := uint32(os.Getuid()) //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
	return proxyBrokerTarget{uid: uid, socketPath: proxyhost.BrokerSocketPath(uid), brokerUID: 0, addr: proxyBrokerAddr}
}

func init() {
	rootCmd.AddCommand(newProxyCmd())
}

// newProxyCmd builds `jailoc proxy`, the internal HTTPS proxy to the proxy
// routes of running workspace VMs.
func newProxyCmd() *cobra.Command {
	var opts proxyOptions
	cmd := &cobra.Command{
		Use:    "proxy [workspace...]",
		Hidden: true,
		Short:  "Serve the proxy routes of running workspaces over HTTPS",
		Long: "Serve the proxy routes of running workspaces over HTTPS on a loopback address.\n\n" +
			"By default the proxy runs in the background (`--foreground` keeps it in the foreground).\n" +
			"When the background proxy is already running, `jailoc proxy` keeps existing browser sessions and prints the active routes.\n\n" +
			"Each route is https://<route>.<workspace>.jailoc.local; routes with auth = true are protected by a one-time login code, printed once when the route is added.\n" +
			"When --tls-cert and --tls-key are omitted, jailoc generates and reuses a local CA and leaf certificate in ~/.config/jailoc/ingress.\n" +
			"The proxy starts no VM, opens no browser and changes no host setting: make the names resolve to the listen address\n" +
			"(for example with /etc/hosts entries) and trust ~/.config/jailoc/ingress/ca.crt in your browser.\n" +
			"The proxy does not listen on privileged ports itself: browsers use port 443 for these URLs, so a --listen port\n" +
			"other than 443 needs a forward from 443 to it that you set up.\n\n" +
			"Without workspace arguments the proxy serves every running workspace that has routes. Naming a workspace that is\n" +
			"not running, has no routes, or whose running VM does not match its configuration is an error.\n" +
			"A proxy serves the VM boots it started with: when a VM stops or is replaced, its routes close.\n\n" +
			"With --listener-broker the proxy does not open a listener: it attaches to jailoc-listener-broker, a separate\n" +
			"root-owned program that holds 127.0.0.1:443 and [::1]:443 for your user and passes each connection to the proxy, so\n" +
			"the URLs need no forward and no port. Run it as root with --uid <your uid>; only one proxy can attach at a time.\n" +
			"The flag cannot be combined with --listen, and the proxy refuses to run as root.",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxy(cmd, args, opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.foreground, "foreground", "f", false, "run the proxy in the foreground instead of the background")
	cmd.Flags().StringVar(&opts.listen, "listen", proxyDefaultListen, "loopback address to listen on, host:port (port 0 picks a free port)")
	cmd.Flags().BoolVar(&opts.listenerBroker, "listener-broker", false, "serve through jailoc-listener-broker on 127.0.0.1:443 instead of opening a listener")
	cmd.Flags().StringVar(&opts.certFile, "tls-cert", "", "PEM certificate chain that covers every route host (default: automatic cert in ~/.config/jailoc/ingress)")
	cmd.Flags().StringVar(&opts.keyFile, "tls-key", "", "PEM private key of the certificate (default: automatic key in ~/.config/jailoc/ingress)")
	cmd.AddCommand(newProxyStatusCmd(), newProxyStopCmd())
	return cmd
}

func runProxy(cmd *cobra.Command, args []string, opts proxyOptions) error {
	names := args
	if len(names) == 0 && workspaceExplicit {
		names = []string{workspaceFlag}
	}
	if !opts.foreground {
		return runBackgroundProxy(cmd, names, opts)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var listen string
	if opts.listenerBroker {
		if err := checkProxyBrokerOptions(cmd); err != nil {
			return fmt.Errorf("proxy options: %w", err)
		}
	} else {
		var err error
		if listen, err = parseProxyListen(opts.listen); err != nil {
			return err
		}
	}
	if (opts.certFile == "") != (opts.keyFile == "") {
		return errors.New("both --tls-cert and --tls-key must be set, or omit both to use the automatic certificate in ~/.config/jailoc/ingress")
	}
	if opts.certFile != "" {
		if _, err := os.Stat(opts.certFile); err != nil {
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		if _, err := os.Stat(opts.keyFile); err != nil {
			return fmt.Errorf("load TLS certificate: %w", err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	var notes bytes.Buffer
	targets, err := selectProxyTargets(cfg, names, &notes)
	if err != nil {
		return err
	}
	cert, caPath, err := resolveProxyCert(opts, cfg, targets)
	if err != nil {
		return err
	}
	if notes.Len() > 0 {
		_, _ = io.WriteString(cmd.ErrOrStderr(), notes.String())
	}

	unlockProxy, err := acquireProxyLock()
	if err != nil {
		return err
	}
	defer unlockProxy()

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	var watchers sync.WaitGroup
	mgr := &proxyManager{
		runCtx:    runCtx,
		cancelRun: cancelRun,
		opts:      opts,
		caCert:    caPath,
		startedAt: time.Now(),
		instances: make(map[string]*proxyInstance, len(targets)),
		wsRoutes:  make(map[string][]workspace.ProxyRoute, len(targets)),
		watchers:  &watchers,
		errw:      cmd.ErrOrStderr(),
	}
	var routes []proxy.Route
	for _, t := range targets {
		inst := mgr.addInstanceLocked(t)
		routes = append(routes, inst.routes()...)
	}
	gw, codes, err := proxy.New(proxy.Config{Cert: cert, Routes: routes})
	if err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	mgr.gw = gw
	ln, err := listenProxy(runCtx, opts, listen)
	if err != nil {
		return fmt.Errorf("open proxy listener: %w", err)
	}
	defer func() { _ = ln.Close() }()
	mgr.listen = ln.Addr().String()

	ctrlLn, sockPath, cleanupCtrl, err := openProxyControlListener()
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(proxyStateFile())
		cleanupCtrl()
	}()
	mgr.sockPath = sockPath
	_ = writeProxySavedOptions(opts)
	_ = writeProxyState(mgr.stateLocked())
	go mgr.serveControl(ctrlLn)

	bannerOut := cmd.OutOrStdout()
	startupPipe := startupBannerWriter()
	if startupPipe != nil {
		var buf bytes.Buffer
		if notes.Len() > 0 {
			_, _ = buf.Write(notes.Bytes())
		}
		printProxyBannerWithCA(&buf, ln.Addr(), caPath, targets, codes)
		_, _ = startupPipe.Write(buf.Bytes())
		_ = startupPipe.Close()
	} else {
		printProxyBannerWithCA(bannerOut, ln.Addr(), caPath, targets, codes)
	}

	err = gw.Serve(runCtx, ln)
	cancelRun(nil)
	cleanupCtrl()
	watchers.Wait()
	switch {
	case errors.Is(context.Cause(runCtx), errProxyNoVMs):
		return errProxyNoVMs
	case errors.Is(context.Cause(runCtx), context.Canceled) && ctx.Err() == nil:
		return nil
	case ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)):
		return nil
	case err != nil:
		return fmt.Errorf("serve proxy: %w", err)
	}
	return nil
}

// listenProxy opens the listener the proxy serves on: its own on listen,
// or the one fed by the listener broker.
func listenProxy(ctx context.Context, opts proxyOptions, listen string) (net.Listener, error) {
	if opts.listenerBroker {
		return attachProxyBroker(ctx)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", listen, err)
	}
	return ln, nil
}

// checkProxyBrokerOptions refuses --listener-broker together with an
// explicit --listen and when running as root: the broker is the privileged
// part, the proxy that parses HTTP and TLS must not be.
func checkProxyBrokerOptions(cmd *cobra.Command) error {
	if cmd.Flags().Changed("listen") {
		return errors.New("--listener-broker serves on 127.0.0.1:443 through the broker; do not combine it with --listen")
	}
	if proxyBrokerHost().uid == 0 {
		return errors.New("--listener-broker: refusing to run the proxy as root; run it as your own user (only jailoc-listener-broker runs as root)")
	}
	return nil
}

// attachProxyBroker attaches to the listener broker of the running user.
// The returned listener reports the broker's address, so printed URLs have
// no port.
func attachProxyBroker(ctx context.Context) (net.Listener, error) {
	t := proxyBrokerHost()
	ln, err := proxyhost.AttachBroker(ctx, t.socketPath, t.brokerUID, t.addr)
	switch {
	case err == nil:
		return ln, nil
	case errors.Is(err, proxyhost.ErrBrokerBusy):
		return nil, errors.New("another proxy is attached to the listener broker; stop it first, only one proxy can serve port 443 at a time")
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return nil, fmt.Errorf("the listener broker is not running for this user (no answer on %s); a root user must start jailoc-listener-broker --uid %d", t.socketPath, t.uid)
	}
	return nil, fmt.Errorf("attach to the listener broker: %w", err)
}

// parseProxyListen validates a --listen value: a loopback IP address and a
// port, where port 0 picks a free one. Names are refused, since they could
// resolve to a non-loopback address.
func parseProxyListen(s string) (string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("--listen %q: want host:port such as %s: %w", s, proxyDefaultListen, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", fmt.Errorf("--listen %q: host must be a loopback IP address such as 127.0.0.1 or ::1, not a name", s)
	}
	if !ip.Unmap().IsLoopback() {
		return "", fmt.Errorf("--listen %q: %s is not a loopback address; the proxy listens on loopback only", s, ip)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return "", fmt.Errorf("--listen %q: port must be a number from 0 to 65535", s)
	}
	return net.JoinHostPort(ip.Unmap().String(), port), nil
}

// proxyHost is the host name of a proxy route of a workspace.
func proxyHost(route, workspace string) string {
	return route + "." + workspace + "." + proxyDomain
}

// proxyTarget is a workspace VM the proxy serves, as it was when the
// proxy captured it.
type proxyTarget struct {
	ws       *workspace.Resolved
	stateDir string
	id       vmIdentity
}

// selectProxyTargets resolves the workspaces to serve. With names it
// serves exactly those and fails for one that is not running or has no
// routes; without names it serves every configured workspace, in name order,
// that has routes and runs, and notes the others on w. A running VM that
// does not match the configured routes fails either way.
func selectProxyTargets(cfg *config.Config, names []string, w io.Writer) ([]proxyTarget, error) {
	explicit := len(names) > 0
	if !explicit {
		names = slices.Sorted(maps.Keys(cfg.Workspaces))
	} else {
		names = slices.Compact(slices.Sorted(slices.Values(names)))
	}
	var targets []proxyTarget
	for _, name := range names {
		ws, err := workspace.Resolve(cfg, name)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q: %w", name, err)
		}
		if len(ws.Proxy) == 0 {
			if explicit {
				return nil, fmt.Errorf("workspace %q has no proxy routes; add a [workspaces.%s.proxy] table", name, name)
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
		if err := checkProxyVM(ws, stateDir); err != nil {
			return nil, err
		}
		targets = append(targets, proxyTarget{ws: ws, stateDir: stateDir, id: id})
	}
	if len(targets) == 0 {
		return nil, errors.New("no running workspace with proxy routes; start one with `jailoc up`")
	}
	return targets, nil
}

// checkProxyVM verifies that the running VM of ws was started with the
// routes the configuration now has, so that mapping a route to its guest
// port is safe: the guest relay allows every route port. A VM that does not
// match needs a restart first.
func checkProxyVM(ws *workspace.Resolved, stateDir string) error {
	running, err := vm.ReadSpec(stateDir)
	if err != nil {
		return fmt.Errorf("workspace %q: read running VM configuration: %w", ws.Name, err)
	}
	if running.Guest == nil {
		return fmt.Errorf("workspace %q: running VM has no guest configuration; run `jailoc restart %s`", ws.Name, ws.Name)
	}
	if changed := proxyChanges(ws, running.Guest); len(changed) > 0 {
		return fmt.Errorf("workspace %q: the running VM does not match the configured %s; run `jailoc restart %s` first (this ends running sessions)", ws.Name, joinList(changed), ws.Name)
	}
	return nil
}

// proxyChanges names the proxy settings of ws that differ from the running guest
// configuration g and need a restart.
func proxyChanges(ws *workspace.Resolved, g *guest.Config) []string {
	if !slices.Equal(guestProxyPorts(g), proxyRelayPorts(ws.Proxy)) {
		return []string{"proxy"}
	}
	return nil
}

// printProxyBanner prints each route's URL and its login code once, on w.
// Codes are only printed here; they appear in no log and no URL. The proxy
// accepts requests for https://<host> only, which browsers send to port 443,
// so a listen address on another port needs a forward from 443.
func printProxyBanner(w io.Writer, addr net.Addr, targets []proxyTarget, codes map[string]string) {
	printProxyBannerWithCA(w, addr, "", targets, codes)
}

func printProxyBannerWithCA(w io.Writer, addr net.Addr, caPath string, targets []proxyTarget, codes map[string]string) {
	_, _ = fmt.Fprintf(w, "Proxy listening on %s (loopback only)\n", addr)
	if caPath != "" {
		home, _ := os.UserHomeDir()
		_, _ = fmt.Fprintf(w, "CA certificate: %s\n", shortenHome(caPath, home))
	}
	if addrPort(addr) != 443 {
		_, _ = fmt.Fprintf(w, "Browsers use port 443 for these URLs: forward 127.0.0.1:443 to %s, and resolve the host names to 127.0.0.1\n", addr)
	}
	for _, t := range targets {
		_, _ = fmt.Fprintf(w, "Workspace %s:\n", t.ws.Name)
		for _, r := range t.ws.Proxy {
			host := proxyHost(r.Name, t.ws.Name)
			if code := codes[host]; code != "" {
				_, _ = fmt.Fprintf(w, "  %-10s %s\n             login code %s (only valid for %s)\n", r.Name, proxyURL(host), code, proxyOrigin(host))
			} else {
				_, _ = fmt.Fprintf(w, "  %-10s %s\n", r.Name, proxyURL(host))
			}
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

// proxyOrigin is the origin the proxy accepts for host: without a port,
// as the proxy routes are served on 443.
func proxyOrigin(host string) string { return "https://" + host }

// proxyURL is the address of the root page of host.
func proxyURL(host string) string { return proxyOrigin(host) + "/" }

var errVMNotRunning = errors.New("the VM is not running")

// vmIdentity identifies one boot of a workspace VM: its supervisor and
// network broker processes and the private proxy socket they created. A VM
// that stopped and started again differs in at least the socket.
type vmIdentity struct {
	pid, brokerPID int
	sock           os.FileInfo
}

// readVMIdentity reads the identity of the running VM in stateDir from its
// state file and proxy socket, without running any process. It returns
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
	sock, err := os.Stat(vm.ProxySocket(stateDir))
	if err != nil {
		return vmIdentity{}, fmt.Errorf("the VM has no proxy socket (run `jailoc restart` if it predates the proxy routes): %w", err)
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

// proxyInstance is one workspace VM boot served by the proxy. Its
// context ends for good when the VM stops or is replaced.
var nextProxyInstanceGen atomic.Uint64

type proxyInstance struct {
	target proxyTarget
	gen    string
	ctx    context.Context
	cancel context.CancelCauseFunc
	gone   sync.Once
	errw   io.Writer
	// read and dial are the seams of the state file poll and the guest
	// relay dial.
	read func(stateDir string) (vmIdentity, error)
	dial func(ctx context.Context, stateDir string, port uint16) (net.Conn, error)
}

func newProxyInstance(parent context.Context, t proxyTarget, errw io.Writer) *proxyInstance {
	ctx, cancel := context.WithCancelCause(parent)
	gen := fmt.Sprintf("%d:%d:%d", nextProxyInstanceGen.Add(1), t.id.pid, t.id.brokerPID)
	return &proxyInstance{target: t, gen: gen, ctx: ctx, cancel: cancel, errw: errw, read: readVMIdentity, dial: vm.DialProxy}
}

// routes returns the proxy routes of the instance, one per proxy route. Routes
// share the instance context, so ending the instance fails them all.
func (i *proxyInstance) routes() []proxy.Route {
	return i.routesFor(i.target.ws.Proxy)
}

func (i *proxyInstance) routesFor(proxyRoutes []workspace.ProxyRoute) []proxy.Route {
	out := make([]proxy.Route, 0, len(proxyRoutes))
	for _, r := range proxyRoutes {
		route := &proxyRoute{inst: i, port: uint16(r.Port), ctx: i.ctx} //nolint:gosec // G115: validated to 1-65535
		out = append(out, proxy.Route{Host: proxyHost(r.Name, i.target.ws.Name), Dial: route.dialContext, Auth: r.Auth, Generation: i.gen})
	}
	return out
}

// check ends the instance when the VM in its state directory is not the one
// that was captured, and reports whether the instance is still alive.
func (i *proxyInstance) check() error {
	if i.ctx.Err() != nil {
		return context.Cause(i.ctx)
	}
	cur, err := i.read(i.target.stateDir)
	if err != nil || !i.target.id.same(cur) {
		i.markGone()
		return errProxyVMGone
	}
	return nil
}

// markGone ends the instance: its routes fail from now on and their open
// connections close.
func (i *proxyInstance) markGone() {
	i.gone.Do(func() {
		_, _ = fmt.Fprintf(i.errw, "Workspace %s: %v\n", i.target.ws.Name, errProxyVMGone)
		i.cancel(errProxyVMGone)
	})
}

// watch polls the VM state every interval until the instance ends or its
// context does.
func (i *proxyInstance) watch(interval time.Duration) {
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

// proxyRoute dials one guest port of an instance.
type proxyRoute struct {
	inst *proxyInstance
	port uint16
	ctx  context.Context
}

// dialContext connects to the guest port through the proxy relay of the VM the
// instance captured. It checks the VM identity before and after the dial, so
// a replacement VM that reuses the state directory is never reached, and
// ties the connection to the route context.
func (r *proxyRoute) dialContext(ctx context.Context) (net.Conn, error) {
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
