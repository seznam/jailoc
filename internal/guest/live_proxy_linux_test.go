package guest

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLiveStateApplyProxyPorts(t *testing.T) {
	t.Parallel()
	ports := func(p ...uint16) *[]uint16 { return &p }
	tests := []struct {
		name      string
		relay     bool
		in        *[]uint16
		wantErr   string
		wantAllow []uint16 // ports the relay must allow afterwards
		wantDeny  []uint16
	}{
		{"absent leaves the allowlist", true, nil, "", []uint16{3000}, []uint16{4000}},
		{"replaces the allowlist", true, ports(4000), "", []uint16{4000}, []uint16{3000}},
		{"empty denies everything", true, ports(), "", nil, []uint16{3000, 4000}},
		{"port 0 is rejected", true, ports(4000, 0), "port 0", []uint16{3000}, []uint16{4000}},
		{"no relay and ports", false, ports(4000), "no proxy relay", nil, nil},
		{"no relay and empty ports", false, ports(), "", nil, nil},
		{"no relay and absent", false, nil, "", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := &liveState{
				logger:    slog.New(slog.DiscardHandler),
				hostPorts: map[uint16]func(){},
				setCA:     func(string) error { return nil },
			}
			if tt.relay {
				allowed, err := proxyRelayAllowlist([]uint16{3000})
				if err != nil {
					t.Fatal(err)
				}
				state.proxy = newProxyRelay(slog.New(slog.DiscardHandler), allowed, dialProxyTarget)
				t.Cleanup(state.proxy.Stop)
			}
			err := state.apply(Reload{ProxyPorts: tt.in})
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("apply: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("apply error = %v, want %q", err, tt.wantErr)
			}
			if state.proxy == nil {
				return
			}
			for _, p := range tt.wantAllow {
				if !state.proxy.portAllowed(p) {
					t.Errorf("port %d denied, want allowed", p)
				}
			}
			for _, p := range tt.wantDeny {
				if state.proxy.portAllowed(p) {
					t.Errorf("port %d allowed, want denied", p)
				}
			}
		})
	}
}

// TestLiveStateApplyProxyPortsAroundHostPorts checks that a reload moving a
// guest port between a proxy route and host_ports never lets the relay allow
// a port while its host port mirror changes: removed route ports are denied
// before the mirrors change, added ones are allowed only afterwards.
func TestLiveStateApplyProxyPortsAroundHostPorts(t *testing.T) {
	t.Parallel()
	allowed, err := proxyRelayAllowlist([]uint16{3000, 5000})
	if err != nil {
		t.Fatal(err)
	}
	relay := newProxyRelay(slog.New(slog.DiscardHandler), allowed, dialProxyTarget)
	t.Cleanup(relay.Stop)
	called := false
	state := &liveState{
		logger:    slog.New(slog.DiscardHandler),
		hostPorts: map[uint16]func(){},
		setCA:     func(string) error { return nil },
		proxy:     relay,
		setHosts: func(ports []uint16) {
			called = true
			if relay.portAllowed(3000) {
				t.Error("removed route port 3000 allowed while host ports change")
			}
			if relay.portAllowed(4000) {
				t.Error("added route port 4000 allowed while host ports change")
			}
			if !relay.portAllowed(5000) {
				t.Error("kept route port 5000 denied while host ports change")
			}
		},
	}
	// Port 3000 moves from a proxy route to host_ports, 4000 the other way.
	next := []uint16{4000, 5000}
	if err := state.apply(Reload{HostPorts: []uint16{3000}, ProxyPorts: &next}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !called {
		t.Fatal("host ports were not set")
	}
	for _, p := range []uint16{4000, 5000} {
		if !relay.portAllowed(p) {
			t.Errorf("port %d denied after reload, want allowed", p)
		}
	}
	if relay.portAllowed(3000) {
		t.Error("port 3000 allowed after reload, want denied")
	}
}
