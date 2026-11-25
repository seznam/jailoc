package guest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestReloadProxyPortsJSON(t *testing.T) {
	t.Parallel()
	ports := []uint16{3000, 6080}
	empty := []uint16{}
	tests := []struct {
		name     string
		in       Reload
		wantJSON string // substring; empty means proxy_ports must be absent
		want     *[]uint16
	}{
		{"absent", Reload{Seq: 1}, "", nil},
		{"ports", Reload{Seq: 1, ProxyPorts: &ports}, `"proxy_ports":[3000,6080]`, &ports},
		{"empty list is not omitted", Reload{Seq: 1, ProxyPorts: &empty}, `"proxy_ports":[]`, &empty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantJSON == "" {
				if strings.Contains(string(b), "proxy_ports") {
					t.Fatalf("JSON %s has proxy_ports", b)
				}
			} else if !strings.Contains(string(b), tt.wantJSON) {
				t.Fatalf("JSON %s lacks %s", b, tt.wantJSON)
			}
			var got Reload
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if (got.ProxyPorts == nil) != (tt.want == nil) {
				t.Fatalf("ProxyPorts = %v, want %v", got.ProxyPorts, tt.want)
			}
			if tt.want != nil && !slices.Equal(*got.ProxyPorts, *tt.want) {
				t.Fatalf("ProxyPorts = %v, want %v", *got.ProxyPorts, *tt.want)
			}
		})
	}
}
