//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/proxy"
	"github.com/seznam/jailoc/internal/proxyhost"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	proxyStartupFDEnv = "JAILOC_WEB_STARTUP_FD"
	proxyStartTimeout = 10 * time.Second
)

// proxyState is recorded in ~/.cache/jailoc-vm/proxy/state.json while a
// proxy process is serving.
type proxyState struct {
	PID int `json:"pid"`
	// Listen is the local HTTPS address, RedirectListen the local address of
	// the plain HTTP redirect. A state from before the redirect has none.
	Listen         string `json:"listen"`
	RedirectListen string `json:"redirect_listen,omitempty"`
	// ListenOverride is true when --listen chose Listen.
	ListenOverride bool `json:"listen_override,omitempty"`
	ListenerBroker bool `json:"listener_broker,omitempty"`
	// The ports are the [proxy] ports the proxy started with: the browser
	// authority ports and the standalone ports it binds. A state from before
	// they were configurable has none, and served 443.
	HTTPPort            int                 `json:"http_port,omitempty"`
	HTTPSPort           int                 `json:"https_port,omitempty"`
	StandaloneHTTPPort  int                 `json:"standalone_http_port,omitempty"`
	StandaloneHTTPSPort int                 `json:"standalone_https_port,omitempty"`
	CACert              string              `json:"ca_cert,omitempty"`
	Config              string              `json:"config,omitempty"`
	ControlSocket       string              `json:"control_socket"`
	StartedAt           time.Time           `json:"started_at"`
	Workspaces          map[string][]string `json:"workspaces"`
}

// httpsPort is the port of the HTTPS authority the proxy serves.
func (st proxyState) httpsPort() int {
	if st.HTTPSPort == 0 {
		return config.DefaultProxyHTTPSPort
	}
	return st.HTTPSPort
}

// portChanges names the [proxy] settings that differ between the ports the
// proxy started with and want, which a running proxy cannot adopt: it keeps
// the authority ports of its routes and the addresses it bound until it is
// restarted. A proxy from before the ports were configurable served the
// default authority and is compared on that only. Standalone ports count only
// for what the proxy bound: nothing with the listener broker, and no HTTPS
// port when --listen chose it.
func (st proxyState) portChanges(want config.ProxyPorts) []string {
	var changed []string
	if st.HTTPSPort == 0 {
		if want.HTTP != config.DefaultProxyHTTPPort {
			changed = append(changed, "http_port")
		}
		if want.HTTPS != config.DefaultProxyHTTPSPort {
			changed = append(changed, "https_port")
		}
		return changed
	}
	if st.HTTPPort != want.HTTP {
		changed = append(changed, "http_port")
	}
	if st.HTTPSPort != want.HTTPS {
		changed = append(changed, "https_port")
	}
	if st.ListenerBroker {
		return changed
	}
	if st.StandaloneHTTPPort != want.StandaloneHTTP {
		changed = append(changed, "standalone_http_port")
	}
	if !st.ListenOverride && st.StandaloneHTTPSPort != want.StandaloneHTTPS {
		changed = append(changed, "standalone_https_port")
	}
	return changed
}

type proxyControlRequest struct {
	Action     string   `json:"action"` // "sync", "prune", "status", "stop"
	Workspaces []string `json:"workspaces,omitempty"`
	// Config is the caller's configuration file. A proxy serves the
	// workspaces of one configuration and rejects a sync for another.
	Config string `json:"config,omitempty"`
}

