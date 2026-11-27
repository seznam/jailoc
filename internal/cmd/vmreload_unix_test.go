//go:build darwin || linux

package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestBrokerReloadEvent(t *testing.T) {
	t.Parallel()
	const (
		old    = `{"event":"reload","policy_error":"old"}` + "\n"
		denied = `{"event":"flow","allow":false}` + "\n"
		reload = `{"event":"reload","policy_error":"new"}` + "\n"
	)
	tests := []struct {
		name  string
		after func(t *testing.T, path string) // what the broker writes after the offset is taken
		want  string                          // policy_error of the event found, "" when none
	}{
		{"appended", func(t *testing.T, path string) { appendFile(t, path, denied+reload) }, "new"},
		{"not yet written", func(t *testing.T, path string) { appendFile(t, path, denied) }, ""},
		{"written before rotation", func(t *testing.T, path string) {
			appendFile(t, path, reload)
			rotate(t, path, denied)
		}, "new"},
		{"written after rotation", func(t *testing.T, path string) {
			rotate(t, path, denied+reload)
		}, "new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "broker.jsonl")
			appendFile(t, path, old)
			before, err := os.Open(path) //nolint:gosec // test path
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = before.Close() })
			tt.after(t, path)
			ev, ok := brokerReloadEvent(before, int64(len(old)), path)
			if got := ev.PolicyError; ok != (tt.want != "") || got != tt.want {
				t.Fatalf("brokerReloadEvent() = %q, %v; want %q", got, ok, tt.want)
			}
		})
	}
}

func appendFile(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// rotate moves path to path.1 and starts a new log with data, as the broker does.
func rotate(t *testing.T, path, data string) {
	t.Helper()
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, data)
}

func TestRestartOnlyChangesDockerDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := vm.Spec{Agent: "opencode", Docker: true, GuestProtocol: guest.Protocol, Guest: &guest.Config{}}
	tests := []struct {
		name          string
		running, want int64
		docker        bool
		changes       []string
	}{
		{"unchanged default", 0, 20 << 30, true, nil},
		{"grown", 20 << 30, 40 << 30, true, []string{"docker_disk"}},
		{"grown from a spec without disk_size", 0, 40 << 30, true, []string{"docker_disk"}},
		{"docker off", 20 << 30, 40 << 30, false, nil},
	}
	for _, tt := range tests {
		running, want := base, base
		running.DiskSize, want.DiskSize = tt.running, tt.want
		running.Docker, want.Docker = tt.docker, tt.docker
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.changes) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.changes)
		}
	}
}

func TestRestartOnlyChangesIPv6(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		protocol      int
		running, want bool
		changes       []string
	}{
		{"both off", guest.Protocol, false, false, nil},
		{"both on", guest.Protocol, true, true, nil},
		{"enable", guest.Protocol, false, true, []string{"network.ipv6"}},
		{"disable", guest.Protocol, true, false, []string{"network.ipv6"}},
		// Legacy guests and brokers enable IPv6 whatever the spec says.
		{"legacy off", 1, false, false, []string{"network.ipv6", "the jailoc guest (jailoc was upgraded)"}},
		{"legacy on", 1, false, true, []string{"the jailoc guest (jailoc was upgraded)"}},
	} {
		running := vm.Spec{Agent: "opencode", GuestProtocol: tt.protocol, Guest: &guest.Config{IPv6: tt.running}}
		want := vm.Spec{Agent: "opencode", GuestProtocol: guest.Protocol, Guest: &guest.Config{IPv6: tt.want}}
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.changes) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.changes)
		}
	}
}

func TestRestartOnlyChangesSession(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                string
		runningEnv, wantEnv []string
		runningDir, wantDir string
		changes             []string
	}{
		{"unchanged", []string{"A=1"}, []string{"A=1"}, "/w", "/w", nil},
		{"env", []string{"A=1"}, []string{"A=2"}, "/w", "/w", []string{"TUI plugins"}},
		{"working directory", nil, nil, "/w", "/x", []string{"the working directory"}},
		{"both", nil, []string{"A=1"}, "/w", "/x", []string{"TUI plugins", "the working directory"}},
	} {
		running := vm.Spec{Agent: "opencode", GuestProtocol: guest.Protocol, Guest: &guest.Config{Env: tt.runningEnv, WorkDir: tt.runningDir}}
		want := vm.Spec{Agent: "opencode", GuestProtocol: guest.Protocol, Guest: &guest.Config{Env: tt.wantEnv, WorkDir: tt.wantDir}}
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.changes) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.changes)
		}
	}
}

