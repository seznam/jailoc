package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestParseHostPort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      HostPort
		want    HostPortTarget
		wantErr bool
	}{
		{in: "9222", want: HostPortTarget{Guest: 9222, HostPort: 9222}},
		{in: "8931:3845", want: HostPortTarget{Guest: 8931, HostPort: 3845}},
		{in: "7100:/tmp/figma.sock", want: HostPortTarget{Guest: 7100, Socket: "/tmp/figma.sock"}},
		{in: "7100:/tmp/../tmp/a.sock", want: HostPortTarget{Guest: 7100, Socket: "/tmp/a.sock"}},
		{in: "7100:rel.sock", wantErr: true},
		{in: "0", wantErr: true},
		{in: "65536", wantErr: true},
		{in: "80:", wantErr: true},
		{in: "a:80", wantErr: true},
		{in: "", wantErr: true},
		{in: "22", wantErr: true},
		{in: "53:5353", wantErr: true},
		{in: "2375", wantErr: true},
		{in: "7022:/tmp/a.sock", wantErr: true},
		{in: "7023:/tmp/a.sock", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			t.Parallel()
			got, err := ParseHostPort(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseHostPort(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("ParseHostPort(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseHostPortHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ParseHostPort("7100:~/mcp.sock")
	if err != nil {
		t.Fatalf("ParseHostPort: %v", err)
	}
	if want := filepath.Join(home, "mcp.sock"); got.Socket != want {
		t.Fatalf("Socket = %q, want %q", got.Socket, want)
	}
}

func TestHostPortDangerous(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   HostPortTarget
		want bool
	}{
		{in: HostPortTarget{Guest: 9222, HostPort: 9222}, want: true},
		{in: HostPortTarget{Guest: 9000, HostPort: 9222}, want: true},
		{in: HostPortTarget{Guest: 9222, HostPort: 9223}, want: false},
		{in: HostPortTarget{Guest: 7100, Socket: "/var/run/docker.sock"}, want: true},
		{in: HostPortTarget{Guest: 7100, Socket: "/run/containerd/containerd.sock"}, want: true},
		{in: HostPortTarget{Guest: 7100, Socket: "/tmp/figma.sock"}, want: false},
	}
	for _, tt := range tests {
		if got := tt.in.Dangerous() != ""; got != tt.want {
			t.Errorf("%+v.Dangerous() = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHostPortsTOML(t *testing.T) {
	t.Parallel()
	var ws Workspace
	if _, err := toml.Decode(`host_ports = [9222, "8931:3845", "7100:/tmp/a.sock"]`, &ws); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []HostPort{"9222", "8931:3845", "7100:/tmp/a.sock"}
	if !slices.Equal(ws.HostPorts, want) {
		t.Fatalf("HostPorts = %v, want %v", ws.HostPorts, want)
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(ws); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(b.String(), `host_ports = ["9222", "8931:3845", "7100:/tmp/a.sock"]`) {
		t.Fatalf("encoded = %s", b.String())
	}
	var bad Workspace
	if _, err := toml.Decode(`host_ports = [true]`, &bad); err == nil {
		t.Fatal("decode bool: want error")
	}
}

func TestMergeHostPorts(t *testing.T) {
	t.Parallel()
	got := MergeHostPorts([]HostPort{"9222", "8931:3845"}, []HostPort{"8931:4000", "7100:/tmp/a.sock"})
	want := []HostPortTarget{
		{Guest: 9222, HostPort: 9222},
		{Guest: 8931, HostPort: 4000},
		{Guest: 7100, Socket: "/tmp/a.sock"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("MergeHostPorts = %+v, want %+v", got, want)
	}
}

func TestHostPortsValidation(t *testing.T) {
	ws := "[workspaces.w]\nbackend = \"vm\"\npaths = [\"/home/u/p\"]\n"
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "valid", in: ws + "host_ports = [9222, \"7100:/tmp/a.sock\"]\n"},
		{name: "duplicate", in: ws + "host_ports = [9222, \"9222:9223\"]\n", wantErr: "listed twice"},
		{name: "reserved", in: ws + "host_ports = [22]\n", wantErr: "reserved"},
		{name: "defaults", in: "[defaults]\nhost_ports = [\"x\"]\n" + ws, wantErr: "defaults"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := os.MkdirAll(filepath.Join(home, "p"), 0o750); err != nil {
				t.Fatal(err)
			}
			var cfg Config
			if _, err := toml.Decode(tt.in, &cfg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			err := Validate(&cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
