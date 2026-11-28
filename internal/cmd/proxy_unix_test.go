//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/proxyhost"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestParseProxyListen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "127.0.0.1:8443", want: "127.0.0.1:8443"},
		{in: "127.0.0.1:0", want: "127.0.0.1:0"},
		{in: "127.1.2.3:443", want: "127.1.2.3:443"},
		{in: "[::1]:8443", want: "[::1]:8443"},
		{in: "[::ffff:127.0.0.1]:8443", want: "127.0.0.1:8443"},
		{in: "0.0.0.0:8443", wantErr: "not a loopback address"},
		{in: ":8443", wantErr: "not a name"},
		{in: "[::]:8443", wantErr: "not a loopback address"},
		{in: "192.168.1.5:8443", wantErr: "not a loopback address"},
		{in: "localhost:8443", wantErr: "not a name"},
		{in: "127.0.0.1", wantErr: "want host:port"},
		{in: "127.0.0.1:http", wantErr: "port must be a number"},
		{in: "127.0.0.1:65536", wantErr: "port must be a number"},
		{in: "127.0.0.1:-1", wantErr: "port must be a number"},
		{in: "", wantErr: "want host:port"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseProxyListen(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseProxyListen(%q) = %q, %v; want error containing %q", tt.in, got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("parseProxyListen(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestProxyHostAndOrigin(t *testing.T) {
	t.Parallel()
	if got, want := proxyHost("desktop", "dev"), "desktop.dev.jailoc.local"; got != want {
		t.Errorf("proxyHost = %q, want %q", got, want)
	}
	if got, want := proxyOrigin("a.dev.jailoc.local"), "https://a.dev.jailoc.local"; got != want {
		t.Errorf("origin = %q, want %q", got, want)
	}
	if got, want := proxyURL("a.dev.jailoc.local"), "https://a.dev.jailoc.local/"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

// fakeVM writes the state of a running workspace VM, owned by this test
// process, whose guest runs g.
func fakeVM(t *testing.T, name string, g guest.Config) string {
	t.Helper()
	stateDir := vmStateDir(name)
	if err := vm.WriteSpec(vm.Spec{StateDir: stateDir, Workspace: name, Guest: &g}); err != nil {
		t.Fatal(err)
	}
	writeFakeState(t, stateDir, vm.State{PID: os.Getpid(), BrokerPID: os.Getpid(), Ready: true})
	if err := os.WriteFile(vm.ProxySocket(stateDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

func writeFakeState(t *testing.T, stateDir string, st vm.State) {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(stateDir), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// replaceProxySocket swaps in a new file at path while the old one still exists, so
// the new one never reuses its inode.
func replaceProxySocket(t *testing.T, path string) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func relayGuest(ports ...uint16) guest.Config {
	return guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: ports}}
}

func proxyTestConfig() *config.Config {
	return &config.Config{Workspaces: map[string]config.Workspace{
		"app":     {Proxy: config.ProxyMap(map[string]int{"app": 3000, "api": 8080})},
		"desk":    {Proxy: config.ProxyMap(map[string]int{"web": 6080})},
		"plain":   {},
		"stopped": {Proxy: config.ProxyMap(map[string]int{"app": 3000})},
	}}
}

func TestSelectProxyTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeVM(t, "app", relayGuest(3000, 8080))
	fakeVM(t, "desk", relayGuest(6080))
	fakeVM(t, "plain", guest.Config{})
	cfg := proxyTestConfig()

	names := func(targets []proxyTarget) []string {
		var out []string
		for _, tg := range targets {
			out = append(out, tg.ws.Name)
		}
		return out
	}

	t.Run("all configured, sorted, skipping stopped and routeless", func(t *testing.T) {
		var note bytes.Buffer
		got, err := selectProxyTargets(cfg, nil, &note)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"app", "desk"}; !slices.Equal(names(got), want) {
			t.Errorf("targets = %v, want %v", names(got), want)
		}
		if want := "Skipping workspace stopped: not running\n"; note.String() != want {
			t.Errorf("notes = %q, want %q", note.String(), want)
		}
	})
	t.Run("explicit sorted and unique", func(t *testing.T) {
		got, err := selectProxyTargets(cfg, []string{"desk", "app", "desk"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"app", "desk"}; !slices.Equal(names(got), want) {
			t.Errorf("targets = %v, want %v", names(got), want)
		}
	})
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"explicit stopped fails", []string{"stopped"}, "is not running; start it with `jailoc up stopped`"},
		{"explicit without routes fails", []string{"plain"}, "has no proxy routes"},
		{"explicit unknown fails", []string{"nope"}, `resolve workspace "nope"`},
		{"explicit stopped fails even next to a running one", []string{"app", "stopped"}, "is not running"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := selectProxyTargets(cfg, tt.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	t.Run("nothing to serve", func(t *testing.T) {
		only := &config.Config{Workspaces: map[string]config.Workspace{"stopped": cfg.Workspaces["stopped"], "plain": {}}}
		_, err := selectProxyTargets(only, nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "no running workspace with proxy routes") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestSelectProxyTargetsRejectsRunningMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tt := range []struct {
		name    string
		ws      config.Workspace
		running guest.Config
		want    string
	}{
		{"proxy route added since start", config.Workspace{Proxy: config.ProxyMap(map[string]int{"app": 3000, "api": 8080})}, relayGuest(3000), "configured proxy"},
		{"proxy port changed since start", config.Workspace{Proxy: config.ProxyMap(map[string]int{"app": 3001})}, relayGuest(3000), "configured proxy"},
		{"running VM without a guest relay", config.Workspace{Proxy: config.ProxyMap(map[string]int{"app": 3000})}, guest.Config{}, "configured proxy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			name := "ws"
			fakeVM(t, name, tt.running)
			cfg := &config.Config{Workspaces: map[string]config.Workspace{name: tt.ws}}
			for _, args := range [][]string{nil, {name}} {
				_, err := selectProxyTargets(cfg, args, io.Discard)
				if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "jailoc restart ws") {
					t.Errorf("args %v: error = %v, want a restart hint containing %q", args, err, tt.want)
				}
			}
		})
	}
	t.Run("renamed route on the same port is safe", func(t *testing.T) {
		fakeVM(t, "ws", relayGuest(3000))
		cfg := &config.Config{Workspaces: map[string]config.Workspace{"ws": {Proxy: config.ProxyMap(map[string]int{"renamed": 3000})}}}
		if _, err := selectProxyTargets(cfg, []string{"ws"}, io.Discard); err != nil {
			t.Fatal(err)
		}
	})
}

// A VM whose guest replaces its relay allowlist on a reload is told to reload,
// not to restart, when its routes differ from the configuration.
func TestSelectProxyTargetsLiveProxyMismatchAsksForReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateDir := fakeVM(t, "ws", relayGuest(3000))
	if err := vm.WriteSpec(vm.Spec{StateDir: stateDir, Workspace: "ws", GuestProtocol: liveProxyGuestProtocol, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000}}}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Workspaces: map[string]config.Workspace{"ws": {Proxy: config.ProxyMap(map[string]int{"app": 3000, "api": 8080})}}}
	for _, args := range [][]string{nil, {"ws"}} {
		_, err := selectProxyTargets(cfg, args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "jailoc reload ws") || strings.Contains(err.Error(), "jailoc restart") {
			t.Errorf("args %v: error = %v, want a reload hint", args, err)
		}
	}
	// After the reload the spec carries the new routes.
	if err := vm.WriteSpec(vm.Spec{StateDir: stateDir, Workspace: "ws", GuestProtocol: liveProxyGuestProtocol, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000, 8080}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := selectProxyTargets(cfg, []string{"ws"}, io.Discard); err != nil {
		t.Fatalf("after reload: %v", err)
	}
}

func TestReadVMIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateDir := vmStateDir("id")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readVMIdentity(stateDir); !errors.Is(err, errVMNotRunning) {
		t.Fatalf("no state file: %v, want errVMNotRunning", err)
	}
	stateDir = fakeVM(t, "id", guest.Config{})
	first, err := readVMIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := readVMIdentity(stateDir)
	if err != nil || !first.same(again) {
		t.Fatalf("unchanged VM: %v, same = %v", err, first.same(again))
	}

	writeFakeState(t, stateDir, vm.State{PID: os.Getpid(), BrokerPID: os.Getpid(), Ready: false})
	if _, err := readVMIdentity(stateDir); !errors.Is(err, errVMNotRunning) {
		t.Errorf("not ready: %v, want errVMNotRunning", err)
	}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skipf("no true command to make a dead pid: %v", err)
	}
	writeFakeState(t, stateDir, vm.State{PID: gone.Process.Pid, Ready: true})
	if _, err := readVMIdentity(stateDir); !errors.Is(err, errVMNotRunning) {
		t.Errorf("dead supervisor: %v, want errVMNotRunning", err)
	}

	writeFakeState(t, stateDir, vm.State{PID: os.Getpid(), BrokerPID: os.Getpid(), Ready: true})
	replaceProxySocket(t, vm.ProxySocket(stateDir))
	sameBoot, err := readVMIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.same(sameBoot) {
		t.Error("a new proxy socket must be a different VM identity")
	}
	writeFakeState(t, stateDir, vm.State{PID: os.Getppid(), BrokerPID: os.Getpid(), Ready: true})
	other, err := readVMIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if sameBoot.same(other) {
		t.Error("another supervisor pid must be a different VM identity")
	}
	if err := os.Remove(vm.ProxySocket(stateDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := readVMIdentity(stateDir); err == nil || errors.Is(err, errVMNotRunning) {
		t.Errorf("missing proxy socket: %v, want a restart hint", err)
	}
}

// testInstance serves workspace "ws" with routes app (3000) and api (8080),
// whose VM identity read returns *id.
func testInstance(t *testing.T, id *vmIdentity, dial func(context.Context, string, uint16) (net.Conn, error)) (*proxyInstance, *bytes.Buffer) {
	t.Helper()
	var errw bytes.Buffer
	ws := &workspace.Resolved{Name: "ws", Proxy: []workspace.ProxyRoute{{Name: "api", Port: 8080}, {Name: "app", Port: 3000}}}
	inst := newProxyInstance(t.Context(), proxyTarget{ws: ws, stateDir: "/state", id: *id}, &errw)
	inst.read = func(string) (vmIdentity, error) { return *id, nil }
	inst.dial = dial
	return inst, &errw
}

func TestProxyRoutes(t *testing.T) {
	t.Parallel()
	id := &vmIdentity{pid: 1}
	inst, _ := testInstance(t, id, nil)
	var hosts []string
	for _, r := range inst.routes() {
		hosts = append(hosts, r.Host)
	}
	if want := []string{"api.ws.jailoc.local", "app.ws.jailoc.local", "ws.jailoc.local"}; !slices.Equal(hosts, want) {
		t.Errorf("hosts = %v, want %v", hosts, want)
	}
	routes := inst.routes()
	if last := routes[len(routes)-1]; last.StaticBody != "OK" || last.Dial != nil || last.Auth {
		t.Errorf("bare workspace route = %+v, want a static OK route without auth", last)
	}
	if got := proxyWorkspaceHost("ws"); got != "ws.jailoc.local" {
		t.Errorf("proxyWorkspaceHost = %q", got)
	}
	noRoutes := inst.routesFor(nil)
	if len(noRoutes) != 0 {
		t.Errorf("routesFor(nil) = %v, want none", noRoutes)
	}
}

func TestProxyRouteDialsItsPort(t *testing.T) {
	t.Parallel()
	id := &vmIdentity{pid: 1}
	var gotDir string
	var gotPort uint16
	inst, errw := testInstance(t, id, func(_ context.Context, dir string, port uint16) (net.Conn, error) {
		gotDir, gotPort = dir, port
		c, _ := net.Pipe()
		return c, nil
	})
	conn, err := inst.routes()[1].Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if gotDir != "/state" || gotPort != 3000 {
		t.Errorf("dialed %q port %d, want /state port 3000", gotDir, gotPort)
	}
	if errw.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errw.String())
	}
}

func TestProxyRouteFailsClosedWhenVMGone(t *testing.T) {
	t.Parallel()
	id := &vmIdentity{pid: 1}
	var dials atomic.Int32
	inst, errw := testInstance(t, id, func(context.Context, string, uint16) (net.Conn, error) {
		dials.Add(1)
		c, _ := net.Pipe()
		return c, nil
	})
	route := inst.routes()[0]
	conn, err := route.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The VM is replaced: another boot has another identity.
	*id = vmIdentity{pid: 2}
	if _, err := route.Dial(t.Context()); !errors.Is(err, errProxyVMGone) {
		t.Fatalf("dial after replacement: %v, want errProxyVMGone", err)
	}
	if dials.Load() != 1 {
		t.Errorf("dialed %d times, want the replacement VM never dialed", dials.Load())
	}
	// Even if the replacement identity flips back, the instance stays dead.
	*id = vmIdentity{pid: 1}
	if _, err := inst.routes()[1].Dial(t.Context()); !errors.Is(err, errProxyVMGone) {
		t.Errorf("dial after the identity reverted: %v, want errProxyVMGone", err)
	}
	if inst.ctx.Err() == nil {
		t.Error("instance context still live")
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("open connection was not closed when the VM was replaced")
	}
	if got := strings.Count(errw.String(), "Workspace ws:"); got != 1 {
		t.Errorf("stderr reported the lost VM %d times: %q", got, errw.String())
	}
	if !strings.Contains(errw.String(), "restart `jailoc proxy`") {
		t.Errorf("stderr = %q, want a restart hint", errw.String())
	}
}

func TestProxyRouteDialDuringReplacement(t *testing.T) {
	t.Parallel()
	id := &vmIdentity{pid: 1}
	var server net.Conn
	inst, _ := testInstance(t, id, func(context.Context, string, uint16) (net.Conn, error) {
		// The VM is replaced while the dial runs, and the dial reaches the new one.
		*id = vmIdentity{pid: 2}
		var client net.Conn
		client, server = net.Pipe()
		return client, nil
	})
	if _, err := inst.routes()[0].Dial(t.Context()); !errors.Is(err, errProxyVMGone) {
		t.Fatalf("dial: %v, want errProxyVMGone", err)
	}
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Error("connection to the replacement VM was left open")
	}
}

func TestProxyRouteDialError(t *testing.T) {
	t.Parallel()
	id := &vmIdentity{pid: 1}
	boom := errors.New("relay refused")
	inst, _ := testInstance(t, id, func(context.Context, string, uint16) (net.Conn, error) { return nil, boom })
	_, err := inst.routes()[0].Dial(t.Context())
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "guest port 8080") {
		t.Errorf("error = %v, want the dial error with its port", err)
	}
	if inst.ctx.Err() != nil {
		t.Error("a failed dial must not end the instance while the VM runs")
	}
}