func TestRestartOnlyChangesTmpfs(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                    string
		runningAgent, wantAgent string
		runningTmpfs, wantTmpfs []string
		want                    []string
	}{
		{"unchanged", "omo", "omo", []string{"/home/agent/.omo/agent/rpc"}, []string{"/home/agent/.omo/agent/rpc"}, nil},
		{"added tmpfs on upgrade", "omo", "omo", nil, []string{"/home/agent/.omo/agent/rpc"}, []string{"tmpfs mounts"}},
		{"agent changed reports agent only", "opencode", "omo", nil, []string{"/home/agent/.omo/agent/rpc"}, []string{"agent"}},
	} {
		running := vm.Spec{Agent: tt.runningAgent, GuestProtocol: guest.Protocol, Guest: &guest.Config{Tmpfs: tt.runningTmpfs}}
		want := vm.Spec{Agent: tt.wantAgent, GuestProtocol: guest.Protocol, Guest: &guest.Config{Tmpfs: tt.wantTmpfs}}
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestReloadPolicyIPv6(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		protocol int
		guest    *guest.Config
		want     bool
		ipv6     bool
	}{
		{"legacy guest", 1, &guest.Config{}, true, false},
		{"legacy guest with ipv6 spec", 1, &guest.Config{IPv6: true}, true, false},
		{"guest booted without ipv6", guest.Protocol, &guest.Config{}, true, false},
		{"no guest config", guest.Protocol, nil, true, false},
		{"guest booted with ipv6", guest.Protocol, &guest.Config{IPv6: true}, true, true},
		{"disable ipv6", guest.Protocol, &guest.Config{IPv6: true}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			running := vm.Spec{GuestProtocol: tt.protocol, Guest: tt.guest}
			ws := &workspace.Resolved{IPv6: tt.want}
			data, err := reloadPolicy(context.Background(), ws, running)
			if err != nil {
				t.Fatal(err)
			}
			var policy map[string]any
			if err := json.Unmarshal(data, &policy); err != nil {
				t.Fatal(err)
			}
			if got := policy["ipv6"] == true; got != tt.ipv6 {
				t.Errorf("policy %s: ipv6 = %v, want %v", data, got, tt.ipv6)
			}
			if ws.IPv6 != tt.want {
				t.Error("reloadPolicy changed the workspace")
			}
		})
	}
}

func TestReloadPolicyRejectsOlderBrokerForRules(t *testing.T) {
	t.Parallel()
	running := vm.Spec{GuestProtocol: 2, Guest: &guest.Config{}}
	ws := &workspace.Resolved{Name: "api", DNSUpstream: "9.9.9.9"}
	if _, err := reloadPolicy(context.Background(), ws, running); err == nil {
		t.Fatal("reloadPolicy accepted rules policy for protocol 2 broker")
	}
}

func TestReloadPolicyRejectsBrokerBeforeInternalZones(t *testing.T) {
	t.Parallel()
	ws := &workspace.Resolved{Name: "api", NetworkRules: []config.NetworkRule{{Name: "agent", Allow: []string{"api.anthropic.com"}}}}
	old := vm.Spec{GuestProtocol: guest.Protocol, Guest: &guest.Config{}}
	if _, err := reloadPolicy(context.Background(), ws, old); err == nil {
		t.Fatal("reloadPolicy accepted a broker that predates internal_zones")
	}
	current := old
	current.BrokerPolicy = netbroker.PolicyVersion
	if _, err := reloadPolicy(context.Background(), ws, current); err != nil {
		t.Fatalf("reloadPolicy with a current broker: %v", err)
	}
}

