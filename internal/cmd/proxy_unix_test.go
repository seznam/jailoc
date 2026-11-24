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
	if want := []string{"api.ws.jailoc.local", "app.ws.jailoc.local"}; !slices.Equal(hosts, want) {
		t.Errorf("hosts = %v, want %v", hosts, want)
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
	if _, err := run("--tls-cert", "c.pem"); err == nil || !strings.Contains(err.Error(), "tls-key") {
		t.Errorf("no key: error = %v, want tls-key named", err)
	}
	if _, err := run("--tls-key", "k.pem"); err == nil || !strings.Contains(err.Error(), "tls-cert") {
		t.Errorf("no cert: error = %v, want tls-cert named", err)
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
	for _, want := range []string{"proxy [workspace...]", "--foreground", "--listen", "--tls-cert", "--tls-key", proxyDefaultListen} {
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
	return startProxyRunArgs(t, handler, cfgTOML, host, g, func(certFile, keyFile string) []string {
		return []string{"--foreground", "--listen", "127.0.0.1:0", "--tls-cert", certFile, "--tls-key", keyFile}
	})
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

// startProxyRunArgs is startProxyRunFor with the command line built by
// mkArgs from the generated certificate and key paths.
func startProxyRunArgs(t *testing.T, handler http.Handler, cfgTOML, host string, g guest.Config, mkArgs func(certFile, keyFile string) []string) (*proxyRun, string) {
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
	certFile, keyFile, pool := writeTestCertificate(t, home, host)

	ctx, cancel := context.WithCancel(t.Context())
	run := &proxyRun{out: &syncBuffer{}, errOut: &syncBuffer{}, done: make(chan struct{}), cancel: cancel, host: host}
	cmd := newProxyCmd()
	cmd.SetOut(run.out)
	cmd.SetErr(run.errOut)
	cmd.SetArgs(mkArgs(certFile, keyFile))
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
		_, err := runProxyArgs(t, "--tls-cert", "c.pem", "--tls-key", "k.pem", "--listener-broker", "--listen", listen)
		if err == nil || !strings.Contains(err.Error(), "--listener-broker") || !strings.Contains(err.Error(), "--listen") {
			t.Errorf("--listen %s: error = %v, want a refusal naming both flags", listen, err)
		}
	}
}

func TestProxyListenerBrokerRefusesRoot(t *testing.T) {
	useProxyBrokerHost(t, proxyBrokerTarget{uid: 0, socketPath: "/nonexistent/b.sock", addr: proxyBrokerAddr})
	_, err := runProxyArgs(t, "--tls-cert", "c.pem", "--tls-key", "k.pem", "--listener-broker")
	if err == nil || !strings.Contains(err.Error(), "root") || !strings.Contains(err.Error(), "--listener-broker") {
		t.Fatalf("error = %v, want a refusal to run as root", err)
	}
}

func TestProxyListenerBrokerRequiresMatchingTLSFlags(t *testing.T) {
	useProxyBrokerHost(t, proxyBrokerTarget{uid: testUID(), socketPath: "/nonexistent/b.sock", addr: proxyBrokerAddr})
	if _, err := runProxyArgs(t, "--listener-broker", "--tls-cert", "c.pem"); err == nil || !strings.Contains(err.Error(), "tls-key") {
		t.Errorf("error = %v, want tls-key named", err)
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
	}), "[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", "app.e2e.jailoc.local", relayGuest(3000), func(certFile, keyFile string) []string {
		return []string{"--foreground", "--listener-broker", "--tls-cert", certFile, "--tls-key", keyFile}
	})
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

func TestEnsureIngressCert(t *testing.T) {
	home := shortTempDir(t, "jc")
	t.Setenv("HOME", home)
	t.Setenv(config.PathEnv, filepath.Join(home, "config.toml"))
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"one": {Proxy: config.ProxyMap(map[string]int{"app": 3000})},
	}}
	targets := []proxyTarget{{ws: &workspace.Resolved{Name: "one", Proxy: []workspace.ProxyRoute{{Name: "app", Port: 3000}}}}}
	cert1, caPath, err := ensureIngressCert(cfg, targets)
	if err != nil {
		t.Fatalf("ensureIngressCert: %v", err)
	}
	if caPath == "" {
		t.Fatal("caPath is empty")
	}
	if err := cert1.Leaf.VerifyHostname("app.one.jailoc.local"); err != nil {
		t.Fatalf("VerifyHostname: %v", err)
	}
	// Second call reuses the existing leaf and CA.
	cert2, _, err := ensureIngressCert(cfg, targets)
	if err != nil {
		t.Fatalf("second ensureIngressCert: %v", err)
	}
	if !bytes.Equal(cert1.Certificate[0], cert2.Certificate[0]) {
		t.Fatal("existing valid leaf was regenerated")
	}
	// Adding a workspace not covered by the first leaf regenerates the leaf with the same CA.
	cfg.Workspaces["two"] = config.Workspace{Proxy: config.ProxyMap(map[string]int{"api": 8080})}
	targets = append(targets, proxyTarget{ws: &workspace.Resolved{Name: "two", Proxy: []workspace.ProxyRoute{{Name: "api", Port: 8080}}}})
	cert3, _, err := ensureIngressCert(cfg, targets)
	if err != nil {
		t.Fatalf("third ensureIngressCert: %v", err)
	}
	if err := cert3.Leaf.VerifyHostname("api.two.jailoc.local"); err != nil {
		t.Fatalf("VerifyHostname(api.two): %v", err)
	}
	if err := cert3.Leaf.VerifyHostname("app.one.jailoc.local"); err != nil {
		t.Fatalf("VerifyHostname(app.one): %v", err)
	}
}

func TestProxySyncStatusAndStop(t *testing.T) {
	run, _ := startProxyRunArgs(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}), "[workspaces.e2e.proxy]\napp = { port = 3000, auth = true }\n", "app.e2e.jailoc.local", relayGuest(3000), func(_, _ string) []string {
		return []string{"--foreground", "--listen", "127.0.0.1:0"}
	})
	// Trust the auto-generated CA in the test client.
	caPEM, err := os.ReadFile(ingressCACertPath()) //nolint:gosec // test fixture path.
	if err != nil {
		t.Fatalf("ReadFile(ca.crt): %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("AppendCertsFromPEM failed")
	}
	run.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
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
