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