func TestReloadPolicyRequiresBrokerForNewFeatures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	off := false
	plain := []config.NetworkRule{{Name: "agent", Allow: []string{"api.anthropic.com"}}}
	httpRule := config.NetworkRule{Name: "gh", Allow: []string{"api.github.com"}, HTTP: &config.HTTPMatch{Methods: []string{"GET"}}}
	quietRule := config.NetworkRule{Name: "registry", Allow: []string{"registry.example.com"}, Log: &off}
	tests := []struct {
		name    string
		rules   []config.NetworkRule
		running int
		wantErr bool
	}{
		{"plain rules on a version 1 broker", plain, policyVersionRules, false},
		{"http rules on a version 1 broker", append(slices.Clone(plain), httpRule), policyVersionRules, true},
		{"http rules on a version 2 broker", append(slices.Clone(plain), httpRule), policyVersionHTTP, false},
		{"http rules on a current broker", append(slices.Clone(plain), httpRule), netbroker.PolicyVersion, false},
		{"quiet rule on a version 1 broker", append(slices.Clone(plain), quietRule), policyVersionRules, true},
		{"quiet rule on a version 2 broker", append(slices.Clone(plain), quietRule), policyVersionHTTP, true},
		{"quiet rule on a current broker", append(slices.Clone(plain), quietRule), netbroker.PolicyVersion, false},
		{"quiet and http rules on a version 2 broker", append(slices.Clone(plain), httpRule, quietRule), policyVersionHTTP, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &workspace.Resolved{Name: "api", NetworkRules: tt.rules}
			running := vm.Spec{GuestProtocol: guest.Protocol, Guest: &guest.Config{}, BrokerPolicy: tt.running}
			_, err := reloadPolicy(context.Background(), ws, running)
			if (err != nil) != tt.wantErr {
				t.Fatalf("reloadPolicy error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestReloadSpecRunningSharesExcludeSnapshots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeMD := filepath.Join(home, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(claudeMD), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeMD, []byte("# rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claude, _ := agent.Lookup("claude")
	ws := &workspace.Resolved{
		Name:              "w",
		Agent:             claude,
		ImportAgentConfig: true,
		CPU:               2,
		Memory:            "2g",
		DockerDisk:        "20g",
		StateDisk:         "10g",
		CacheDisk:         "20g",
	}
	running := vm.Spec{
		GuestProtocol: guest.Protocol,
		Guest:         &guest.Config{},
		Shares:        []vm.Share{{HostPath: "/p/claude", GuestPath: guestHome + "/.claude"}},
	}
	_, snapshots, err := reloadSpec(context.Background(), ws, running)
	if err != nil {
		t.Fatalf("reloadSpec() error = %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("snapshots = %v, want empty when running share covers ~/.claude", snapshots)
	}
}

func TestRestartOnlyChangesProxy(t *testing.T) {
	t.Parallel()
	relay := func(ports ...uint16) *guest.ProxyRelay { return &guest.ProxyRelay{Ports: ports} }
	for _, tt := range []struct {
		name          string
		running, want guest.Config
		// restart is whether a VM that cannot replace its relay allowlist
		// lists proxy as restart-only; a VM that can never does.
		restart bool
	}{
		{"unchanged none", guest.Config{}, guest.Config{}, false},
		{"unchanged proxy", guest.Config{ProxyRelay: relay(3000, 6080)}, guest.Config{ProxyRelay: relay(3000, 6080)}, false},
		{"proxy added", guest.Config{}, guest.Config{ProxyRelay: relay(3000)}, true},
		{"proxy added to an empty relay", guest.Config{ProxyRelay: relay()}, guest.Config{ProxyRelay: relay(3000)}, true},
		{"proxy port changed", guest.Config{ProxyRelay: relay(3000)}, guest.Config{ProxyRelay: relay(3001)}, true},
		{"proxy removed", guest.Config{ProxyRelay: relay(3000)}, guest.Config{}, true},
		{"proxy removed to an empty relay", guest.Config{ProxyRelay: relay(3000)}, guest.Config{ProxyRelay: relay()}, true},
		{"empty relay equals none", guest.Config{ProxyRelay: relay()}, guest.Config{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range []struct {
				name     string
				protocol int
				want     bool
			}{
				{"old guest", liveProxyGuestProtocol - 1, tt.restart},
				{"live proxy guest", liveProxyGuestProtocol, false},
			} {
				running := vm.Spec{Agent: "opencode", GuestProtocol: c.protocol, Guest: &tt.running}
				want := vm.Spec{Agent: "opencode", GuestProtocol: guest.Protocol, Guest: &tt.want}
				got := slices.Contains(restartOnlyChanges(running, want), "proxy")
				if got != c.want {
					t.Errorf("%s: proxy restart-only = %v, want %v", c.name, got, c.want)
				}
			}
		})
	}
}

func TestProxyReloadable(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		protocol int
		want     bool
	}{
		{0, false},
		{liveProxyGuestProtocol - 1, false},
		{liveProxyGuestProtocol, true},
		{liveProxyGuestProtocol + 1, true},
	} {
		if got := proxyReloadable(vm.Spec{GuestProtocol: tt.protocol}); got != tt.want {
			t.Errorf("proxyReloadable(protocol %d) = %v, want %v", tt.protocol, got, tt.want)
		}
	}
	if guest.Protocol < liveProxyGuestProtocol {
		t.Errorf("guest.Protocol = %d is below liveProxyGuestProtocol %d", guest.Protocol, liveProxyGuestProtocol)
	}
}

func TestReloadedGuestProxy(t *testing.T) {
	t.Parallel()
	running := guest.Config{
		ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000, 6080}},
		CABundle:   "/old.pem",
		TimeZone:   &guest.TimeZone{},
	}
	// The configuration asks for another proxy setup.
	want := guest.Config{
		ProxyRelay: &guest.ProxyRelay{Ports: []uint16{9000}},
		CABundle:   "/new.pem",
	}
	binds := []guest.Snapshot{{Source: "/a", Target: "/snap"}}
	tests := []struct {
		name      string
		want      guest.Config
		liveProxy bool
		ports     []uint16
	}{
		{"old guest retains its allowlist", want, false, []uint16{3000, 6080}},
		{"live proxy guest takes the wanted allowlist", want, true, []uint16{9000}},
		{"live proxy guest without routes keeps an empty relay", guest.Config{CABundle: "/new.pem"}, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := reloadedGuest(running, tt.want, binds, tt.liveProxy)
			if got.ProxyRelay == nil || !slices.Equal(got.ProxyRelay.Ports, tt.ports) {
				t.Errorf("reloaded guest proxy = %+v, want ports %v", got.ProxyRelay, tt.ports)
			}
			if got.CABundle != "/new.pem" || got.TimeZone != nil || len(got.Snapshots) != 1 {
				t.Errorf("reloaded guest did not take the reloadable settings: %+v", got)
			}
		})
	}
	if running.ProxyRelay.Ports[0] != 3000 {
		t.Errorf("reloadedGuest changed the running configuration: %+v", running.ProxyRelay)
	}
}