func TestProxyInstanceWatch(t *testing.T) {
	t.Parallel()
	id := &vmIdentity{pid: 1}
	inst, _ := testInstance(t, id, nil)
	var replaced atomic.Bool
	inst.read = func(string) (vmIdentity, error) {
		if replaced.Load() {
			return vmIdentity{}, errVMNotRunning
		}
		return *id, nil
	}
	done := make(chan struct{})
	go func() {
		inst.watch(time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("watch ended while the VM runs")
	case <-time.After(20 * time.Millisecond):
	}
	replaced.Store(true)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not notice the stopped VM")
	}
	if !errors.Is(context.Cause(inst.ctx), errProxyVMGone) {
		t.Errorf("cause = %v, want errProxyVMGone", context.Cause(inst.ctx))
	}
}

func TestProxyInstanceWatchStopsWithParent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	id := &vmIdentity{pid: 1}
	var errw bytes.Buffer
	ws := &workspace.Resolved{Name: "ws"}
	inst := newProxyInstance(ctx, proxyTarget{ws: ws, id: *id}, &errw)
	inst.read = func(string) (vmIdentity, error) { return *id, nil }
	done := make(chan struct{})
	go func() {
		inst.watch(time.Hour)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch outlived its parent context")
	}
	if errw.Len() != 0 {
		t.Errorf("shutdown reported a lost VM: %q", errw.String())
	}
}

