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
	// proxyLoopbackHost is the address a standalone proxy binds by default.
	proxyLoopbackHost = "127.0.0.1"
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
	listenerBroker bool
	foreground     bool
}

// proxyBrokerTarget is the listener broker `jailoc proxy
// --listener-broker` attaches to: the proxy user's UID, the control socket,
// the UID the broker must run as and the HTTP and HTTPS addresses it listens
// on.
type proxyBrokerTarget struct {
	uid                 uint32
	socketPath          string
	brokerUID           uint32
	httpAddr, httpsAddr *net.TCPAddr
}

// proxyBrokerHost resolves the listener broker of the running user for the
// configured browser ports. Tests replace it, which makes them non-parallel.
var proxyBrokerHost = defaultProxyBrokerHost

func defaultProxyBrokerHost(ports config.ProxyPorts) proxyBrokerTarget {
	uid := uint32(os.Getuid()) //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
	ip := net.IPv4(127, 0, 0, 1)
	return proxyBrokerTarget{
		uid: uid, socketPath: proxyhost.BrokerSocketPath(uid), brokerUID: 0,
		httpAddr:  &net.TCPAddr{IP: ip, Port: ports.HTTP},
		httpsAddr: &net.TCPAddr{IP: ip, Port: ports.HTTPS},
	}
}

// loadProxyPorts returns the configured proxy ports, the defaults when there
// is no configuration file.
func loadProxyPorts() (config.ProxyPorts, error) {
	cfg, err := loadProxyHostConfig()
	if err != nil {
		return config.ProxyPorts{}, err
	}
	return cfg.Proxy.Ports(), nil
}

// proxyStandaloneListen is where a standalone proxy serves HTTPS locally
// without --listen.
func proxyStandaloneListen(ports config.ProxyPorts) string {
	return net.JoinHostPort(proxyLoopbackHost, strconv.Itoa(ports.StandaloneHTTPS))
}

func init() {
	rootCmd.AddCommand(newProxyCmd())
}