type proxyControlResponse struct {
	State   proxyState        `json:"state"`
	Codes   map[string]string `json:"codes,omitempty"`
	Notes   string            `json:"notes,omitempty"`
	Stopped bool              `json:"stopped,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func proxyStateDir() string {
	return filepath.Join(vmRootDir(), "proxy")
}

func proxyStateFile() string {
	return filepath.Join(proxyStateDir(), "state.json")
}

func proxyOptionsFile() string {
	return filepath.Join(proxyStateDir(), "options.json")
}

func proxyLockFile() string {
	return filepath.Join(proxyStateDir(), "proxy.lock")
}

func proxyLogFile() string {
	return filepath.Join(proxyStateDir(), "proxy.log")
}

type proxySavedOptions struct {
	Listen         string `json:"listen,omitempty"`
	ListenerBroker bool   `json:"listener_broker,omitempty"`
}

func writeProxySavedOptions(opts proxyOptions) error {
	if err := os.MkdirAll(proxyStateDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(proxySavedOptions{
		Listen:         opts.listen,
		ListenerBroker: opts.listenerBroker,
	}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(proxyOptionsFile(), append(data, '\n'))
}

func loadProxySavedOptions() proxyOptions {
	data, err := os.ReadFile(proxyOptionsFile()) //nolint:gosec // G304: jailoc-owned state path
	if errors.Is(err, os.ErrNotExist) && (proxyServiceInstalled() || proxyBrokerAnswers()) {
		// A host set up with `jailoc proxy install` needs no saved options,
		// and a broker that is down right now is still the installed one.
		// A broker started by hand has no install state, so it is probed.
		return proxyOptions{listenerBroker: true}
	}
	if err != nil {
		return proxyOptions{}
	}
	var saved proxySavedOptions
	if err := json.Unmarshal(data, &saved); err != nil {
		return proxyOptions{}
	}
	return proxyOptions{
		listen:         saved.Listen,
		listenerBroker: saved.ListenerBroker,
	}
}

// proxyServiceInstalled reports whether `jailoc proxy install` recorded the
// listener broker service. It reads the install state only: it does not ask
// the broker, which may be stopped or restarting.
func proxyServiceInstalled() bool {
	st, err := proxyhost.ReadInstallState(proxyInstallStatePath())
	return err == nil && (st.HelperPath != "" || st.ServiceUnitPath != "")
}

// proxyBrokerAnswers reports whether the listener broker of the running user
// answers on its control socket.
func proxyBrokerAnswers() bool {
	ports, err := loadProxyPorts()
	if err != nil {
		return false
	}
	t := proxyBrokerHost(ports)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return proxyhost.ProbeBroker(ctx, t.socketPath, t.brokerUID, t.httpsAddr) == nil
}

func acquireProxyLock() (func(), error) {
	dir := proxyStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create proxy state dir: %w", err)
	}
	f, err := os.OpenFile(proxyLockFile(), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("open proxy lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: file descriptors fit in int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New("another jailoc proxy is already running")
		}
		return nil, fmt.Errorf("lock proxy state: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // G115: file descriptors fit in int
		_ = f.Close()
	}, nil
}

// proxyLockHeld reports whether a proxy process holds proxy.lock. A proxy
// keeps the lock while it serves, so a free lock means state.json is stale
// and its PID may already belong to an unrelated process.
func proxyLockHeld() bool {
	f, err := os.OpenFile(proxyLockFile(), os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: file descriptors fit in int
		return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // G115: file descriptors fit in int
	return false
}

// proxyConfigPath identifies the configuration a proxy serves.
func proxyConfigPath() string {
	p := config.ConfigPath()
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func readProxyState() (*proxyState, error) {
	data, err := os.ReadFile(proxyStateFile()) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, err
	}
	var st proxyState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeProxyState(st proxyState) error {
	if err := os.MkdirAll(proxyStateDir(), 0o700); err != nil {
		return fmt.Errorf("create proxy state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode proxy state: %w", err)
	}
	return writePrivateFile(proxyStateFile(), append(data, '\n'))
}

func runningProxy() *proxyState {
	st, err := readProxyState()
	if err != nil || st.PID <= 0 || !processAlive(st.PID) || st.ControlSocket == "" || !proxyLockHeld() {
		return nil
	}
	if _, err := os.Stat(st.ControlSocket); err != nil {
		return nil
	}
	return st
}

// proxyManager coordinates live route updates and control-socket requests
// for a running proxy Server.
type proxyManager struct {
	mu        sync.Mutex
	runCtx    context.Context
	cancelRun context.CancelCauseFunc
	opts      proxyOptions
	ports     config.ProxyPorts
	listen    string
	// redirectListen is the local address of the HTTP redirect.
	redirectListen string
	caCert         string
	// configPath is the configuration file this proxy loads on every sync.
	configPath string
	sockPath   string
	startedAt  time.Time
	gw         *proxy.Server
	instances  map[string]*proxyInstance
	wsRoutes   map[string][]workspace.ProxyRoute
	watchers   *sync.WaitGroup
	errw       io.Writer
}

func (m *proxyManager) stateLocked() proxyState {
	ws := make(map[string][]string, len(m.instances))
	for _, name := range slices.Sorted(maps.Keys(m.instances)) {
		routes := m.wsRoutes[name]
		hosts := make([]string, 0, len(routes))
		for _, r := range routes {
			hosts = append(hosts, proxyHost(r.Name, name))
		}
		ws[name] = hosts
	}
	return proxyState{
		PID:                 os.Getpid(),
		Listen:              m.listen,
		RedirectListen:      m.redirectListen,
		ListenOverride:      m.opts.listen != "" && !m.opts.listenerBroker,
		ListenerBroker:      m.opts.listenerBroker,
		HTTPPort:            m.ports.HTTP,
		HTTPSPort:           m.ports.HTTPS,
		StandaloneHTTPPort:  m.ports.StandaloneHTTP,
		StandaloneHTTPSPort: m.ports.StandaloneHTTPS,
		CACert:              m.caCert,
		Config:              m.configPath,
		ControlSocket:       m.sockPath,
		StartedAt:           m.startedAt,
		Workspaces:          ws,
	}
}

func (m *proxyManager) addInstanceLocked(t proxyTarget) *proxyInstance {
	inst := newProxyInstance(m.runCtx, t, m.errw)
	m.instances[t.ws.Name] = inst
	if m.wsRoutes == nil {
		m.wsRoutes = make(map[string][]workspace.ProxyRoute)
	}
	m.wsRoutes[t.ws.Name] = slices.Clone(t.ws.Proxy)
	m.watchers.Add(1)
	go func(name string, i *proxyInstance) {
		defer m.watchers.Done()
		i.watch(proxyPollInterval)
		m.onInstanceDone(name, i)
	}(t.ws.Name, inst)
	return inst
}

func (m *proxyManager) onInstanceDone(name string, inst *proxyInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instances[name] != inst {
		return
	}
	delete(m.instances, name)
	delete(m.wsRoutes, name)
	if len(m.instances) == 0 {
		m.cancelRun(errProxyNoVMs)
	}
}

func (m *proxyManager) checkConfig(path string) error {
	if path == "" || path == m.configPath {
		return nil
	}
	return fmt.Errorf("the background proxy serves workspaces of %s, not %s; stop it with `jailoc proxy stop` first", m.configPath, path)
}

// sync reloads the configuration, refreshes the routes of served workspaces,
// adds running workspaces with routes (only the requested ones, if any) and
// applies the result.
func (m *proxyManager) sync(req proxyControlRequest) proxyControlResponse {
	if err := m.checkConfig(req.Config); err != nil {
		return proxyControlResponse{Error: err.Error()}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := config.Load()
	if err != nil {
		return proxyControlResponse{Error: fmt.Sprintf("load config: %v", err)}
	}
	// A running proxy keeps the ports and addresses it started with, so a
	// changed one is refused before anything else changes: the routes would
	// otherwise be refreshed under the old scope.
	if changed := m.stateLocked().portChanges(cfg.Proxy.Ports()); len(changed) > 0 {
		return proxyControlResponse{Error: proxyPortsChangedError(changed).Error()}
	}
	reqNames := req.Workspaces
	var notes bytes.Buffer
	if len(reqNames) > 0 {
		// Validate the explicitly requested workspaces first.
		if _, err := selectProxyTargets(cfg, reqNames, &notes); err != nil {
			return proxyControlResponse{Error: err.Error()}
		}
	}
	m.pruneLocked(cfg, true)

	// Discover all running workspaces with routes (or ensure reqNames are included).
	discoverNames := reqNames
	if len(discoverNames) == 0 {
		discoverNames = slices.Sorted(maps.Keys(cfg.Workspaces))
	} else {
		discoverNames = slices.Compact(slices.Sorted(slices.Values(discoverNames)))
	}
	for _, name := range discoverNames {
		if _, already := m.instances[name]; already {
			continue
		}
		ws, err := workspace.Resolve(cfg, name)
		if err != nil || len(ws.Proxy) == 0 {
			continue
		}
		stateDir := vmStateDir(name)
		id, err := readVMIdentity(stateDir)
		if err != nil {
			continue
		}
		if err := checkProxyVM(ws, stateDir); err != nil {
			if len(reqNames) > 0 {
				return proxyControlResponse{Error: err.Error()}
			}
			continue
		}
		m.addInstanceLocked(proxyTarget{ws: ws, stateDir: stateDir, id: id})
	}
	return m.applyLocked(cfg, notes.String())
}

// prune drops workspaces whose VM stopped or whose routes were removed. It
// neither adds workspaces nor changes the routes of the others, so it never
// issues login codes that nobody would see.
func (m *proxyManager) prune() proxyControlResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return proxyControlResponse{Error: fmt.Sprintf("load config: %v", err)}
	}
	if !m.pruneLocked(cfg, false) {
		return proxyControlResponse{State: m.stateLocked()}
	}
	return m.applyLocked(cfg, "")
}

// pruneLocked removes instances whose VM stopped, was replaced, or no longer
// has routes in cfg. With refresh, the remaining instances take their routes
// from cfg. It reports whether it removed an instance.
func (m *proxyManager) pruneLocked(cfg *config.Config, refresh bool) bool {
	removed := false
	for name, inst := range m.instances {
		if inst.check() != nil {
			delete(m.instances, name)
			delete(m.wsRoutes, name)
			removed = true
			continue
		}
		var ws *workspace.Resolved
		_, exists := cfg.Workspaces[name]
		if exists {
			ws, _ = workspace.Resolve(cfg, name) // an unresolvable workspace is dropped below
		}
		// Only a refresh installs the configured routes, so only a refresh
		// needs the running VM to match them; a prune keeps the routes the
		// instance was added with.
		if ws == nil || len(ws.Proxy) == 0 || (refresh && checkProxyVM(ws, inst.target.stateDir) != nil) {
			delete(m.instances, name)
			delete(m.wsRoutes, name)
			inst.cancel(errProxyVMGone)
			removed = true
			continue
		}
		if refresh {
			m.wsRoutes[name] = slices.Clone(ws.Proxy)
		}
	}
	return removed
}

// applyLocked installs the routes of the current instances, or stops the
// proxy when none remain.
func (m *proxyManager) applyLocked(cfg *config.Config, notes string) proxyControlResponse {
	if len(m.instances) == 0 {
		m.cancelRun(errProxyNoVMs)
		return proxyControlResponse{Stopped: true}
	}

	var targets []proxyTarget
	var routes []proxy.Route
	for _, name := range slices.Sorted(maps.Keys(m.instances)) {
		inst := m.instances[name]
		wsCopy := *inst.target.ws
		wsCopy.Proxy = m.wsRoutes[name]
		targets = append(targets, proxyTarget{ws: &wsCopy, stateDir: inst.target.stateDir, id: inst.target.id})
		routes = append(routes, inst.routesFor(m.wsRoutes[name])...)
	}
	cert, caPath, err := ensureProxyCert(cfg, targets)
	if err != nil {
		return proxyControlResponse{Error: err.Error()}
	}
	m.caCert = caPath
	codes, err := m.gw.Update(proxy.Config{Cert: cert, Routes: routes, HTTPPort: m.ports.HTTP, HTTPSPort: m.ports.HTTPS})
	if err != nil {
		return proxyControlResponse{Error: err.Error()}
	}
	st := m.stateLocked()
	_ = writeProxyState(st)
	return proxyControlResponse{State: st, Codes: codes, Notes: notes}
}

func openProxyControlListener() (*net.UnixListener, string, func(), error) {
	dir := proxyStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", nil, fmt.Errorf("create proxy state dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // proxy state dir must be 0700.
		return nil, "", nil, fmt.Errorf("chmod proxy state dir: %w", err)
	}
	sockPath := filepath.Join(dir, "control.sock")
	cleanupDir := ""
	if len(sockPath) > 96 {
		tmpDir, err := os.MkdirTemp("/tmp", "jlproxy-")
		if err != nil {
			return nil, "", nil, fmt.Errorf("create proxy socket dir: %w", err)
		}
		if err := os.Chmod(tmpDir, 0o700); err != nil { //nolint:gosec // temp socket dir must be 0700.
			_ = os.RemoveAll(tmpDir)
			return nil, "", nil, fmt.Errorf("chmod proxy socket dir: %w", err)
		}
		cleanupDir = tmpDir
		sockPath = filepath.Join(tmpDir, "control.sock")
	}
	_ = os.Remove(sockPath)
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		if cleanupDir != "" {
			_ = os.RemoveAll(cleanupDir)
		}
		return nil, "", nil, fmt.Errorf("listen proxy control socket: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ul.Close()
		if cleanupDir != "" {
			_ = os.RemoveAll(cleanupDir)
		}
		return nil, "", nil, fmt.Errorf("chmod proxy control socket: %w", err)
	}
	cleanup := func() {
		_ = ul.Close()
		_ = os.Remove(sockPath)
		if cleanupDir != "" {
			_ = os.RemoveAll(cleanupDir)
		}
	}
	return ul, sockPath, cleanup, nil
}

func (m *proxyManager) serveControl(ul *net.UnixListener) {
	for {
		conn, err := ul.AcceptUnix()
		if err != nil {
			return
		}
		go func(c *net.UnixConn) {
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			var req proxyControlRequest
			if err := json.NewDecoder(c).Decode(&req); err != nil {
				return
			}
			var resp proxyControlResponse
			switch req.Action {
			case "status":
				m.mu.Lock()
				resp = proxyControlResponse{State: m.stateLocked()}
				m.mu.Unlock()
			case "stop":
				m.cancelRun(context.Canceled)
				resp = proxyControlResponse{Stopped: true}
			case "sync":
				resp = m.sync(req)
			case "prune":
				resp = m.prune()
			default:
				resp = proxyControlResponse{Error: fmt.Sprintf("unknown action %q", req.Action)}
			}
			_ = json.NewEncoder(c).Encode(resp)
		}(conn)
	}
}

func sendProxyControl(sockPath string, req proxyControlRequest) (proxyControlResponse, error) {
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return proxyControlResponse{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return proxyControlResponse{}, err
	}
	var resp proxyControlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return proxyControlResponse{}, err
	}
	return resp, nil
}

// proxyPortsChangedError tells that a running proxy cannot adopt the changed
// [proxy] settings.
func proxyPortsChangedError(changed []string) error {
	return fmt.Errorf("the running proxy was started with other [proxy] ports (%s changed); run `jailoc proxy` to restart it", strings.Join(changed, ", "))
}

func runBackgroundProxy(cmd *cobra.Command, names []string, opts proxyOptions) error {
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

	if st := runningProxy(); st != nil {
		if st.Config != "" && st.Config != proxyConfigPath() {
			return fmt.Errorf("the background proxy serves workspaces of %s, not %s; stop it with `jailoc proxy stop` first", st.Config, proxyConfigPath())
		}
		portChanges := st.portChanges(ports)
		listenChanged := (cmd.Flags().Changed("listen") && st.Listen != listen) ||
			(cmd.Flags().Changed("listener-broker") && st.ListenerBroker != opts.listenerBroker)
		if len(portChanges) > 0 || listenChanged {
			// Restarting ends the sessions of the running proxy, so a
			// broker that would refuse the new one is found out first.
			if opts.listenerBroker {
				if err := probeProxyBroker(ctx, ports); err != nil {
					return err
				}
			}
			if len(portChanges) > 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Restarting the background proxy: %s changed\n", strings.Join(portChanges, ", "))
			}
			_ = stopBackgroundProxy()
		} else {
			resp, err := sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "sync", Workspaces: names, Config: proxyConfigPath()})
			if err == nil {
				if resp.Error != "" {
					return errors.New(resp.Error)
				}
				if resp.Notes != "" {
					_, _ = io.WriteString(cmd.ErrOrStderr(), resp.Notes)
				}
				printSyncedProxy(cmd.OutOrStdout(), resp.State, resp.Codes, names)
				return nil
			}
			_ = stopBackgroundProxy()
		}
	}

	if _, err := selectProxyTargets(cfg, names, io.Discard); err != nil {
		return err
	}
	return startBackgroundProxy(ctx, names, opts, cmd.OutOrStdout())
}

func startBackgroundProxy(ctx context.Context, names []string, opts proxyOptions, out io.Writer) error {
	if err := os.MkdirAll(proxyStateDir(), 0o700); err != nil {
		return fmt.Errorf("create proxy state dir: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate jailoc executable: %w", err)
	}
	logFile, err := os.OpenFile(proxyLogFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open proxy log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	// state.json stays: the child replaces it once it holds proxy.lock, and
	// removing it here could hide a proxy that won a concurrent start.

	bannerR, bannerW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create startup pipe: %w", err)
	}
	defer func() { _ = bannerR.Close() }()

	childArgs := []string{"--config", proxyConfigPath(), "proxy", "--foreground"}
	if opts.listenerBroker {
		childArgs = append(childArgs, "--listener-broker")
	} else if opts.listen != "" {
		childArgs = append(childArgs, "--listen", opts.listen)
	}
	if len(names) > 0 {
		childArgs = append(childArgs, "--")
		childArgs = append(childArgs, names...)
	}

	child := exec.Command(exe, childArgs...) //nolint:gosec // G204: re-executes jailoc itself
	child.Stdout = logFile
	child.Stderr = logFile
	child.ExtraFiles = []*os.File{bannerW}
	child.Env = append(os.Environ(), proxyStartupFDEnv+"=3")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		_ = bannerW.Close()
		return fmt.Errorf("start proxy: %w", err)
	}
	_ = bannerW.Close()

	type readResult struct {
		data []byte
		err  error
	}
	bannerCh := make(chan readResult, 1)
	go func() {
		b, rerr := io.ReadAll(bannerR)
		bannerCh <- readResult{b, rerr}
	}()
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()

	timer := time.NewTimer(proxyStartTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		_ = child.Process.Signal(syscall.SIGTERM)
		return ctx.Err()
	case <-timer.C:
		_ = child.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("proxy did not start within %s", proxyStartTimeout)
	case err := <-exited:
		logBytes, _ := os.ReadFile(proxyLogFile()) //nolint:gosec // G304: jailoc-owned log path
		msg := strings.TrimSpace(string(logBytes))
		if msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("proxy exited: %w", err)
	case res := <-bannerCh:
		if len(res.data) == 0 {
			err := <-exited
			logBytes, _ := os.ReadFile(proxyLogFile()) //nolint:gosec // G304: jailoc-owned log path
			msg := strings.TrimSpace(string(logBytes))
			if msg != "" {
				return errors.New(msg)
			}
			if err != nil {
				return fmt.Errorf("proxy exited: %w", err)
			}
		}
		_, _ = out.Write(res.data)
		return nil
	}
}

func stopBackgroundProxy() error {
	st, err := readProxyState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if st.ControlSocket != "" {
		_, _ = sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "stop"})
	}
	// Signal the recorded PID only while a proxy holds proxy.lock: after a
	// crash the lock is free and the PID may belong to an unrelated process.
	if waitProxyLockReleased(3*time.Second) || st.PID <= 0 || st.PID == os.Getpid() {
		return removeProxyState(st)
	}
	if processAlive(st.PID) {
		_ = syscall.Kill(st.PID, syscall.SIGTERM)
	}
	if !waitProxyLockReleased(3*time.Second) && processAlive(st.PID) {
		_ = syscall.Kill(st.PID, syscall.SIGKILL)
	}
	return removeProxyState(st)
}

// waitProxyLockReleased waits up to timeout for no proxy to hold proxy.lock.
func waitProxyLockReleased(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for proxyLockHeld() {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
	return true
}

func removeProxyState(st *proxyState) error {
	_ = os.Remove(proxyStateFile())
	if st.ControlSocket != "" {
		_ = os.Remove(st.ControlSocket)
	}
	return nil
}

func printSyncedProxy(w io.Writer, st proxyState, codes map[string]string, filter []string) {
	_, _ = fmt.Fprintf(w, "Proxy running on %s (pid %d, loopback only)\n", st.Listen, st.PID)
	if st.RedirectListen != "" {
		_, _ = fmt.Fprintf(w, "HTTP redirect running on %s\n", st.RedirectListen)
	}
	if st.CACert != "" {
		home, _ := os.UserHomeDir()
		_, _ = fmt.Fprintf(w, "CA certificate: %s\n", shortenHome(st.CACert, home))
	}
	// A sync can issue codes for workspaces outside the filter (for example
	// when another workspace's route turned on auth). Only hashes are kept,
	// so a workspace with a new code is always printed.
	wsNames := slices.Sorted(maps.Keys(st.Workspaces))
	if len(filter) > 0 {
		wsNames = slices.DeleteFunc(wsNames, func(name string) bool {
			return !slices.Contains(filter, name) && !hasProxyCode(st.Workspaces[name], codes)
		})
	}
	for _, ws := range wsNames {
		_, _ = fmt.Fprintf(w, "Workspace %s:\n", ws)
		for _, host := range st.Workspaces[ws] {
			if code, ok := codes[host]; ok && code != "" {
				_, _ = fmt.Fprintf(w, "  %s  login code %s (only valid for %s)\n", proxyURL(host, st.httpsPort()), code, proxyOrigin(host, st.httpsPort()))
			} else {
				_, _ = fmt.Fprintf(w, "  %s  (active; existing session preserved)\n", proxyURL(host, st.httpsPort()))
			}
		}
	}
}

func hasProxyCode(hosts []string, codes map[string]string) bool {
	return slices.ContainsFunc(hosts, func(host string) bool { return codes[host] != "" })
}

func newProxyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show the status of the background proxy and of the host setup",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if st := runningProxy(); st == nil {
				_, _ = fmt.Fprintln(out, "Proxy is not running")
			} else {
				if resp, err := sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "status"}); err == nil && !resp.Stopped {
					st = &resp.State
				}
				printSyncedProxy(out, *st, nil, nil)
			}
			printProxyHostStatus(cmd.Context(), out)
			return nil
		},
	}
}

func newProxyStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "stop",
		Aliases:      []string{"down"},
		Short:        "Stop the background proxy",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			running := runningProxy() != nil
			// Also clears the state a crashed proxy left behind.
			if err := stopBackgroundProxy(); err != nil {
				return err
			}
			if err := os.Remove(proxyOptionsFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove saved proxy options: %w", err)
			}
			if !running {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Proxy is not running")
				return nil
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Proxy stopped")
			return nil
		},
	}
}

// autoManageProxyOnUp starts or syncs the background proxy when ws has proxy routes.
func autoManageProxyOnUp(ctx context.Context, ws *workspace.Resolved, out, errw io.Writer) {
	if len(ws.Proxy) == 0 {
		return
	}
	if st := runningProxy(); st != nil {
		resp, err := sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "sync", Workspaces: []string{ws.Name}, Config: proxyConfigPath()})
		if err == nil {
			if resp.Error != "" {
				_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: could not sync background proxy (%s); run `jailoc proxy %s`\n", resp.Error, ws.Name)
				return
			}
			printSyncedProxy(out, resp.State, resp.Codes, []string{ws.Name})
			warnMissingProxyHosts(errw, ws)
			return
		}
	}
	if err := startBackgroundProxy(ctx, []string{ws.Name}, loadProxySavedOptions(), out); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: could not start background proxy (%v); run `jailoc proxy %s`\n", err, ws.Name)
		return
	}
	warnMissingProxyHosts(errw, ws)
}

// warnMissingProxyHosts warns on errw when /etc/hosts lacks a host name of
// ws. An unreadable hosts file is not worth a warning.
func warnMissingProxyHosts(errw io.Writer, ws *workspace.Resolved) {
	data, err := os.ReadFile(proxyHostsFilePath()) //nolint:gosec // G304: the system hosts file
	if err != nil {
		return
	}
	missing, err := proxyhost.CheckHostsContent(data, workspaceProxyHostNames(ws))
	if err != nil || len(missing) == 0 {
		return
	}
	_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: /etc/hosts is missing proxy hostnames (%s); run 'jailoc proxy install' to update /etc/hosts\n", strings.Join(missing, ", "))
}

// pruneBackgroundProxy drops the routes of stopped workspaces, or of
// workspaces without routes, from the running background proxy. The proxy
// stops when no workspace remains.
func pruneBackgroundProxy() {
	st := runningProxy()
	if st == nil {
		return
	}
	if resp, err := sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "prune"}); err == nil && resp.Stopped {
		_ = os.Remove(proxyStateFile())
	}
}

// proxyStatusLabel describes the proxy routes and background proxy state for
// `jailoc status`.
func proxyStatusLabel(ws *workspace.Resolved) string {
	if len(ws.Proxy) == 0 {
		return ""
	}
	st := runningProxy()
	httpsPort := ws.ProxyPorts.HTTPS
	if st != nil {
		httpsPort = st.httpsPort()
	}
	urls := make([]string, 0, len(ws.Proxy))
	for _, r := range ws.Proxy {
		urls = append(urls, fmt.Sprintf("%s -> %d", strings.TrimSuffix(proxyURL(proxyHost(r.Name, ws.Name), httpsPort), "/"), r.Port))
	}
	if st != nil {
		if _, ok := st.Workspaces[ws.Name]; ok {
			return fmt.Sprintf("running on %s (pid %d); %s", st.Listen, st.PID, strings.Join(urls, ", "))
		}
	}
	return fmt.Sprintf("proxy stopped (`jailoc proxy %s` starts it); %s", ws.Name, strings.Join(urls, ", "))
}

// startupBannerWriter returns the startup pipe writer when the process was
// spawned by startBackgroundProxy, or nil for a foreground run.
func startupBannerWriter() *os.File {
	v := os.Getenv(proxyStartupFDEnv)
	if v == "" {
		return nil
	}
	fd, err := strconv.Atoi(v)
	if err != nil || fd <= 2 {
		return nil
	}
	return os.NewFile(uintptr(fd), "proxy-startup") //nolint:gosec // G115: validated positive fd
}