func TestPrintProxyBanner(t *testing.T) {
	t.Parallel()
	targets := []proxyTarget{
		{ws: &workspace.Resolved{Name: "dev", Proxy: []workspace.ProxyRoute{{Name: "app", Port: 3000, Auth: true}, {Name: "desktop", Port: 6080, Auth: true}}}},
	}
	codes := map[string]string{"app.dev.jailoc.local": "CODE-APP", "desktop.dev.jailoc.local": "CODE-DESK"}
	var out bytes.Buffer
	printProxyBanner(&out, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8443}, targets, codes)
	got := out.String()
	for _, want := range []string{
		"https://app.dev.jailoc.local/",
		"https://desktop.dev.jailoc.local/",
		"only valid for https://app.dev.jailoc.local)",
		"forward 127.0.0.1:443 to 127.0.0.1:8443",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "CODE-APP") != 1 || strings.Count(got, "CODE-DESK") != 1 {
		t.Errorf("each code must be printed exactly once:\n%s", got)
	}
	if strings.Contains(got, "?") || strings.Contains(got, ":8443/") {
		t.Errorf("URLs carry no query and no port:\n%s", got)
	}
	out.Reset()
	printProxyBanner(&out, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}, targets, codes)
	if strings.Contains(out.String(), "forward") {
		t.Errorf("port 443 needs no forward:\n%s", out.String())
	}
}

func TestProxyCommandArguments(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		cmd := newProxyCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(t.Context())
		return out.String(), err
	}
	for _, flag := range []string{"--tls-cert", "--tls-key"} {
		if _, err := run(flag, "c.pem"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s: error = %v, want unknown flag", flag, err)
		}
	}
	for _, listen := range []string{"0.0.0.0:8443", "example.com:443", "8443"} {
		if _, err := run("--listen", listen); err == nil || !strings.Contains(err.Error(), "--listen") {
			t.Errorf("--listen %s: error = %v, want a --listen error", listen, err)
		}
	}
	out, err := run("--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proxy [workspace...]", "--foreground", "--listen", "limited to names under jailoc.local", proxyDefaultListen} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}