// newProxyCmd builds `jailoc proxy`, the internal HTTPS proxy to the proxy
// routes of running workspace VMs.
func newProxyCmd() *cobra.Command {
	var opts proxyOptions
	cmd := &cobra.Command{
		Use:   "proxy [workspace...]",
		Short: "Serve the proxy routes of running workspaces over HTTPS (see also: proxy install, uninstall, status, stop)",
		Long: "Serve the proxy routes of running workspaces over HTTPS, and redirect plain HTTP to HTTPS, on loopback addresses.\n\n" +
			"Subcommands: `jailoc proxy install` sets up the host once (privileged listener broker on the [proxy] http_port and https_port, 80 and 443 by default, /etc/hosts entries,\n" +
			"CA trust), `jailoc proxy uninstall` reverts it, `jailoc proxy status` shows the proxy and the host setup, and\n" +
			"`jailoc proxy stop` stops the background proxy.\n\n" +
			"By default the proxy runs in the background (`--foreground` keeps it in the foreground).\n" +
			"When the background proxy is already running, `jailoc proxy` keeps existing browser sessions and prints the active routes.\n\n" +
			"Each route is https://<route>.<workspace>.jailoc.local (with :<https_port> unless that is 443); https://<workspace>.jailoc.local answers \"OK\" and shows the host setup works; routes with auth = true are protected by a one-time login code, printed once when the route is added.\n" +
			"Plain http:// requests to a route host on http_port are redirected to its https:// URL unless the route sets redirect = false; the redirect never reaches a route's login or app.\n" +
			"jailoc generates and reuses a local CA and leaf certificate in ~/.local/share/jailoc/proxy; the CA is limited to names under jailoc.local.\n" +
			"The proxy starts no VM, opens no browser and changes no host setting: make the names resolve to the listen address\n" +
			"(for example with /etc/hosts entries) and trust ~/.local/share/jailoc/proxy/ca.crt in your browser.\n" +
			"The proxy does not listen on privileged ports itself. Without the listener broker it binds [proxy] standalone_https_port\n" +
			"(7043 by default) for HTTPS, or the --listen address, and standalone_http_port (7080) for the redirect. Browsers use https_port and\n" +
			"http_port for the URLs, so unless those are the ports it binds, you set up the forwards to it yourself.\n" +
			"Changing the [proxy] ports needs `jailoc proxy install` again for the listener broker and a proxy restart (`jailoc proxy` does it).\n\n" +
			"Without workspace arguments the proxy serves every running workspace that has routes. Naming a workspace that is\n" +
			"not running, has no routes, or whose running VM does not match its configuration is an error.\n" +
			"A proxy serves the VM boots it started with: when a VM stops or is replaced, its routes close.\n\n" +
			"With --listener-broker the proxy does not open a listener: it attaches to jailoc-listener-broker, a separate\n" +
			"root-owned program that holds 127.0.0.1 and [::1] on http_port and https_port for your user and passes each connection to the proxy, so\n" +
			"the URLs need no forward and, with the default ports, no port. Run it as root with --uid <your uid>; only one proxy can attach at a time.\n" +
			"The flag cannot be combined with --listen, and the proxy refuses to run as root.",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxy(cmd, args, opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.foreground, "foreground", "f", false, "run the proxy in the foreground instead of the background")
	cmd.Flags().StringVar(&opts.listen, "listen", "", "loopback address to serve HTTPS on locally, host:port (default 127.0.0.1:<standalone_https_port>, port 0 picks a free port)")
	cmd.Flags().BoolVar(&opts.listenerBroker, "listener-broker", false, "serve through jailoc-listener-broker on 127.0.0.1 http_port and https_port instead of opening listeners")
	cmd.AddCommand(newProxyInstallCmd(), newProxyUninstallCmd(), newProxyStatusCmd(), newProxyStopCmd())
	return cmd
}

func runProxy(cmd *cobra.Command, args []string, opts proxyOptions) error {
	names := args
	if len(names) == 0 && workspaceExplicit {
		names = []string{workspaceFlag}
	}
	if !cmd.Flags().Changed("listen") && !cmd.Flags().Changed("listener-broker") {
		if saved := loadProxySavedOptions(); saved.listenerBroker {
			opts.listenerBroker = true
			opts.listen = ""
		}
	}
	if !opts.foreground {
		return runBackgroundProxy(cmd, names, opts)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkProxyOptions(cmd, opts); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ports := cfg.Proxy.Ports()
	listen, err := proxyListenAddr(opts, ports)
	if err != nil {
		return err
	}
	var notes bytes.Buffer
	targets, err := selectProxyTargets(cfg, names, &notes)
	if err != nil {
		return err
	}
	cert, caPath, err := ensureProxyCert(cfg, targets)
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
		runCtx:     runCtx,
		cancelRun:  cancelRun,
		opts:       opts,
		ports:      ports,
		caCert:     caPath,
		configPath: proxyConfigPath(),
		startedAt:  time.Now(),
		instances:  make(map[string]*proxyInstance, len(targets)),
		wsRoutes:   make(map[string][]workspace.ProxyRoute, len(targets)),
		watchers:   &watchers,
		errw:       cmd.ErrOrStderr(),
	}
	// Watchers start in addInstanceLocked and may remove instances at once,
	// so the maps are only touched under mgr.mu from here on.
	var routes []proxy.Route
	mgr.mu.Lock()
	for _, t := range targets {
		inst := mgr.addInstanceLocked(t)
		routes = append(routes, inst.routes()...)
	}
	mgr.mu.Unlock()
	gw, codes, err := proxy.New(proxy.Config{Cert: cert, Routes: routes, HTTPPort: ports.HTTP, HTTPSPort: ports.HTTPS})
	if err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	mgr.gw = gw
	httpsLn, httpLn, err := listenProxy(runCtx, opts, ports, listen)
	if err != nil {
		return fmt.Errorf("open proxy listener: %w", err)
	}
	defer func() { _ = httpsLn.Close() }()
	defer func() { _ = httpLn.Close() }()
	mgr.listen = httpsLn.Addr().String()
	mgr.redirectListen = httpLn.Addr().String()

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
	mgr.mu.Lock()
	startState := mgr.stateLocked()
	mgr.mu.Unlock()
	if err := writeProxyState(startState); err != nil {
		return fmt.Errorf("write proxy state: %w", err)
	}
	go mgr.serveControl(ctrlLn)

	bannerOut := cmd.OutOrStdout()
	ep := proxyEndpoints{httpsAddr: httpsLn.Addr(), httpAddr: httpLn.Addr(), httpPort: ports.HTTP, httpsPort: ports.HTTPS}
	startupPipe := startupBannerWriter()
	if startupPipe != nil {
		var buf bytes.Buffer
		if notes.Len() > 0 {
			_, _ = buf.Write(notes.Bytes())
		}
		printProxyBannerWithCA(&buf, ep, caPath, targets, codes)
		_, _ = startupPipe.Write(buf.Bytes())
		_ = startupPipe.Close()
	} else {
		printProxyBannerWithCA(bannerOut, ep, caPath, targets, codes)
	}

	err = serveProxy(runCtx, cancelRun, gw, httpsLn, httpLn)
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

// checkProxyOptions validates the listen options before anything is read or
// started.
func checkProxyOptions(cmd *cobra.Command, opts proxyOptions) error {
	if opts.listenerBroker {
		if err := checkProxyBrokerOptions(cmd); err != nil {
			return fmt.Errorf("proxy options: %w", err)
		}
		return nil
	}
	if opts.listen != "" {
		if _, err := parseProxyListen(opts.listen); err != nil {
			return err
		}
	}
	return nil
}

// proxyListenAddr is the local HTTPS address a standalone proxy binds: the
// --listen value, else 127.0.0.1 on standalone_https_port. A proxy that
// serves through the listener broker has none.
func proxyListenAddr(opts proxyOptions, ports config.ProxyPorts) (string, error) {
	switch {
	case opts.listenerBroker:
		return "", nil
	case opts.listen == "":
		return parseProxyListen(proxyStandaloneListen(ports))
	}
	return parseProxyListen(opts.listen)
}

// listenProxy opens the two listeners the proxy serves on: HTTPS and the
// plain HTTP redirect. Standalone, they are its own, HTTPS on listen and HTTP
// on the same address with standalone_http_port; with the listener broker
// they are the pair the broker feeds. It opens both or neither.
func listenProxy(ctx context.Context, opts proxyOptions, ports config.ProxyPorts, listen string) (httpsLn, httpLn net.Listener, err error) {
	if opts.listenerBroker {
		return attachProxyBroker(ctx, ports)
	}
	var lc net.ListenConfig
	httpsLn, err = lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", listen, err)
	}
	tcp, ok := httpsLn.Addr().(*net.TCPAddr)
	if !ok {
		_ = httpsLn.Close()
		return nil, nil, fmt.Errorf("listen on %s: address %v is not a TCP address", listen, httpsLn.Addr())
	}
	httpAddr := &net.TCPAddr{IP: tcp.IP, Port: ports.StandaloneHTTP}
	httpLn, err = lc.Listen(ctx, "tcp", httpAddr.String())
	if err != nil {
		_ = httpsLn.Close()
		return nil, nil, fmt.Errorf("listen on %s for the HTTP redirect: %w", httpAddr, err)
	}
	return httpsLn, httpLn, nil
}

// serveProxy serves the HTTPS and the HTTP redirect listener under one
// lifetime: whichever ends first ends ctx, and with it the other. A failing
// redirect listener fails the proxy.
func serveProxy(ctx context.Context, cancel context.CancelCauseFunc, gw *proxy.Server, httpsLn, httpLn net.Listener) error {
	redirectDone := make(chan error, 1)
	go func() {
		err := gw.ServeHTTPRedirect(ctx, httpLn)
		cancel(err)
		redirectDone <- err
	}()
	err := gw.Serve(ctx, httpsLn)
	cancel(nil)
	if redirectErr := <-redirectDone; err == nil {
		err = redirectErr
	}
	return err
}

// checkProxyBrokerOptions refuses --listener-broker together with an
// explicit --listen and when running as root: the broker is the privileged
// part, the proxy that parses HTTP and TLS must not be.
func checkProxyBrokerOptions(cmd *cobra.Command) error {
	if cmd.Flags().Changed("listen") {
		return errors.New("--listener-broker serves through the broker's ports; do not combine it with --listen")
	}
	if proxyBrokerHost(config.ProxyPorts{}).uid == 0 { // the UID does not depend on the ports
		return errors.New("--listener-broker: refusing to run the proxy as root; run it as your own user (only jailoc-listener-broker runs as root)")
	}
	return nil
}

// attachProxyBroker attaches to the listener broker of the running user. It
// returns the HTTPS and the HTTP listener. They report the broker's addresses,
// so printed URLs carry only a non-default port.
func attachProxyBroker(ctx context.Context, ports config.ProxyPorts) (httpsLn, httpLn net.Listener, err error) {
	if err := probeProxyBroker(ctx, ports); err != nil {
		return nil, nil, err
	}
	t := proxyBrokerHost(ports)
	httpLn, httpsLn, err = proxyhost.AttachBrokerPair(ctx, t.socketPath, t.brokerUID, t.httpAddr, t.httpsAddr)
	if err != nil {
		return nil, nil, proxyBrokerError(t, err)
	}
	return httpsLn, httpLn, nil
}

// probeProxyBroker checks that the listener broker of the running user
// answers and serves exactly the configured ports.
func probeProxyBroker(ctx context.Context, ports config.ProxyPorts) error {
	t := proxyBrokerHost(ports)
	if err := proxyhost.ProbeBrokerPair(ctx, t.socketPath, t.brokerUID, t.httpAddr, t.httpsAddr); err != nil {
		return proxyBrokerError(t, err)
	}
	return nil
}

// proxyBrokerError explains a failure to reach the listener broker of t.
func proxyBrokerError(t proxyBrokerTarget, err error) error {
	switch {
	case errors.Is(err, proxyhost.ErrBrokerBusy):
		return fmt.Errorf("another proxy is attached to the listener broker; stop it first, only one proxy can serve ports %d and %d at a time", t.httpAddr.Port, t.httpsAddr.Port)
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("the listener broker is not running for this user (no answer on %s); a root user must start jailoc-listener-broker --uid %d", t.socketPath, t.uid)
	}
	return fmt.Errorf("attach to the listener broker (after changing the [proxy] ports, run `jailoc proxy install`): %w", err)
}

// parseProxyListen validates a --listen value: a loopback IP address and a
// port, where port 0 picks a free one. Names are refused, since they could
// resolve to a non-loopback address.
func parseProxyListen(s string) (string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("--listen %q: want host:port such as %s: %w", s, proxyStandaloneListen(config.ProxyPorts{StandaloneHTTPS: config.DefaultProxyStandaloneHTTPSPort}), err)
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

// proxyWorkspaceHost is the bare host name of a workspace. It answers "OK"
// while the workspace has proxy routes, so a browser can check the host setup.
func proxyWorkspaceHost(workspace string) string {
	return workspace + "." + proxyDomain
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
// match needs a reload first, or a restart when its guest cannot replace the
// relay allowlist.
func checkProxyVM(ws *workspace.Resolved, stateDir string) error {
	running, err := vm.ReadSpec(stateDir)
	if err != nil {
		return fmt.Errorf("workspace %q: read running VM configuration: %w", ws.Name, err)
	}
	if running.Guest == nil {
		return fmt.Errorf("workspace %q: running VM has no guest configuration; run `jailoc restart %s`", ws.Name, ws.Name)
	}
	if changed := proxyChanges(ws, running.Guest); len(changed) > 0 {
		if proxyReloadable(*running) {
			return fmt.Errorf("workspace %q: the running VM does not match the configured %s; run `jailoc reload %s` first", ws.Name, joinList(changed), ws.Name)
		}
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

// proxyEndpoints is where a proxy listens and which ports browsers use: the
// two are the same only with the listener broker, or when the user forwards
// the browser ports to the local ones.
type proxyEndpoints struct {
	httpsAddr, httpAddr net.Addr
	httpPort, httpsPort int
}

// printProxyBanner prints each route's URL and its login code once, on w.
// Codes are only printed here; they appear in no log and no URL. The proxy
// accepts requests for the https_port authority only, so a listen address on
// another port needs a forward from the browser port.
func printProxyBanner(w io.Writer, ep proxyEndpoints, targets []proxyTarget, codes map[string]string) {
	printProxyBannerWithCA(w, ep, "", targets, codes)
}

func printProxyBannerWithCA(w io.Writer, ep proxyEndpoints, caPath string, targets []proxyTarget, codes map[string]string) {
	_, _ = fmt.Fprintf(w, "Proxy listening on %s (loopback only)\n", ep.httpsAddr)
	_, _ = fmt.Fprintf(w, "HTTP redirect listening on %s (loopback only)\n", ep.httpAddr)
	if caPath != "" {
		home, _ := os.UserHomeDir()
		_, _ = fmt.Fprintf(w, "CA certificate: %s\n", shortenHome(caPath, home))
	}
	if addrPort(ep.httpsAddr) != ep.httpsPort {
		_, _ = fmt.Fprintf(w, "Browsers use port %d for these URLs: forward 127.0.0.1:%d to %s, and resolve the host names to 127.0.0.1\n", ep.httpsPort, ep.httpsPort, ep.httpsAddr)
	}
	if addrPort(ep.httpAddr) != ep.httpPort {
		_, _ = fmt.Fprintf(w, "Plain http:// URLs use port %d: forward 127.0.0.1:%d to %s to get redirected to https://\n", ep.httpPort, ep.httpPort, ep.httpAddr)
	}
	for _, t := range targets {
		_, _ = fmt.Fprintf(w, "Workspace %s:\n", t.ws.Name)
		for _, r := range t.ws.Proxy {
			host := proxyHost(r.Name, t.ws.Name)
			if code := codes[host]; code != "" {
				_, _ = fmt.Fprintf(w, "  %-10s %s\n             login code %s (only valid for %s)\n", r.Name, proxyURL(host, ep.httpsPort), code, proxyOrigin(host, ep.httpsPort))
			} else {
				_, _ = fmt.Fprintf(w, "  %-10s %s\n", r.Name, proxyURL(host, ep.httpsPort))
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
	out := make([]proxy.Route, 0, len(proxyRoutes)+1)
	for _, r := range proxyRoutes {
		route := &proxyRoute{inst: i, port: uint16(r.Port), ctx: i.ctx} //nolint:gosec // G115: validated to 1-65535
		out = append(out, proxy.Route{Host: proxyHost(r.Name, i.target.ws.Name), Dial: route.dialContext, Auth: r.Auth, DisableRedirect: !r.Redirect, Generation: i.gen})
	}
	if len(proxyRoutes) > 0 {
		out = append(out, proxy.Route{Host: proxyWorkspaceHost(i.target.ws.Name), StaticBody: "OK", Generation: i.gen})
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