func TestReloadProxyPorts(t *testing.T) {
	t.Parallel()
	relay := func(ports ...uint16) guest.Config { return guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: ports}} }
	tests := []struct {
		name     string
		protocol int
		g        guest.Config
		want     *string // JSON of the proxy_ports field; nil when it must be absent
	}{
		{"old guest sends nothing", liveProxyGuestProtocol - 1, relay(3000), nil},
		{"ports", liveProxyGuestProtocol, relay(3000, 6080), ptr(`"proxy_ports":[3000,6080]`)},
		{"empty relay sends an empty list", liveProxyGuestProtocol, relay(), ptr(`"proxy_ports":[]`)},
		{"no relay sends an empty list", liveProxyGuestProtocol, guest.Config{}, ptr(`"proxy_ports":[]`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ports := reloadProxyPorts(vm.Spec{GuestProtocol: tt.protocol}, tt.g)
			data, err := json.Marshal(guest.Reload{Seq: 1, ProxyPorts: ports})
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == nil {
				if strings.Contains(string(data), "proxy_ports") {
					t.Fatalf("request %s has proxy_ports", data)
				}
				return
			}
			if !strings.Contains(string(data), *tt.want) {
				t.Fatalf("request %s lacks %s", data, *tt.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestCheckReloadProxyCollision(t *testing.T) {
	t.Parallel()
	proxyRunning := guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000}}}
	hostPort := func(port uint16) vm.Spec {
		return vm.Spec{
			HostServices: []vm.HostService{{GuestPort: port, TCP: "127.0.0.1:9000"}},
			Guest:        &guest.Config{HostPorts: []uint16{port}},
		}
	}
	forward := func(port int) vm.Spec {
		return vm.Spec{Forwards: []vm.Forward{{HostPort: 18000, GuestPort: port}}, Guest: &guest.Config{}}
	}
	tests := []struct {
		name    string
		running guest.Config
		want    vm.Spec
		wantErr string // "" for a reload that is fine
	}{
		{"no relay, any host port", guest.Config{}, hostPort(3000), ""},
		{"no relay, relay port forward", guest.Config{}, forward(6081), ""},
		{"unrelated host port", proxyRunning, hostPort(9090), ""},
		{"unrelated forward", proxyRunning, forward(8080), ""},
		{"no host ports at all", proxyRunning, vm.Spec{Guest: &guest.Config{}}, ""},
		{"proxy allowlist port as host port after proxy was removed", proxyRunning, hostPort(3000), "guest port 3000"},
		{"relay port as host port", proxyRunning, hostPort(6081), "guest port 6081"},
		{"relay port as forward", proxyRunning, forward(6081), "guest port 6081"},
		{"allowlist port as forward", proxyRunning, forward(3000), "guest port 3000"},
		{"host service only in the spec list", proxyRunning, vm.Spec{HostServices: []vm.HostService{{GuestPort: 3000}}, Guest: &guest.Config{}}, "guest port 3000"},
		{"host port only in the guest list", proxyRunning, vm.Spec{Guest: &guest.Config{HostPorts: []uint16{3000}}}, "guest port 3000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			running := vm.Spec{Guest: &tt.running}
			err := checkReloadProxyCollision("ws", running, tt.want)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("got %v, want a reload that is allowed", err)
				}
				return
			}
			if err == nil {
				t.Fatal("reload that exposes a running proxy port was allowed")
			}
			for _, want := range []string{tt.wantErr, "jailoc restart ws"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// A guest that replaces its relay allowlist on a reload reserves only the relay
// port: the route ports follow the configuration, which validation checks.
func TestCheckReloadProxyCollisionLiveProxy(t *testing.T) {
	t.Parallel()
	running := vm.Spec{GuestProtocol: liveProxyGuestProtocol, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000}}}}
	hostPort := func(port uint16) vm.Spec {
		return vm.Spec{HostServices: []vm.HostService{{GuestPort: port}}, Guest: &guest.Config{HostPorts: []uint16{port}}}
	}
	if err := checkReloadProxyCollision("ws", running, hostPort(3000)); err != nil {
		t.Errorf("former route port as host port: %v", err)
	}
	if err := checkReloadProxyCollision("ws", running, hostPort(6081)); err == nil || !strings.Contains(err.Error(), "guest port 6081") {
		t.Errorf("relay port as host port: err = %v, want a guest port 6081 error", err)
	}
	empty := vm.Spec{GuestProtocol: liveProxyGuestProtocol, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{}}}
	if err := checkReloadProxyCollision("ws", empty, vm.Spec{Forwards: []vm.Forward{{HostPort: 18000, GuestPort: 6081}}, Guest: &guest.Config{}}); err == nil {
		t.Error("relay port as forward with an empty relay was allowed")
	}
}

