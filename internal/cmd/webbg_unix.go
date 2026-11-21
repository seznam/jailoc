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
	"github.com/seznam/jailoc/internal/webgateway"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	webStartupFDEnv = "JAILOC_WEB_STARTUP_FD"
	webStartTimeout = 10 * time.Second
)

// webGatewayState is recorded in ~/.cache/jailoc-vm/web/state.json while a
// gateway process is serving.
type webGatewayState struct {
	PID            int                 `json:"pid"`
	Listen         string              `json:"listen"`
	ListenerBroker bool                `json:"listener_broker,omitempty"`
	CACert         string              `json:"ca_cert,omitempty"`
	ControlSocket  string              `json:"control_socket"`
	StartedAt      time.Time           `json:"started_at"`
	Workspaces     map[string][]string `json:"workspaces"`
}

type webControlRequest struct {
	Action     string   `json:"action"` // "sync", "status", "stop"
	Workspaces []string `json:"workspaces,omitempty"`
}

type webControlResponse struct {
	State   webGatewayState   `json:"state"`
	Codes   map[string]string `json:"codes,omitempty"`
	Notes   string            `json:"notes,omitempty"`
	Stopped bool              `json:"stopped,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func webStateDir() string {
	return filepath.Join(vmRootDir(), "web")
}

func webStateFile() string {
	return filepath.Join(webStateDir(), "state.json")
}

func webOptionsFile() string {
	return filepath.Join(webStateDir(), "options.json")
}

func webLockFile() string {
	return filepath.Join(webStateDir(), "proxy.lock")
}

func webLogFile() string {
	return filepath.Join(webStateDir(), "web.log")
}

type webSavedOptions struct {
	Listen         string `json:"listen,omitempty"`
	ListenerBroker bool   `json:"listener_broker,omitempty"`
	CertFile       string `json:"cert_file,omitempty"`
	KeyFile        string `json:"key_file,omitempty"`
}

func writeWebSavedOptions(opts gatewayOptions) error {
	if err := os.MkdirAll(webStateDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(webSavedOptions{
		Listen:         opts.listen,
		ListenerBroker: opts.listenerBroker,
		CertFile:       opts.certFile,
		KeyFile:        opts.keyFile,
	}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(webOptionsFile(), append(data, '\n'))
}

func loadWebSavedOptions() gatewayOptions {
	data, err := os.ReadFile(webOptionsFile()) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return gatewayOptions{listen: gatewayDefaultListen}
	}
	var saved webSavedOptions
	if err := json.Unmarshal(data, &saved); err != nil {
		return gatewayOptions{listen: gatewayDefaultListen}
	}
	listen := saved.Listen
	if listen == "" && !saved.ListenerBroker {
		listen = gatewayDefaultListen
	}
	return gatewayOptions{
		listen:         listen,
		listenerBroker: saved.ListenerBroker,
		certFile:       saved.CertFile,
		keyFile:        saved.KeyFile,
	}
}

func acquireWebProxyLock() (func(), error) {
	dir := webStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create web state dir: %w", err)
	}
	f, err := os.OpenFile(webLockFile(), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
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
func readWebGatewayState() (*webGatewayState, error) {
	data, err := os.ReadFile(webStateFile()) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, err
	}
	var st webGatewayState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeWebGatewayState(st webGatewayState) error {
	if err := os.MkdirAll(webStateDir(), 0o700); err != nil {
		return fmt.Errorf("create web state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(webStateFile(), append(data, '\n'))
}

func runningWebGateway() *webGatewayState {
	st, err := readWebGatewayState()
	if err != nil || st.PID <= 0 || !processAlive(st.PID) || st.ControlSocket == "" {
		return nil
	}
	if _, err := os.Stat(st.ControlSocket); err != nil {
		return nil
	}
	return st
}

// gatewayManager coordinates live route updates and control-socket requests
// for a running Gateway.
type gatewayManager struct {
	mu        sync.Mutex
	runCtx    context.Context
	cancelRun context.CancelCauseFunc
	opts      gatewayOptions
	listen    string
	caCert    string
	sockPath  string
	startedAt time.Time
	gw        *webgateway.Gateway
	instances map[string]*gatewayInstance
	wsRoutes  map[string][]workspace.ProxyRoute
	watchers  *sync.WaitGroup
	errw      io.Writer
}

func (m *gatewayManager) stateLocked() webGatewayState {
	ws := make(map[string][]string, len(m.instances))
	for _, name := range slices.Sorted(maps.Keys(m.instances)) {
		routes := m.wsRoutes[name]
		hosts := make([]string, 0, len(routes))
		for _, r := range routes {
			hosts = append(hosts, gatewayHost(r.Name, name))
		}
		ws[name] = hosts
	}
	return webGatewayState{
		PID:            os.Getpid(),
		Listen:         m.listen,
		ListenerBroker: m.opts.listenerBroker,
		CACert:         m.caCert,
		ControlSocket:  m.sockPath,
		StartedAt:      m.startedAt,
		Workspaces:     ws,
	}
}

func (m *gatewayManager) addInstanceLocked(t gatewayTarget) *gatewayInstance {
	inst := newGatewayInstance(m.runCtx, t, m.errw)
	m.instances[t.ws.Name] = inst
	if m.wsRoutes == nil {
		m.wsRoutes = make(map[string][]workspace.ProxyRoute)
	}
	m.wsRoutes[t.ws.Name] = slices.Clone(t.ws.Proxy)
	m.watchers.Add(1)
	go func(name string, i *gatewayInstance) {
		defer m.watchers.Done()
		i.watch(gatewayPollInterval)
		m.onInstanceDone(name, i)
	}(t.ws.Name, inst)
	return inst
}

func (m *gatewayManager) onInstanceDone(name string, inst *gatewayInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instances[name] != inst {
		return
	}
	delete(m.instances, name)
	delete(m.wsRoutes, name)
	if len(m.instances) == 0 {
		m.cancelRun(errGatewayNoVMs)
	}
}

func (m *gatewayManager) sync(reqNames []string) webControlResponse {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := config.Load()
	if err != nil {
		return webControlResponse{Error: fmt.Sprintf("load config: %v", err)}
	}
	var notes bytes.Buffer
	if len(reqNames) > 0 {
		// Validate the explicitly requested workspaces first.
		if _, err := selectGatewayTargets(cfg, reqNames, &notes); err != nil {
			return webControlResponse{Error: err.Error()}
		}
	}

	// Prune instances whose VM stopped, was replaced, or no longer matches config.
	for name, inst := range m.instances {
		if inst.check() != nil {
			delete(m.instances, name)
			delete(m.wsRoutes, name)
			continue
		}
		_, exists := cfg.Workspaces[name]
		if !exists {
			delete(m.instances, name)
			delete(m.wsRoutes, name)
			inst.cancel(errGatewayVMGone)
			continue
		}
		ws, err := workspace.Resolve(cfg, name)
		if err != nil || len(ws.Proxy) == 0 || checkGatewayVM(ws, inst.target.stateDir) != nil {
			delete(m.instances, name)
			delete(m.wsRoutes, name)
			inst.cancel(errGatewayVMGone)
			continue
		}
		m.wsRoutes[name] = slices.Clone(ws.Proxy)
	}

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
		if err := checkGatewayVM(ws, stateDir); err != nil {
			if len(reqNames) > 0 {
				return webControlResponse{Error: err.Error()}
			}
			continue
		}
		m.addInstanceLocked(gatewayTarget{ws: ws, stateDir: stateDir, id: id})
	}

	if len(m.instances) == 0 {
		m.cancelRun(errGatewayNoVMs)
		return webControlResponse{Stopped: true}
	}

	var targets []gatewayTarget
	var routes []webgateway.Route
	for _, name := range slices.Sorted(maps.Keys(m.instances)) {
		inst := m.instances[name]
		wsCopy := *inst.target.ws
		wsCopy.Proxy = m.wsRoutes[name]
		targets = append(targets, gatewayTarget{ws: &wsCopy, stateDir: inst.target.stateDir, id: inst.target.id})
		routes = append(routes, inst.routesFor(m.wsRoutes[name])...)
	}
	cert, caPath, err := resolveGatewayCert(m.opts, cfg, targets)
	if err != nil {
		return webControlResponse{Error: err.Error()}
	}
	m.caCert = caPath
	codes, err := m.gw.Update(webgateway.Config{Cert: cert, Routes: routes})
	if err != nil {
		return webControlResponse{Error: err.Error()}
	}
	st := m.stateLocked()
	_ = writeWebGatewayState(st)
	return webControlResponse{State: st, Codes: codes, Notes: notes.String()}
}

func openWebControlListener() (*net.UnixListener, string, func(), error) {
	dir := webStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", nil, fmt.Errorf("create web state dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // web state dir must be 0700.
		return nil, "", nil, fmt.Errorf("chmod web state dir: %w", err)
	}
	sockPath := filepath.Join(dir, "control.sock")
	cleanupDir := ""
	if len(sockPath) > 96 {
		tmpDir, err := os.MkdirTemp("/tmp", "jlweb-")
		if err != nil {
			return nil, "", nil, fmt.Errorf("create web socket dir: %w", err)
		}
		if err := os.Chmod(tmpDir, 0o700); err != nil { //nolint:gosec // temp socket dir must be 0700.
			_ = os.RemoveAll(tmpDir)
			return nil, "", nil, fmt.Errorf("chmod web socket dir: %w", err)
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
		return nil, "", nil, fmt.Errorf("listen web control socket: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ul.Close()
		if cleanupDir != "" {
			_ = os.RemoveAll(cleanupDir)
		}
		return nil, "", nil, fmt.Errorf("chmod web control socket: %w", err)
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

func (m *gatewayManager) serveControl(ul *net.UnixListener) {
	for {
		conn, err := ul.AcceptUnix()
		if err != nil {
			return
		}
		go func(c *net.UnixConn) {
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			var req webControlRequest
			if err := json.NewDecoder(c).Decode(&req); err != nil {
				return
			}
			var resp webControlResponse
			switch req.Action {
			case "status":
				m.mu.Lock()
				resp = webControlResponse{State: m.stateLocked()}
				m.mu.Unlock()
			case "stop":
				m.cancelRun(context.Canceled)
				resp = webControlResponse{Stopped: true}
			case "sync":
				resp = m.sync(req.Workspaces)
			default:
				resp = webControlResponse{Error: fmt.Sprintf("unknown action %q", req.Action)}
			}
			_ = json.NewEncoder(c).Encode(resp)
		}(conn)
	}
}

func sendWebControl(sockPath string, req webControlRequest) (webControlResponse, error) {
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return webControlResponse{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return webControlResponse{}, err
	}
	var resp webControlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return webControlResponse{}, err
	}
	return resp, nil
}

func runBackgroundGateway(cmd *cobra.Command, names []string, opts gatewayOptions) error {
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

	if st := runningWebGateway(); st != nil {
		listenChanged := (cmd.Flags().Changed("listen") && st.Listen != listen) ||
			(cmd.Flags().Changed("listener-broker") && st.ListenerBroker != opts.listenerBroker) ||
			cmd.Flags().Changed("tls-cert") || cmd.Flags().Changed("tls-key")
		if listenChanged {
			_ = stopBackgroundWebGateway()
		} else {
			resp, err := sendWebControl(st.ControlSocket, webControlRequest{Action: "sync", Workspaces: names})
			if err == nil {
				if resp.Error != "" {
					return errors.New(resp.Error)
				}
				if resp.Notes != "" {
					_, _ = io.WriteString(cmd.ErrOrStderr(), resp.Notes)
				}
				printSyncedWebGateway(cmd.OutOrStdout(), resp.State, resp.Codes, names)
				return nil
			}
			_ = stopBackgroundWebGateway()
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if _, err := selectGatewayTargets(cfg, names, io.Discard); err != nil {
		return err
	}
	return startBackgroundWebGateway(ctx, names, opts, cmd.OutOrStdout())
}

func startBackgroundWebGateway(ctx context.Context, names []string, opts gatewayOptions, out io.Writer) error {
	if err := os.MkdirAll(webStateDir(), 0o700); err != nil {
		return fmt.Errorf("create web state dir: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate jailoc executable: %w", err)
	}
	logFile, err := os.OpenFile(webLogFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open web gateway log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	_ = os.Remove(webStateFile())

	bannerR, bannerW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create startup pipe: %w", err)
	}
	defer func() { _ = bannerR.Close() }()

	childArgs := []string{"--config", config.ConfigPath(), "proxy", "--foreground"}
	if opts.listenerBroker {
		childArgs = append(childArgs, "--listener-broker")
	} else if opts.listen != "" {
		childArgs = append(childArgs, "--listen", opts.listen)
	}
	if opts.certFile != "" {
		childArgs = append(childArgs, "--tls-cert", opts.certFile, "--tls-key", opts.keyFile)
	}
	if len(names) > 0 {
		childArgs = append(childArgs, "--")
		childArgs = append(childArgs, names...)
	}

	child := exec.Command(exe, childArgs...) //nolint:gosec // G204: re-executes jailoc itself
	child.Stdout = logFile
	child.Stderr = logFile
	child.ExtraFiles = []*os.File{bannerW}
	child.Env = append(os.Environ(), webStartupFDEnv+"=3")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		_ = bannerW.Close()
		return fmt.Errorf("start web gateway: %w", err)
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

	timer := time.NewTimer(webStartTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		_ = child.Process.Signal(syscall.SIGTERM)
		return ctx.Err()
	case <-timer.C:
		_ = child.Process.Signal(syscall.SIGTERM)
		return fmt.Errorf("web gateway did not start within %s", webStartTimeout)
	case err := <-exited:
		logBytes, _ := os.ReadFile(webLogFile()) //nolint:gosec // G304: jailoc-owned log path
		msg := strings.TrimSpace(string(logBytes))
		if msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("web gateway exited: %w", err)
	case res := <-bannerCh:
		if len(res.data) == 0 {
			err := <-exited
			logBytes, _ := os.ReadFile(webLogFile()) //nolint:gosec // G304: jailoc-owned log path
			msg := strings.TrimSpace(string(logBytes))
			if msg != "" {
				return errors.New(msg)
			}
			if err != nil {
				return fmt.Errorf("web gateway exited: %w", err)
			}
		}
		_, _ = out.Write(res.data)
		return nil
	}
}

func stopBackgroundWebGateway() error {
	st, err := readWebGatewayState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if st.ControlSocket != "" {
		_, _ = sendWebControl(st.ControlSocket, webControlRequest{Action: "stop"})
	}
	if st.PID > 0 && st.PID != os.Getpid() && processAlive(st.PID) {
		_ = syscall.Kill(st.PID, syscall.SIGTERM)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && processAlive(st.PID) {
			time.Sleep(25 * time.Millisecond)
		}
		if processAlive(st.PID) {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	}
	_ = os.Remove(webStateFile())
	if st.ControlSocket != "" {
		_ = os.Remove(st.ControlSocket)
	}
	return nil
}

func printSyncedWebGateway(w io.Writer, st webGatewayState, codes map[string]string, filter []string) {
	_, _ = fmt.Fprintf(w, "Proxy running on %s (pid %d, loopback only)\n", st.Listen, st.PID)
	if st.CACert != "" {
		home, _ := os.UserHomeDir()
		_, _ = fmt.Fprintf(w, "CA certificate: %s\n", shortenHome(st.CACert, home))
	}
	wsNames := slices.Sorted(maps.Keys(st.Workspaces))
	if len(filter) > 0 {
		wsNames = slices.DeleteFunc(wsNames, func(name string) bool {
			return !slices.Contains(filter, name)
		})
	}
	for _, ws := range wsNames {
		_, _ = fmt.Fprintf(w, "Workspace %s:\n", ws)
		for _, host := range st.Workspaces[ws] {
			if code, ok := codes[host]; ok && code != "" {
				_, _ = fmt.Fprintf(w, "  https://%s/  login code %s (only valid for https://%s)\n", host, code, host)
			} else {
				_, _ = fmt.Fprintf(w, "  https://%s/  (active; existing session preserved)\n", host)
			}
		}
	}
}

func newWebStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show the status of the background web gateway",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := runningWebGateway()
			if st == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Web gateway is not running")
				return nil
			}
			if resp, err := sendWebControl(st.ControlSocket, webControlRequest{Action: "status"}); err == nil && !resp.Stopped {
				st = &resp.State
			}
			printSyncedWebGateway(cmd.OutOrStdout(), *st, nil, nil)
			return nil
		},
	}
}

func newWebStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "stop",
		Aliases:      []string{"down"},
		Short:        "Stop the background web gateway",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := runningWebGateway()
			if st == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Web gateway is not running")
				return nil
			}
			if err := stopBackgroundWebGateway(); err != nil {
				return err
			}
			_ = os.Remove(webOptionsFile())
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Web gateway stopped")
			return nil
		},
	}
}

// autoManageWebOnUp starts or syncs the background proxy when ws has proxy routes.
func autoManageWebOnUp(ctx context.Context, ws *workspace.Resolved, out, errw io.Writer) {
	if len(ws.Proxy) == 0 {
		return
	}
	if st := runningWebGateway(); st != nil {
		resp, err := sendWebControl(st.ControlSocket, webControlRequest{Action: "sync", Workspaces: []string{ws.Name}})
		if err == nil {
			if resp.Error != "" {
				_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: could not sync background proxy (%s); run `jailoc proxy %s`\n", resp.Error, ws.Name)
				return
			}
			printSyncedWebGateway(out, resp.State, resp.Codes, []string{ws.Name})
			return
		}
	}
	if err := startBackgroundWebGateway(ctx, []string{ws.Name}, loadWebSavedOptions(), out); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: could not start background proxy (%v); run `jailoc proxy %s`\n", err, ws.Name)
	}
}

// syncWebGatewayAfterDown updates the running background proxy after a
// workspace VM stops, or stops the proxy when no proxy workspaces remain.
func syncWebGatewayAfterDown() {
	st := runningWebGateway()
	if st == nil {
		return
	}
	if resp, err := sendWebControl(st.ControlSocket, webControlRequest{Action: "sync"}); err == nil && resp.Stopped {
		_ = os.Remove(webStateFile())
	}
}

// webStatusLabel describes the proxy routes and background proxy state for
// `jailoc status`.
func webStatusLabel(ws *workspace.Resolved) string {
	if len(ws.Proxy) == 0 {
		return ""
	}
	urls := make([]string, 0, len(ws.Proxy))
	for _, r := range ws.Proxy {
		urls = append(urls, fmt.Sprintf("https://%s -> %d", gatewayHost(r.Name, ws.Name), r.Port))
	}
	st := runningWebGateway()
	if st != nil {
		if _, ok := st.Workspaces[ws.Name]; ok {
			return fmt.Sprintf("running on %s (pid %d); %s", st.Listen, st.PID, strings.Join(urls, ", "))
		}
	}
	return fmt.Sprintf("proxy stopped (`jailoc proxy %s` starts it); %s", ws.Name, strings.Join(urls, ", "))
}

// startupBannerWriter returns the startup pipe writer when the process was
// spawned by startBackgroundWebGateway, or nil for a foreground run.
func startupBannerWriter() *os.File {
	v := os.Getenv(webStartupFDEnv)
	if v == "" {
		return nil
	}
	fd, err := strconv.Atoi(v)
	if err != nil || fd <= 2 {
		return nil
	}
	return os.NewFile(uintptr(fd), "web-startup") //nolint:gosec // G115: validated positive fd
}