// proxyCATestPool returns a pool holding the automatic proxy CA of the
// current HOME.
func proxyCATestPool(t *testing.T) *x509.CertPool {
	t.Helper()
	caPEM, err := os.ReadFile(proxyCACertPath()) //nolint:gosec // test fixture path.
	if err != nil {
		t.Fatalf("read proxy CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("proxy CA holds no certificate")
	}
	return pool
}

// fakeProxyRelay listens on the proxy socket of stateDir like the guest relay
// through the broker: it answers the 2-byte port handshake with "connected"
// and serves HTTP on the connection.
func fakeProxyRelay(t *testing.T, stateDir string, handler http.Handler) {
	t.Helper()
	ln, err := net.Listen("unix", vm.ProxySocket(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	conns := make(chan net.Conn)
	cl := &chanListener{conns: conns, closed: make(chan struct{}), addr: ln.Addr()}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(cl) }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				var port [2]byte
				if _, err := io.ReadFull(c, port[:]); err != nil {
					_ = c.Close()
					return
				}
				if _, err := c.Write([]byte{0}); err != nil {
					_ = c.Close()
					return
				}
				select {
				case conns <- c:
				case <-cl.closed:
					_ = c.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		_ = cl.Close()
		_ = srv.Close()
	})
}

// chanListener hands the connections sent on conns to an http.Server until
// it is closed. Close unblocks Accept, so http.Server.Close cannot hang on it.
type chanListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	addr   net.Addr
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

type proxyRun struct {
	out    *syncBuffer
	errOut *syncBuffer
	done   chan struct{} // closed when the command returned
	err    error         // its result, valid after done is closed
	cancel context.CancelFunc
	addr   string
	code   string
	host   string
	client *http.Client
}

var (
	proxyAddrRe = regexp.MustCompile(`Proxy listening on (127\.0\.0\.1:\d+)`)
	proxyCodeRe = regexp.MustCompile(`login code (\S+) \(only valid for`)
)

// startProxyRun runs `jailoc proxy` for the proxy route app of workspace
// "e2e" on an ephemeral port, with a running fake VM whose relay serves
// handler, and waits until it prints its address and login code.
func startProxyRun(t *testing.T, handler http.Handler) (*proxyRun, string) {
	t.Helper()
	return startProxyRunFor(t, handler, "[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", "app.e2e.jailoc.local", relayGuest(3000))
}

// startProxyRunFor is startProxyRun for the config text cfgTOML, serving
// the route at host from a fake VM configured as g.
func startProxyRunFor(t *testing.T, handler http.Handler, cfgTOML, host string, g guest.Config) (*proxyRun, string) {
	t.Helper()
	return startProxyRunArgs(t, handler, cfgTOML, host, g, "--foreground", "--listen", "127.0.0.1:0")
}

// shortTempDir creates a temporary directory with a short path: unix socket
// paths are limited to about 100 bytes, so a long TMPDIR (macOS) falls back
// to /tmp.
func shortTempDir(t *testing.T, pattern string) string {
	t.Helper()
	base := os.TempDir()
	if len(base) > 40 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startProxyRunArgs is startProxyRunFor with the command line args. The
// client trusts the automatic proxy CA.
func startProxyRunArgs(t *testing.T, handler http.Handler, cfgTOML, host string, g guest.Config, args ...string) (*proxyRun, string) {
	t.Helper()
	home := shortTempDir(t, "jp")
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PathEnv, cfgPath)
	stateDir := fakeVM(t, "e2e", g)
	if err := os.Remove(vm.ProxySocket(stateDir)); err != nil {
		t.Fatal(err)
	}
	fakeProxyRelay(t, stateDir, handler)

	ctx, cancel := context.WithCancel(t.Context())
	run := &proxyRun{out: &syncBuffer{}, errOut: &syncBuffer{}, done: make(chan struct{}), cancel: cancel, host: host}
	cmd := newProxyCmd()
	cmd.SetOut(run.out)
	cmd.SetErr(run.errOut)
	cmd.SetArgs(args)
	go func() {
		run.err = cmd.ExecuteContext(ctx)
		close(run.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(5 * time.Second):
			t.Error("proxy did not stop")
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a, c := proxyAddrRe.FindStringSubmatch(run.out.String()), proxyCodeRe.FindStringSubmatch(run.out.String())
		if a != nil && c != nil {
			run.addr, run.code = a[1], c[1]
			break
		}
		select {
		case <-run.done:
			t.Fatalf("proxy ended early: %v\nstdout: %s\nstderr: %s", run.err, run.out.String(), run.errOut.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if run.addr == "" {
		t.Fatalf("proxy printed no address and code:\n%s", run.out.String())
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: proxyCATestPool(t), ServerName: host, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", run.addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	run.client = &http.Client{
		Transport: tr, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return run, stateDir
}

func (r *proxyRun) do(t *testing.T, method, path string, form url.Values, cookie *http.Cookie) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(t.Context(), method, "https://"+r.host+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://"+r.host)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (r *proxyRun) login(t *testing.T) *http.Cookie {
	t.Helper()
	res := r.do(t, "POST", "/.jailoc/login", url.Values{"code": {r.code}}, nil)
	if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 1 {
		t.Fatalf("login: status %d, cookies %v", res.StatusCode, res.Cookies())
	}
	return res.Cookies()[0]
}

func TestProxyServesAndFailsClosedWhenVMReplaced(t *testing.T) {
	run, stateDir := startProxyRun(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello from the guest")
	}))
	if out := run.out.String(); strings.Count(out, run.code) != 1 || !strings.Contains(out, "https://app.e2e.jailoc.local/") {
		t.Errorf("output must show the URL and the code once:\n%s", out)
	}
	if strings.Contains(run.errOut.String(), run.code) {
		t.Errorf("the code reached stderr: %s", run.errOut.String())
	}
	if res := run.do(t, "GET", "/", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("without a session: status %d, want a redirect to login", res.StatusCode)
	}
	cookie := run.login(t)
	res := run.do(t, "GET", "/", nil, cookie)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "hello from the guest" {
		t.Fatalf("with a session: status %d, body %q", res.StatusCode, body)
	}

	// Another VM boot takes over the state directory: the same supervisor
	// state file now names another process.
	writeFakeState(t, stateDir, vm.State{PID: os.Getppid(), BrokerPID: os.Getpid(), Ready: true})
	select {
	case <-run.done:
		if !errors.Is(run.err, errProxyNoVMs) {
			t.Fatalf("proxy ended with %v, want errProxyNoVMs", run.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy kept running after its only VM was replaced")
	}
	if !strings.Contains(run.errOut.String(), "Workspace e2e:") {
		t.Errorf("stderr = %q, want the lost VM reported", run.errOut.String())
	}
}

func TestProxyStopsOnContextCancel(t *testing.T) {
	run, _ := startProxyRun(t, http.NotFoundHandler())
	run.cancel()
	select {
	case <-run.done:
		if run.err != nil {
			t.Fatalf("proxy ended with %v on cancel, want nil", run.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy ignored cancellation")
	}
}

// useProxyBrokerHost replaces the host seam of --listener-broker for one
// test. Tests that call it must not be parallel.
func useProxyBrokerHost(t *testing.T, target proxyBrokerTarget) {
	t.Helper()
	old := proxyBrokerHost
	proxyBrokerHost = func() proxyBrokerTarget { return target }
	t.Cleanup(func() { proxyBrokerHost = old })
}

func testUID() uint32 {
	return uint32(os.Getuid()) //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
}

// startTestListenerBroker runs an in-process listener broker for the current
// user on an ephemeral loopback port and a socket in a private directory.
func startTestListenerBroker(t *testing.T) proxyBrokerTarget {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("the test needs an unprivileged user")
	}
	sock := filepath.Join(shortTempDir(t, "jb"), "b.sock")
	tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	control, err := proxyhost.ListenBrokerSocket(sock, testUID())
	if err != nil {
		_ = tcp.Close()
		t.Fatal(err)
	}
	b, err := proxyhost.NewBroker(proxyhost.BrokerConfig{OwnerUID: testUID(), Listeners: []*net.TCPListener{tcp}, Control: control, Logf: t.Logf})
	if err != nil {
		_ = tcp.Close()
		_ = control.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- b.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("broker Serve: %v", err)
		}
	})
	return proxyBrokerTarget{uid: testUID(), socketPath: sock, brokerUID: testUID(), addr: tcp.Addr().(*net.TCPAddr)}
}

func runProxyArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newProxyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func TestProxyListenerBrokerRefusesListen(t *testing.T) {
	useProxyBrokerHost(t, proxyBrokerTarget{uid: testUID(), socketPath: "/nonexistent/b.sock", addr: proxyBrokerAddr})
	for _, listen := range []string{"127.0.0.1:8443", "127.0.0.1:443", proxyDefaultListen} {
		_, err := runProxyArgs(t, "--listener-broker", "--listen", listen)
		if err == nil || !strings.Contains(err.Error(), "--listener-broker") || !strings.Contains(err.Error(), "--listen") {
			t.Errorf("--listen %s: error = %v, want a refusal naming both flags", listen, err)
		}
	}
}

func TestProxyListenerBrokerRefusesRoot(t *testing.T) {
	useProxyBrokerHost(t, proxyBrokerTarget{uid: 0, socketPath: "/nonexistent/b.sock", addr: proxyBrokerAddr})
	_, err := runProxyArgs(t, "--listener-broker")
	if err == nil || !strings.Contains(err.Error(), "root") || !strings.Contains(err.Error(), "--listener-broker") {
		t.Fatalf("error = %v, want a refusal to run as root", err)
	}
}

func TestProxyListenerBrokerHelp(t *testing.T) {
	out, err := runProxyArgs(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--listener-broker") || !strings.Contains(out, "jailoc-listener-broker") {
		t.Errorf("help lacks the flag and the broker command:\n%s", out)
	}
}

func TestProxyListenerBrokerProductionTarget(t *testing.T) {
	t.Parallel()
	got := defaultProxyBrokerHost()
	if got.uid != testUID() || got.brokerUID != 0 || got.socketPath != proxyhost.BrokerSocketPath(testUID()) {
		t.Errorf("target = %+v, want this user, a root broker and its socket path", got)
	}
	if got.addr == nil || got.addr.String() != "127.0.0.1:443" {
		t.Errorf("addr = %v, want 127.0.0.1:443", got.addr)
	}
}

func TestAttachProxyBrokerErrors(t *testing.T) {
	t.Run("missing broker", func(t *testing.T) {
		dir := shortTempDir(t, "jb")
		target := proxyBrokerTarget{uid: testUID(), socketPath: filepath.Join(dir, "none.sock"), brokerUID: testUID(), addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}}
		useProxyBrokerHost(t, target)
		ln, err := attachProxyBroker(t.Context())
		if err == nil {
			_ = ln.Close()
			t.Fatal("attach without a broker succeeded")
		}
		for _, want := range []string{"listener broker is not running", "jailoc-listener-broker"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks %q", err, want)
			}
		}
	})
	t.Run("stale socket", func(t *testing.T) {
		dir := shortTempDir(t, "jb")
		sock := filepath.Join(dir, "stale.sock")
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		l.SetUnlinkOnClose(false)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		useProxyBrokerHost(t, proxyBrokerTarget{uid: testUID(), socketPath: sock, brokerUID: testUID(), addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}})
		ln, err := attachProxyBroker(t.Context())
		if err == nil {
			_ = ln.Close()
			t.Fatal("attach to a stale socket succeeded")
		}
		if !strings.Contains(err.Error(), "listener broker is not running") {
			t.Errorf("error = %q, want the not-running message", err)
		}
	})
	t.Run("busy", func(t *testing.T) {
		target := startTestListenerBroker(t)
		useProxyBrokerHost(t, target)
		first, err := attachProxyBroker(t.Context())
		if err != nil {
			t.Fatalf("first attach: %v", err)
		}
		t.Cleanup(func() { _ = first.Close() })
		ln, err := attachProxyBroker(t.Context())
		if err == nil {
			_ = ln.Close()
			t.Fatal("second attach succeeded")
		}
		if !strings.Contains(err.Error(), "another proxy is attached") {
			t.Errorf("error = %q, want the busy message", err)
		}
	})
	t.Run("broker of another user", func(t *testing.T) {
		target := startTestListenerBroker(t)
		target.brokerUID = testUID() + 1
		useProxyBrokerHost(t, target)
		ln, err := attachProxyBroker(t.Context())
		if err == nil {
			_ = ln.Close()
			t.Fatal("attach to a broker of the wrong user succeeded")
		}
	})
}

func TestProxyListenerBrokerServesThroughBroker(t *testing.T) {
	target := startTestListenerBroker(t)
	useProxyBrokerHost(t, target)
	run, _ := startProxyRunArgs(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "through the broker")
	}), "[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", "app.e2e.jailoc.local", relayGuest(3000), "--foreground", "--listener-broker")
	if run.addr != target.addr.String() {
		t.Errorf("proxy reports %s, want the broker address %s", run.addr, target.addr)
	}
	out := run.out.String()
	if strings.Contains(out, "https://app.e2e.jailoc.local:") {
		t.Errorf("URLs must carry no port:\n%s", out)
	}
	// The connection below reaches the proxy only through the broker: the
	// proxy opened no listener of its own.
	cookie := run.login(t)
	res := run.do(t, "GET", "/", nil, cookie)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "through the broker" {
		t.Fatalf("status %d, body %q", res.StatusCode, body)
	}

	// A second proxy is refused while this one is attached.
	if _, err := attachProxyBroker(t.Context()); err == nil || !strings.Contains(err.Error(), "another proxy is attached") {
		t.Errorf("second attach error = %v, want the busy message", err)
	}

	// Stopping the proxy detaches it, so the next one can attach.
	run.cancel()
	select {
	case <-run.done:
		if run.err != nil {
			t.Fatalf("proxy ended with %v on cancel, want nil", run.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy ignored cancellation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ln, err := attachProxyBroker(t.Context())
		if err == nil {
			_ = ln.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the broker stayed busy after the proxy stopped: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func proxyCertTestSetup(t *testing.T) (*config.Config, []proxyTarget) {
	t.Helper()
	home := shortTempDir(t, "jc")
	t.Setenv("HOME", home)
	t.Setenv(config.PathEnv, filepath.Join(home, "config.toml"))
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"one": {Proxy: config.ProxyMap(map[string]int{"app": 3000})},
	}}
	targets := []proxyTarget{{ws: &workspace.Resolved{Name: "one", Proxy: []workspace.ProxyRoute{{Name: "app", Port: 3000}}}}}
	return cfg, targets
}

func TestEnsureProxyCert(t *testing.T) {
	cfg, targets := proxyCertTestSetup(t)
	cert1, caPath, err := ensureProxyCert(cfg, targets)
	if err != nil {
		t.Fatalf("ensureProxyCert: %v", err)
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "jailoc", "proxy")
	if caPath != filepath.Join(dir, "ca.crt") {
		t.Fatalf("caPath = %q, want %q", caPath, filepath.Join(dir, "ca.crt"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{".lock", "ca.crt", "cert.crt", "cert.key", "intermediate.crt", "intermediate.key"}; !slices.Equal(names, want) {
		t.Fatalf("files in %s = %v, want %v (no root key)", dir, names, want)
	}
	if len(cert1.Certificate) != 2 {
		t.Fatalf("served chain has %d certificates, want leaf and intermediate", len(cert1.Certificate))
	}
	inter, err := x509.ParseCertificate(cert1.Certificate[1])
	if err != nil {
		t.Fatal(err)
	}
	intermediates := x509.NewCertPool()
	intermediates.AddCert(inter)
	if _, err := cert1.Leaf.Verify(x509.VerifyOptions{DNSName: "app.one.jailoc.local", Roots: proxyCATestPool(t), Intermediates: intermediates}); err != nil {
		t.Fatalf("leaf does not chain to ca.crt: %v", err)
	}
	if _, err := cert1.Leaf.Verify(x509.VerifyOptions{DNSName: "app.one.jailoc.local", Roots: proxyCATestPool(t)}); err == nil {
		t.Fatal("leaf verified without the intermediate; want the root to sign only the intermediate")
	}
	// Second call reuses the existing leaf and CA.
	cert2, _, err := ensureProxyCert(cfg, targets)
	if err != nil {
		t.Fatalf("second ensureProxyCert: %v", err)
	}
	if !bytes.Equal(cert1.Certificate[0], cert2.Certificate[0]) {
		t.Fatal("existing valid leaf was regenerated")
	}
	// Adding a workspace not covered by the first leaf regenerates the leaf with the same CA.
	cfg.Workspaces["two"] = config.Workspace{Proxy: config.ProxyMap(map[string]int{"api": 8080})}
	targets = append(targets, proxyTarget{ws: &workspace.Resolved{Name: "two", Proxy: []workspace.ProxyRoute{{Name: "api", Port: 8080}}}})
	cert3, _, err := ensureProxyCert(cfg, targets)
	if err != nil {
		t.Fatalf("third ensureProxyCert: %v", err)
	}
	if !bytes.Equal(cert1.Certificate[1], cert3.Certificate[1]) {
		t.Fatal("intermediate changed when the leaf was reissued")
	}
	if err := cert3.Leaf.VerifyHostname("api.two.jailoc.local"); err != nil {
		t.Fatalf("VerifyHostname(api.two): %v", err)
	}
	if err := cert3.Leaf.VerifyHostname("app.one.jailoc.local"); err != nil {
		t.Fatalf("VerifyHostname(app.one): %v", err)
	}
}

func TestProxyCAIsLimitedToJailocLocal(t *testing.T) {
	cfg, targets := proxyCertTestSetup(t)
	if _, _, err := ensureProxyCert(cfg, targets); err != nil {
		t.Fatalf("ensureProxyCert: %v", err)
	}
	dir := proxyCADir()
	ca, err := readProxyCA(filepath.Join(dir, proxyCACert), filepath.Join(dir, proxyIntermediateCrt), filepath.Join(dir, proxyIntermediateKey), time.Now())
	if err != nil {
		t.Fatalf("readProxyCA: %v", err)
	}
	for name, c := range map[string]*x509.Certificate{"root": ca.root, "intermediate": ca.inter} {
		if err := checkProxyCA(c, time.Now()); err != nil {
			t.Errorf("%s CA: %v", name, err)
		}
	}
	if ca.root.MaxPathLen != 1 || !ca.inter.MaxPathLenZero {
		t.Errorf("path length: root %d, intermediate zero=%v; want 1 and true", ca.root.MaxPathLen, ca.inter.MaxPathLenZero)
	}

	// The intermediate key is the only CA key on disk: whatever it signs must
	// stay under jailoc.local, even with the intermediate as trust anchor.
	for anchorName, anchor := range map[string]*x509.Certificate{"root": ca.root, "intermediate": ca.inter} {
		roots := x509.NewCertPool()
		roots.AddCert(anchor)
		intermediates := x509.NewCertPool()
		intermediates.AddCert(ca.inter)
		issue := func(dnsNames []string, ips []net.IP) *x509.Certificate {
			t.Helper()
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			tmpl := &x509.Certificate{
				SerialNumber: big.NewInt(2), DNSNames: dnsNames, IPAddresses: ips,
				NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
				KeyUsage:    x509.KeyUsageDigitalSignature,
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.inter, key.Public(), ca.interKey)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			return leaf
		}
		verify := func(leaf *x509.Certificate, name string) error {
			_, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, Intermediates: intermediates})
			return err
		}
		if err := verify(issue([]string{"app.one.jailoc.local"}, nil), "app.one.jailoc.local"); err != nil {
			t.Errorf("%s anchor: leaf under jailoc.local: %v", anchorName, err)
		}
		for _, tc := range []struct {
			name  string
			dns   []string
			ips   []net.IP
			check string
		}{
			{"other domain", []string{"example.com"}, nil, "example.com"},
			{"suffix lookalike", []string{"app.notjailoc.local"}, nil, "app.notjailoc.local"},
			{"IPv4 address", nil, []net.IP{net.IPv4(127, 0, 0, 1)}, "127.0.0.1"},
			{"IPv6 address", nil, []net.IP{net.IPv6loopback}, "::1"},
		} {
			if err := verify(issue(tc.dns, tc.ips), tc.check); err == nil {
				t.Errorf("%s anchor, %s: a leaf for %s verified under the proxy CA", anchorName, tc.name, tc.check)
			}
		}
	}
}

func TestEnsureProxyCertRejectsUnconstrainedCA(t *testing.T) {
	cfg, targets := proxyCertTestSetup(t)
	dir := proxyCADir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, proxyCACert), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = ensureProxyCert(cfg, targets)
	if err == nil || !strings.Contains(err.Error(), "not limited to jailoc.local") || !strings.Contains(err.Error(), "remove "+dir) {
		t.Fatalf("error = %v, want a refusal of the unconstrained CA with a removal hint", err)
	}
}

func TestEnsureProxyCertReplacesIncompleteCA(t *testing.T) {
	cfg, targets := proxyCertTestSetup(t)
	dir := proxyCADir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// An interrupted first run leaves an intermediate without ca.crt; nobody
	// can trust that yet, so jailoc starts over.
	for _, name := range []string{proxyIntermediateCrt, proxyIntermediateKey} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := ensureProxyCert(cfg, targets); err != nil {
		t.Fatalf("ensureProxyCert: %v", err)
	}
	if _, err := readProxyCA(filepath.Join(dir, proxyCACert), filepath.Join(dir, proxyIntermediateCrt), filepath.Join(dir, proxyIntermediateKey), time.Now()); err != nil {
		t.Fatalf("readProxyCA after replacing the incomplete CA: %v", err)
	}
}

func TestProxySyncStatusAndStop(t *testing.T) {
	run, _ := startProxyRunArgs(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}), "[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", "app.e2e.jailoc.local", relayGuest(3000), "--foreground", "--listen", "127.0.0.1:0")
	cookie := run.login(t)

	// Re-running `jailoc proxy` (without --foreground) syncs the running proxy,
	// preserves the existing session, and prints status without a new login code.
	var syncOut bytes.Buffer
	syncCmd := newProxyCmd()
	syncCmd.SetOut(&syncOut)
	syncCmd.SetErr(&syncOut)
	if err := syncCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("background sync: %v\n%s", err, syncOut.String())
	}
	if !strings.Contains(syncOut.String(), "existing session preserved") || strings.Contains(syncOut.String(), "login code") {
		t.Fatalf("sync output = %q, want existing session preserved without a new login code", syncOut.String())
	}
	res := run.do(t, "GET", "/", nil, cookie)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / after sync = %d, want 200", res.StatusCode)
	}

	// `jailoc proxy status` reports the running proxy.
	var statusOut bytes.Buffer
	statusCmd := newProxyCmd()
	statusCmd.SetOut(&statusOut)
	statusCmd.SetErr(&statusOut)
	statusCmd.SetArgs([]string{"status"})
	if err := statusCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("proxy status: %v", err)
	}
	if !strings.Contains(statusOut.String(), "https://app.e2e.jailoc.local/") {
		t.Fatalf("status output = %q, want route URL", statusOut.String())
	}

	ws := &workspace.Resolved{Name: "e2e", Proxy: []workspace.ProxyRoute{{Name: "app", Port: 3000}}}
	if label := proxyStatusLabel(ws); !strings.Contains(label, "running on "+run.addr) {
		t.Fatalf("proxyStatusLabel(running) = %q", label)
	}

	// `jailoc proxy stop` stops the running proxy.
	var stopOut bytes.Buffer
	stopCmd := newProxyCmd()
	stopCmd.SetOut(&stopOut)
	stopCmd.SetErr(&stopOut)
	stopCmd.SetArgs([]string{"stop"})
	if err := stopCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("proxy stop: %v", err)
	}
	if !strings.Contains(stopOut.String(), "stopped") {
		t.Fatalf("stop output = %q", stopOut.String())
	}
	if label := proxyStatusLabel(ws); !strings.Contains(label, "proxy stopped") {
		t.Fatalf("proxyStatusLabel(stopped) = %q", label)
	}
}