// The reload that removes proxy from the configuration while the VM still runs
// it must not turn its ports into host ports.
func TestReloadWantAfterProxyRemovedKeepsCollisionVisible(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{
		Name: "ws", CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g",
		HostPorts: []config.HostPortTarget{{Guest: 3000, HostPort: 9000}},
		Ports:     []config.PortForward{{Host: 18000, Guest: 6081}},
	}
	want, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want.Guest.ProxyRelay == nil || len(want.Guest.ProxyRelay.Ports) != 0 {
		t.Fatalf("the wanted spec still has proxy routes: %+v", want.Guest)
	}
	running := vm.Spec{GuestProtocol: liveProxyGuestProtocol - 1, Guest: &guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000, 6080}}}}
	if err := checkReloadProxyCollision("ws", running, want); err == nil {
		t.Fatal("removing proxy from the configuration made its ports reloadable as host ports")
	}
}

func TestRestartOnlyChangesStateAndCacheDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := vm.Spec{Agent: "opencode", GuestProtocol: guest.Protocol, Guest: &guest.Config{}}
	tests := []struct {
		name                string
		stateOn, cacheOn    bool
		runState, wantState int64
		runCache, wantCache int64
		changes             []string
	}{
		{name: "unchanged defaults", stateOn: true, cacheOn: true, wantState: 10 << 30, wantCache: 20 << 30},
		{name: "state grown", stateOn: true, runState: 10 << 30, wantState: 20 << 30, changes: []string{"state_disk"}},
		{name: "cache grown from a spec without size", cacheOn: true, wantCache: 40 << 30, changes: []string{"cache_disk"}},
		{name: "both grown", stateOn: true, cacheOn: true, runState: 10 << 30, wantState: 11 << 30, runCache: 20 << 30, wantCache: 30 << 30, changes: []string{"state_disk", "cache_disk"}},
		{name: "disks off", runState: 10 << 30, wantState: 20 << 30, runCache: 20 << 30, wantCache: 30 << 30},
	}
	for _, tt := range tests {
		running, want := base, base
		running.StateDisk, want.StateDisk = tt.stateOn, tt.stateOn
		running.CacheDisk, want.CacheDisk = tt.cacheOn, tt.cacheOn
		running.StateDiskSize, want.StateDiskSize = tt.runState, tt.wantState
		running.CacheDiskSize, want.CacheDiskSize = tt.runCache, tt.wantCache
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.changes) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.changes)
		}
	}
}

func TestRestartOnlyChangesBuildCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := vm.Spec{Agent: "opencode", GuestProtocol: guest.Protocol, Guest: &guest.Config{}}
	const bundleA, bundleB = "sha256:aaaa", "sha256:bbbb"
	const label = "ca_bundle (for image builds)"
	tests := []struct {
		name          string
		running, want string
		changes       []string
	}{
		{name: "unchanged", running: bundleA, want: bundleA},
		{name: "unchanged without bundle", running: "none", want: "none"},
		{name: "bundle replaced", running: bundleA, want: bundleB, changes: []string{label}},
		{name: "bundle removed", running: bundleA, want: "none", changes: []string{label}},
		{name: "bundle added", running: "none", want: bundleA, changes: []string{label}},
		{name: "legacy VM without a record", running: "", want: bundleA},
		{name: "legacy VM without a record, no bundle", running: "", want: "none"},
		{name: "image not built by jailoc", running: bundleA, want: ""},
	}
	for _, tt := range tests {
		running, want := base, base
		running.BuildCA, want.BuildCA = tt.running, tt.want
		if got := restartOnlyChanges(running, want); !slices.Equal(got, tt.changes) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.changes)
		}
	}
}

func TestReloadSpecBuildCA(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "company.pem"), "company")
	ws := &workspace.Resolved{Name: "ws", CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g", CABundle: config.CustomCABundle(bundle.path)}
	running := vm.Spec{Image: "image", BuildCA: "sha256:old", Guest: &guest.Config{}}

	want, _, err := reloadSpec(t.Context(), ws, running)
	if err != nil {
		t.Fatalf("reloadSpec: %v", err)
	}
	sum := sha256.Sum256(bundle.data)
	if wantCA := "sha256:" + hex.EncodeToString(sum[:]); want.BuildCA != wantCA {
		t.Errorf("BuildCA = %q, want %q", want.BuildCA, wantCA)
	}
	if got := restartOnlyChanges(running, want); !slices.Contains(got, "ca_bundle (for image builds)") {
		t.Errorf("restartOnlyChanges = %v, want ca_bundle (for image builds)", got)
	}
}
