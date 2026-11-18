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
	"maps"
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
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestParseGatewayListen(t *testing.T) {
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
			got, err := parseGatewayListen(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseGatewayListen(%q) = %q, %v; want error containing %q", tt.in, got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("parseGatewayListen(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestGatewayHostAndOrigin(t *testing.T) {
	t.Parallel()
	if got, want := gatewayHost("desktop", "dev"), "desktop.dev.jailoc.local"; got != want {
		t.Errorf("gatewayHost = %q, want %q", got, want)
	}
	if got, want := gatewayOrigin("a.dev.jailoc.local"), "https://a.dev.jailoc.local"; got != want {
		t.Errorf("origin = %q, want %q", got, want)
	}
	if got, want := gatewayURL("a.dev.jailoc.local"), "https://a.dev.jailoc.local/"; got != want {
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
	if err := os.WriteFile(vm.WebSocket(stateDir), nil, 0o600); err != nil {
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

// replaceWebSocket swaps in a new file at path while the old one still exists, so
// the new one never reuses its inode.
func replaceWebSocket(t *testing.T, path string) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func relayGuest(desktop bool, ports ...uint16) guest.Config {
	g := guest.Config{WebRelay: &guest.WebRelay{Ports: ports}}
	if desktop {
		g.Desktop = &guest.Desktop{}
	}
	return g
}

func gatewayTestConfig() *config.Config {
	yes := true
	return &config.Config{Workspaces: map[string]config.Workspace{
		"app":     {Web: map[string]int{"app": 3000, "api": 8080}},
		"desk":    {EnableDesktop: &yes},
		"plain":   {},
		"stopped": {Web: map[string]int{"app": 3000}},
	}}
}

func TestSelectGatewayTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeVM(t, "app", relayGuest(false, 3000, 8080))
	fakeVM(t, "desk", relayGuest(true, 6080))
	fakeVM(t, "plain", guest.Config{})
	cfg := gatewayTestConfig()

	names := func(targets []gatewayTarget) []string {
		var out []string
		for _, tg := range targets {
			out = append(out, tg.ws.Name)
		}
		return out
	}

	t.Run("all configured, sorted, skipping stopped and routeless", func(t *testing.T) {
		var note bytes.Buffer
		got, err := selectGatewayTargets(cfg, nil, &note)
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
		got, err := selectGatewayTargets(cfg, []string{"desk", "app", "desk"}, io.Discard)
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
		{"explicit without routes fails", []string{"plain"}, "has no web routes"},
		{"explicit unknown fails", []string{"nope"}, `resolve workspace "nope"`},
		{"explicit stopped fails even next to a running one", []string{"app", "stopped"}, "is not running"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := selectGatewayTargets(cfg, tt.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	t.Run("nothing to serve", func(t *testing.T) {
		only := &config.Config{Workspaces: map[string]config.Workspace{"stopped": cfg.Workspaces["stopped"], "plain": {}}}
		_, err := selectGatewayTargets(only, nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "no running workspace with web routes") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestSelectGatewayTargetsRejectsRunningMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	yes := true
	for _, tt := range []struct {
		name    string
		ws      config.Workspace
		running guest.Config
		want    string
	}{
		{"web route added since start", config.Workspace{Web: map[string]int{"app": 3000, "api": 8080}}, relayGuest(false, 3000), "configured web"},
		{"web port changed since start", config.Workspace{Web: map[string]int{"app": 3001}}, relayGuest(false, 3000), "configured web"},
		{"desktop enabled since start", config.Workspace{EnableDesktop: &yes, Web: map[string]int{"app": 3000}}, relayGuest(false, 3000), "configured enable_desktop and web"},
		{"desktop disabled since start", config.Workspace{Web: map[string]int{"app": 3000}}, relayGuest(true, 3000, 6080), "configured enable_desktop and web"},
		{"running VM without a guest relay", config.Workspace{Web: map[string]int{"app": 3000}}, guest.Config{}, "configured web"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			name := "ws"
			fakeVM(t, name, tt.running)
			cfg := &config.Config{Workspaces: map[string]config.Workspace{name: tt.ws}}
			for _, args := range [][]string{nil, {name}} {
				_, err := selectGatewayTargets(cfg, args, io.Discard)
				if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "jailoc restart ws") {
					t.Errorf("args %v: error = %v, want a restart hint containing %q", args, err, tt.want)
				}
			}
		})
	}
	t.Run("renamed route on the same port is safe", func(t *testing.T) {
		fakeVM(t, "ws", relayGuest(false, 3000))
		cfg := &config.Config{Workspaces: map[string]config.Workspace{"ws": {Web: map[string]int{"renamed": 3000}}}}
		if _, err := selectGatewayTargets(cfg, []string{"ws"}, io.Discard); err != nil {
			t.Fatal(err)
		}
	})
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
	replaceWebSocket(t, vm.WebSocket(stateDir))
	sameBoot, err := readVMIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.same(sameBoot) {
		t.Error("a new web socket must be a different VM identity")
	}
	writeFakeState(t, stateDir, vm.State{PID: os.Getppid(), BrokerPID: os.Getpid(), Ready: true})
	other, err := readVMIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if sameBoot.same(other) {
		t.Error("another supervisor pid must be a different VM identity")
	}
	if err := os.Remove(vm.WebSocket(stateDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := readVMIdentity(stateDir); err == nil || errors.Is(err, errVMNotRunning) {
		t.Errorf("missing web socket: %v, want a restart hint", err)
	}
}

// testInstance serves workspace "ws" with routes app (3000) and api (8080),
// whose VM identity read returns *id.
func testInstance(t *testing.T, id *vmIdentity, dial func(context.Context, string, uint16) (net.Conn, error)) (*gatewayInstance, *bytes.Buffer) {
	t.Helper()
	var errw bytes.Buffer
	ws := &workspace.Resolved{Name: "ws", Web: []workspace.WebRoute{{Name: "api", Port: 8080}, {Name: "app", Port: 3000}}}
	inst := newGatewayInstance(t.Context(), gatewayTarget{ws: ws, stateDir: "/state", id: *id}, &errw)
	inst.read = func(string) (vmIdentity, error) { return *id, nil }
	inst.dial = dial
	return inst, &errw
}

func TestGatewayRoutes(t *testing.T) {
	t.Parallel()
	id := &vmIdentity{pid: 1}
	inst, _ := testInstance(t, id, nil)
	var hosts []string
	for _, r := range inst.routes() {
		hosts = append(hosts, r.Host)
	}
	if want := []string{"api.ws.jailoc.local", "app.ws.jailoc.local"}; !slices.Equal(hosts, want) {
		t.Errorf("hosts = %v, want %v", hosts, want)
	}
}

func TestGatewayRouteStartPaths(t *testing.T) {
	t.Parallel()
	var errw bytes.Buffer
	ws := &workspace.Resolved{Name: "ws", Web: []workspace.WebRoute{{Name: "desktop", Port: 6080}, {Name: "foo", Port: 3000}}}
	inst := newGatewayInstance(t.Context(), gatewayTarget{ws: ws, stateDir: "/state"}, &errw)
	got := map[string]string{}
	for _, r := range inst.routes() {
		got[r.Host] = r.StartPath
	}
	want := map[string]string{"desktop.ws.jailoc.local": "/vnc.html?autoconnect=1", "foo.ws.jailoc.local": ""}
	if !maps.Equal(got, want) {
		t.Errorf("start paths = %v, want %v", got, want)
	}
}

func TestGatewayRouteDialsItsPort(t *testing.T) {
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

func TestGatewayRouteFailsClosedWhenVMGone(t *testing.T) {
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
	if _, err := route.Dial(t.Context()); !errors.Is(err, errGatewayVMGone) {
		t.Fatalf("dial after replacement: %v, want errGatewayVMGone", err)
	}
	if dials.Load() != 1 {
		t.Errorf("dialed %d times, want the replacement VM never dialed", dials.Load())
	}
	// Even if the replacement identity flips back, the instance stays dead.
	*id = vmIdentity{pid: 1}
	if _, err := inst.routes()[1].Dial(t.Context()); !errors.Is(err, errGatewayVMGone) {
		t.Errorf("dial after the identity reverted: %v, want errGatewayVMGone", err)
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
	if !strings.Contains(errw.String(), "restart `jailoc gateway`") {
		t.Errorf("stderr = %q, want a restart hint", errw.String())
	}
}

func TestGatewayRouteDialDuringReplacement(t *testing.T) {
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
	if _, err := inst.routes()[0].Dial(t.Context()); !errors.Is(err, errGatewayVMGone) {
		t.Fatalf("dial: %v, want errGatewayVMGone", err)
	}
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Error("connection to the replacement VM was left open")
	}
}

func TestGatewayRouteDialError(t *testing.T) {
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

func TestGatewayInstanceWatch(t *testing.T) {
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
	if !errors.Is(context.Cause(inst.ctx), errGatewayVMGone) {
		t.Errorf("cause = %v, want errGatewayVMGone", context.Cause(inst.ctx))
	}
}

func TestGatewayInstanceWatchStopsWithParent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	id := &vmIdentity{pid: 1}
	var errw bytes.Buffer
	ws := &workspace.Resolved{Name: "ws"}
	inst := newGatewayInstance(ctx, gatewayTarget{ws: ws, id: *id}, &errw)
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

func TestPrintGatewayRoutes(t *testing.T) {
	t.Parallel()
	targets := []gatewayTarget{
		{ws: &workspace.Resolved{Name: "dev", Web: []workspace.WebRoute{{Name: "app", Port: 3000}, {Name: "desktop", Port: 6080}}}},
	}
	codes := map[string]string{"app.dev.jailoc.local": "CODE-APP", "desktop.dev.jailoc.local": "CODE-DESK"}
	var out bytes.Buffer
	printGatewayRoutes(&out, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8443}, targets, codes)
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
	printGatewayRoutes(&out, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}, targets, codes)
	if strings.Contains(out.String(), "forward") {
		t.Errorf("port 443 needs no forward:\n%s", out.String())
	}
}

func TestGatewayCommandArguments(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		cmd := newGatewayCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(t.Context())
		return out.String(), err
	}
	if _, err := run(); err == nil || !strings.Contains(err.Error(), `"tls-cert"`) || !strings.Contains(err.Error(), `"tls-key"`) {
		t.Errorf("no flags: error = %v, want the required TLS flags named", err)
	}
	if _, err := run("--tls-cert", "c.pem"); err == nil || !strings.Contains(err.Error(), `"tls-key"`) {
		t.Errorf("no key: error = %v, want tls-key named", err)
	}
	for _, listen := range []string{"0.0.0.0:8443", "example.com:443", "8443"} {
		if _, err := run("--tls-cert", "c.pem", "--tls-key", "k.pem", "--listen", listen); err == nil || !strings.Contains(err.Error(), "--listen") {
			t.Errorf("--listen %s: error = %v, want a --listen error", listen, err)
		}
	}
	dir := t.TempDir()
	if _, err := run("--tls-cert", filepath.Join(dir, "missing.pem"), "--tls-key", filepath.Join(dir, "missing.key")); err == nil || !strings.Contains(err.Error(), "load TLS certificate") {
		t.Errorf("missing certificate: error = %v", err)
	}
	out, err := run("--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gateway [workspace...]", "--listen", "--tls-cert", "--tls-key", gatewayDefaultListen} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}

// writeTestCertificate writes a generated self-signed certificate for hosts
// and its key to dir.
func writeTestCertificate(t *testing.T, dir string, hosts ...string) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: hosts,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(leaf)
	return certFile, keyFile, pool
}

// fakeWebRelay listens on the web socket of stateDir like the guest relay
// through the broker: it answers the 2-byte port handshake with "connected"
// and serves HTTP on the connection.
func fakeWebRelay(t *testing.T, stateDir string, handler http.Handler) {
	t.Helper()
	ln, err := net.Listen("unix", vm.WebSocket(stateDir))
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

type gatewayRun struct {
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
	gatewayAddrRe = regexp.MustCompile(`Gateway listening on (127\.0\.0\.1:\d+)`)
	gatewayCodeRe = regexp.MustCompile(`login code (\S+) \(only valid for`)
)

// startGatewayRun runs `jailoc gateway` for the web route app of workspace
// "e2e" on an ephemeral port, with a running fake VM whose relay serves
// handler, and waits until it prints its address and login code.
func startGatewayRun(t *testing.T, handler http.Handler) (*gatewayRun, string) {
	t.Helper()
	return startGatewayRunFor(t, handler, "[workspaces.e2e.web]\napp = 3000\n", "app.e2e.jailoc.local", relayGuest(false, 3000))
}

// startGatewayRunFor is startGatewayRun for the config text cfgTOML, serving
// the route at host from a fake VM configured as g.
func startGatewayRunFor(t *testing.T, handler http.Handler, cfgTOML, host string, g guest.Config) (*gatewayRun, string) {
	t.Helper()
	// A short path: unix socket paths are limited to about 100 bytes.
	home, err := os.MkdirTemp("/tmp", "jg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PathEnv, cfgPath)
	stateDir := fakeVM(t, "e2e", g)
	if err := os.Remove(vm.WebSocket(stateDir)); err != nil {
		t.Fatal(err)
	}
	fakeWebRelay(t, stateDir, handler)
	certFile, keyFile, pool := writeTestCertificate(t, home, host)

	ctx, cancel := context.WithCancel(t.Context())
	run := &gatewayRun{out: &syncBuffer{}, errOut: &syncBuffer{}, done: make(chan struct{}), cancel: cancel, host: host}
	cmd := newGatewayCmd()
	cmd.SetOut(run.out)
	cmd.SetErr(run.errOut)
	cmd.SetArgs([]string{"--listen", "127.0.0.1:0", "--tls-cert", certFile, "--tls-key", keyFile})
	go func() {
		run.err = cmd.ExecuteContext(ctx)
		close(run.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(5 * time.Second):
			t.Error("gateway did not stop")
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a, c := gatewayAddrRe.FindStringSubmatch(run.out.String()), gatewayCodeRe.FindStringSubmatch(run.out.String())
		if a != nil && c != nil {
			run.addr, run.code = a[1], c[1]
			break
		}
		select {
		case <-run.done:
			t.Fatalf("gateway ended early: %v\nstdout: %s\nstderr: %s", run.err, run.out.String(), run.errOut.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if run.addr == "" {
		t.Fatalf("gateway printed no address and code:\n%s", run.out.String())
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12},
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

func (r *gatewayRun) do(t *testing.T, method, path string, form url.Values, cookie *http.Cookie) *http.Response {
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

func (r *gatewayRun) login(t *testing.T) *http.Cookie {
	t.Helper()
	res := r.do(t, "POST", "/.jailoc/login", url.Values{"code": {r.code}}, nil)
	if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 1 {
		t.Fatalf("login: status %d, cookies %v", res.StatusCode, res.Cookies())
	}
	return res.Cookies()[0]
}

func TestGatewayDesktopLandsOnNoVNC(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	run, _ := startGatewayRunFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RequestURI())
		mu.Unlock()
		_, _ = io.WriteString(w, "novnc")
	}), "[workspaces.e2e]\nenable_desktop = true\n", "desktop.e2e.jailoc.local", relayGuest(true, 6080))
	if out := run.out.String(); !strings.Contains(out, "https://desktop.e2e.jailoc.local/\n") {
		t.Errorf("output must show the canonical root URL:\n%s", out)
	}
	res := run.do(t, "GET", "/", nil, nil)
	if got := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || got != "/.jailoc/login" {
		t.Fatalf("without a session: status %d, Location %q", res.StatusCode, got)
	}
	res = run.do(t, "POST", "/.jailoc/login", url.Values{"code": {run.code}}, nil)
	if got := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || got != "/vnc.html?autoconnect=1" {
		t.Fatalf("login: status %d, Location %q", res.StatusCode, got)
	}
	cookie := res.Cookies()[0]
	res = run.do(t, "GET", "/", nil, cookie)
	if got := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || got != "/vnc.html?autoconnect=1" {
		t.Fatalf("root: status %d, Location %q", res.StatusCode, got)
	}
	res = run.do(t, "GET", "/vnc.html?autoconnect=1", nil, cookie)
	if body, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK || string(body) != "novnc" {
		t.Fatalf("landing page: status %d, body %q", res.StatusCode, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, []string{"/vnc.html?autoconnect=1"}) {
		t.Errorf("upstream saw %v, want only the landing page", seen)
	}
}

func TestGatewayServesAndFailsClosedWhenVMReplaced(t *testing.T) {
	run, stateDir := startGatewayRun(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		if !errors.Is(run.err, errGatewayNoVMs) {
			t.Fatalf("gateway ended with %v, want errGatewayNoVMs", run.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway kept running after its only VM was replaced")
	}
	if !strings.Contains(run.errOut.String(), "Workspace e2e:") {
		t.Errorf("stderr = %q, want the lost VM reported", run.errOut.String())
	}
}

func TestGatewayStopsOnContextCancel(t *testing.T) {
	run, _ := startGatewayRun(t, http.NotFoundHandler())
	run.cancel()
	select {
	case <-run.done:
		if run.err != nil {
			t.Fatalf("gateway ended with %v on cancel, want nil", run.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway ignored cancellation")
	}
}