func TestProxyPruneKeepsRoutesAndSyncRejectsOtherConfig(t *testing.T) {
	run, _ := startProxyRunArgs(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}), "[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", "app.e2e.jailoc.local", relayGuest(3000), "--foreground", "--listen", "127.0.0.1:0")
	st := runningProxy()
	if st == nil {
		t.Fatal("runningProxy() = nil while the proxy serves")
	}
	if st.Config != proxyConfigPath() {
		t.Fatalf("state config = %q, want %q", st.Config, proxyConfigPath())
	}

	resp, err := sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "sync", Config: filepath.Join(t.TempDir(), "other.toml")})
	if err != nil {
		t.Fatalf("sync with another config: %v", err)
	}
	assertContains(t, resp.Error, "serves workspaces of "+proxyConfigPath())

	// A port change waits for `jailoc restart`; a prune must keep the
	// running routes instead of dropping the workspace.
	if err := os.WriteFile(config.ConfigPath(), []byte("[workspaces.e2e.proxy]\napp = { port = 3001, auth = true }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err = sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "prune"})
	if err != nil || resp.Error != "" || resp.Stopped {
		t.Fatalf("prune after a port change = %+v, %v; want the workspace kept", resp, err)
	}

	// Turning auth off in the config must not be applied by a prune, which
	// only drops stopped workspaces.
	if err := os.WriteFile(config.ConfigPath(), []byte("[workspaces.e2e.proxy]\napp = { port = 3000, auth = false }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err = sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "prune"})
	if err != nil || resp.Error != "" || resp.Stopped || len(resp.Codes) != 0 {
		t.Fatalf("prune = %+v, %v; want unchanged state without codes", resp, err)
	}
	if res := run.do(t, "GET", "/", nil, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET / without session after prune = %d, want %d (auth kept)", res.StatusCode, http.StatusSeeOther)
	}
	resp, err = sendProxyControl(st.ControlSocket, proxyControlRequest{Action: "sync", Config: proxyConfigPath()})
	if err != nil || resp.Error != "" {
		t.Fatalf("sync = %+v, %v", resp, err)
	}
	if res := run.do(t, "GET", "/", nil, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("GET / without session after sync = %d, want 200", res.StatusCode)
	}
}

func TestProxyStopIgnoresStaleStateOfAnotherProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	// A crashed proxy left its state and socket path behind and the PID was reused.
	sock := filepath.Join(proxyStateDir(), "control.sock")
	if err := writeProxyState(proxyState{PID: sleeper.Process.Pid, ControlSocket: sock}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeProxySavedOptions(proxyOptions{listenerBroker: true}); err != nil {
		t.Fatal(err)
	}
	if runningProxy() != nil {
		t.Fatal("runningProxy() reports a proxy although none holds proxy.lock")
	}

	var out bytes.Buffer
	stopCmd := newProxyCmd()
	stopCmd.SetOut(&out)
	stopCmd.SetErr(&out)
	stopCmd.SetArgs([]string{"stop"})
	if err := stopCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("proxy stop: %v", err)
	}
	assertContains(t, out.String(), "Proxy is not running")
	if !processAlive(sleeper.Process.Pid) {
		t.Fatal("proxy stop signalled the unrelated process of a stale PID")
	}
	for _, path := range []string{proxyStateFile(), proxyOptionsFile()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s after stop: %v, want removed", path, err)
		}
	}
}

func TestPrintSyncedProxyShowsNewCodesOutsideFilter(t *testing.T) {
	t.Parallel()
	st := proxyState{Listen: "127.0.0.1:8443", PID: 1, Workspaces: map[string][]string{
		"a": {"app.a.jailoc.local"},
		"b": {"app.b.jailoc.local"},
		"c": {"app.c.jailoc.local"},
	}}
	var out bytes.Buffer
	printSyncedProxy(&out, st, map[string]string{"app.b.jailoc.local": "secret"}, []string{"a"})
	assertContains(t, out.String(), "Workspace a:")
	assertContains(t, out.String(), "app.b.jailoc.local/  login code secret")
	if strings.Contains(out.String(), "Workspace c:") {
		t.Fatalf("output = %q, want workspace c filtered out", out.String())
	}
}

func TestEnsureProxyCertRejectsSymlinkedShare(t *testing.T) {
	cfg, targets := proxyCertTestSetup(t)
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "share"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "share-link")
	if err := os.Symlink(filepath.Join(home, ".local", "share"), link); err != nil {
		t.Fatal(err)
	}
	ws := cfg.Workspaces["one"]
	ws.Paths = []string{link}
	cfg.Workspaces["one"] = ws
	_, _, err := ensureProxyCert(cfg, targets)
	if err == nil {
		t.Fatal("ensureProxyCert succeeded with a path that links to a parent of the proxy CA directory")
	}
	assertContains(t, err.Error(), "overlaps shared path")
}

