package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestParsePort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      Port
		want    PortForward
		wantErr bool
	}{
		{in: "3000", want: PortForward{Host: 3000, Guest: 3000}},
		{in: "8080:80", want: PortForward{Host: 8080, Guest: 80}},
		{in: "0", wantErr: true},
		{in: "65536", wantErr: true},
		{in: "80:", wantErr: true},
		{in: "a:80", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			t.Parallel()
			got, err := ParsePort(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePort(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("ParsePort(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestPortsTOML(t *testing.T) {
	t.Parallel()
	var w Workspace
	if _, err := toml.Decode(`ports = [3000, "8080:80"]`, &w); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(w.Ports, []Port{"3000", "8080:80"}) {
		t.Fatalf("ports = %v", w.Ports)
	}
	var bad Workspace
	if _, err := toml.Decode(`ports = [true]`, &bad); err == nil {
		t.Fatal("decode of a boolean port succeeded")
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(w); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(b.String(), `ports = ["3000", "8080:80"]`) {
		t.Fatalf("encoded = %s", b.String())
	}
}

func TestMergePorts(t *testing.T) {
	t.Parallel()
	got := MergePorts([]Port{"3000", "8080:80"}, []Port{"8080:8080", "5432"})
	want := []PortForward{{3000, 3000}, {8080, 8080}, {5432, 5432}}
	if !slices.Equal(got, want) {
		t.Fatalf("MergePorts = %v, want %v", got, want)
	}
}

func TestPortsValidation(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{name: "vm ports", toml: "[workspaces.w]\nbackend = \"vm\"\npaths = [\"/home/u/p\"]\nports = [3000, \"8080:80\"]\n"},
		{name: "docker rejects", toml: "[workspaces.w]\npaths = [\"/home/u/p\"]\nports = [3000]\n", wantErr: "ports is only supported"},
		{name: "duplicate host port", toml: "[workspaces.w]\nbackend = \"vm\"\npaths = [\"/home/u/p\"]\nports = [3000, \"3000:80\"]\n", wantErr: "listed twice"},
		{name: "invalid", toml: "[defaults]\nports = [\"99999\"]\n", wantErr: "invalid port"},
		{name: "docker with protect_git", toml: "[workspaces.w]\nbackend = \"vm\"\npaths = [\"/home/u/p\"]\nprotect_git = true\nenable_docker = true\n", wantErr: "cannot be combined with protect_git"},
		{name: "docker in vm", toml: "[workspaces.w]\nbackend = \"vm\"\npaths = [\"/home/u/p\"]\nenable_docker = true\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			var cfg Config
			if _, err := toml.Decode(tt.toml, &cfg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(home, "p"), 0o750); err != nil {
				t.Fatal(err)
			}
			err := Validate(&cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Validate error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