func TestEnsureProxyCertRejectsRunningVMShare(t *testing.T) {
	cfg, targets := proxyCertTestSetup(t)
	home, _ := os.UserHomeDir()
	// The VM booted with ~/.local/share shared; the configuration no
	// longer lists it, but the share stays until the VM restarts.
	stateDir := vmStateDir("old")
	if err := vm.WriteSpec(vm.Spec{StateDir: stateDir, Shares: []vm.Share{{HostPath: filepath.Join(home, ".local", "share")}}}); err != nil {
		t.Fatal(err)
	}
	writeRunning := func(pid int) {
		t.Helper()
		data, err := json.Marshal(vm.State{PID: pid, Ready: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vm.StateFile(stateDir), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRunning(os.Getpid())
	_, _, err := ensureProxyCert(cfg, targets)
	if err == nil {
		t.Fatal("ensureProxyCert succeeded while a running VM shares a parent of the proxy CA directory")
	}
	assertContains(t, err.Error(), `of workspace "old"`)

	// A share of the host root exposes the CA directory as well.
	if err := vm.WriteSpec(vm.Spec{StateDir: stateDir, Shares: []vm.Share{{HostPath: "/"}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureProxyCert(cfg, targets); err == nil {
		t.Fatal("ensureProxyCert succeeded while a running VM shares the host root")
	}

	// A stopped VM no longer exposes its shares.
	writeRunning(0)
	if _, _, err := ensureProxyCert(cfg, targets); err != nil {
		t.Fatalf("ensureProxyCert with a stopped VM: %v", err)
	}
}

func TestPathsOverlap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want bool
	}{
		{"/", "/home/u/.local/share/jailoc/proxy", true},
		{"/home/u/.local/share/jailoc/proxy", "/", true},
		{"/", "/", true},
		{"/home/u/.local", "/home/u/.local/share/jailoc/proxy", true},
		{"/home/u/.local/share/jailoc/proxy", "/home/u/.local/share/jailoc/proxy/x", true},
		{"/home/u/.local/share/jailoc/proxy", "/home/u/.local/share/jailoc/proxy2", false},
		{"/home/u/src", "/home/u/.local/share/jailoc/proxy", false},
	}
	for _, tt := range tests {
		if got := pathsOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("pathsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestProxyServesWorkspaceHostOK(t *testing.T) {
	run, _ := startProxyRun(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "from the guest")
	}))
	bare := "e2e.jailoc.local"
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: proxyCATestPool(t), ServerName: bare, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", run.addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	run.client = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	run.host = bare

	res := run.do(t, "GET", "/", nil, nil)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "OK" {
		t.Fatalf("GET https://%s/: status %d, body %q, want 200 OK", bare, res.StatusCode, body)
	}
	if res := run.do(t, "GET", "/other", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /other: status %d, want 404", res.StatusCode)
	}
}

func TestProxyCertSANsIncludeWorkspaceHost(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"one":   {Proxy: config.ProxyMap(map[string]int{"app": 3000})},
		"Bad_1": {},
	}}
	targets := []proxyTarget{{ws: &workspace.Resolved{Name: "two", Proxy: []workspace.ProxyRoute{{Name: "api", Port: 8080}}}}}
	names, hosts := proxyCertSANs(cfg, targets)
	for _, want := range []string{"one.jailoc.local", "*.one.jailoc.local", "two.jailoc.local", "*.two.jailoc.local", "api.two.jailoc.local"} {
		if !slices.Contains(names, want) {
			t.Errorf("SAN names %v lack %q", names, want)
		}
	}
	for _, want := range []string{"one.jailoc.local", "two.jailoc.local", "api.two.jailoc.local"} {
		if !slices.Contains(hosts, want) {
			t.Errorf("checked hosts %v lack %q", hosts, want)
		}
	}
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), "bad_1") {
			t.Errorf("invalid DNS label in SANs: %q", n)
		}
	}
}

func TestEnsureProxyCertCoversWorkspaceHostAndReissuesOldLeaf(t *testing.T) {
	cfg, targets := proxyCertTestSetup(t)
	cert, _, err := ensureProxyCert(cfg, targets)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.Leaf.VerifyHostname("one.jailoc.local"); err != nil {
		t.Fatalf("leaf does not cover the bare workspace host: %v", err)
	}
	// A leaf from before the bare host existed is replaced, keeping the CA.
	dir := proxyCADir()
	ca, err := loadOrCreateProxyCA(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	old, err := createProxyLeaf(filepath.Join(dir, proxyLeafCert), filepath.Join(dir, proxyLeafKey), ca, []string{"*.one.jailoc.local", "app.one.jailoc.local"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	renewed, _, err := ensureProxyCert(cfg, targets)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(old.Certificate[0], renewed.Certificate[0]) {
		t.Fatal("a leaf without the bare workspace host was reused")
	}
	if err := renewed.Leaf.VerifyHostname("one.jailoc.local"); err != nil {
		t.Fatalf("renewed leaf: %v", err)
	}
}

func TestEnsureProxyCA(t *testing.T) {
	cfg, _ := proxyCertTestSetup(t)
	caPath, root, rootPEM, err := ensureProxyCA(cfg)
	if err != nil {
		t.Fatalf("ensureProxyCA: %v", err)
	}
	if caPath != proxyCACertPath() {
		t.Errorf("caPath = %q, want %q", caPath, proxyCACertPath())
	}
	onDisk, err := os.ReadFile(caPath) //nolint:gosec // G304: test CA path in t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, rootPEM) {
		t.Error("returned PEM differs from ca.crt")
	}
	if !root.IsCA || root.Subject.CommonName != "jailoc proxy root CA" {
		t.Errorf("root = %v, want the jailoc proxy root CA", root.Subject)
	}
	if _, err := os.Stat(filepath.Join(proxyCADir(), proxyLeafCert)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ensureProxyCA issued a leaf certificate (stat err %v)", err)
	}
	_, again, _, err := ensureProxyCA(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Raw, root.Raw) {
		t.Error("second call replaced the CA")
	}
}

func TestEnsureProxyCARejectsExtraPEMData(t *testing.T) {
	t.Run("data around the one certificate", func(t *testing.T) {
		cfg, _ := proxyCertTestSetup(t)
		_, root, validated, err := ensureProxyCA(cfg)
		if err != nil {
			t.Fatal(err)
		}
		other, _ := testCertPEM(t, "a certificate nobody checked")
		for name, data := range map[string][]byte{
			"second block":  append(slices.Clone(validated), other...),
			"leading block": append(slices.Clone(other), validated...),
			"leading text":  append([]byte("trust me\n"), validated...),
			"trailing text": append(slices.Clone(validated), []byte("trust me\n")...),
		} {
			if err := os.WriteFile(proxyCACertPath(), data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, _, err := ensureProxyCA(cfg)
			if err == nil || !strings.Contains(err.Error(), "more than one certificate PEM block") {
				t.Errorf("%s: error = %v, want the extra data refused", name, err)
			}
		}
		if err := os.WriteFile(proxyCACertPath(), validated, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, again, _, err := ensureProxyCA(cfg); err != nil || !bytes.Equal(again.Raw, root.Raw) {
			t.Fatalf("restored CA: %v", err)
		}
	})

	t.Run("returned bytes are the validated certificate", func(t *testing.T) {
		cfg, _ := proxyCertTestSetup(t)
		_, _, first, err := ensureProxyCA(cfg)
		if err != nil {
			t.Fatal(err)
		}
		// Whitespace around the block is not data.
		if err := os.WriteFile(proxyCACertPath(), append([]byte("\n"), append(slices.Clone(first), '\n', '\n')...), 0o600); err != nil {
			t.Fatal(err)
		}
		_, root, rootPEM, err := ensureProxyCA(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if want := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}); !bytes.Equal(rootPEM, want) {
			t.Errorf("returned PEM is not the canonical encoding of the validated root:\n%s", rootPEM)
		}
	})
}
