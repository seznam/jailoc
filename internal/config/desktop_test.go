package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestDesktopTOML(t *testing.T) {
	t.Parallel()
	var cfg Config
	in := "[defaults]\nenable_desktop = true\n[defaults.proxy]\nauth = true\n[workspaces.w]\nenable_desktop = false\n[workspaces.w.proxy]\nauth = false\napp = 3000\nadmin = { port = 8080, auth = true }\n"
	if _, err := toml.Decode(in, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Defaults.EnableDesktop == nil || !*cfg.Defaults.EnableDesktop {
		t.Fatalf("Defaults.EnableDesktop = %v, want true", cfg.Defaults.EnableDesktop)
	}
	if cfg.Defaults.Proxy == nil || cfg.Defaults.Proxy.Auth == nil || !*cfg.Defaults.Proxy.Auth {
		t.Fatalf("Defaults.Proxy.Auth = %v, want true", cfg.Defaults.Proxy)
	}
	ws := cfg.Workspaces["w"]
	if ws.EnableDesktop == nil || *ws.EnableDesktop {
		t.Fatalf("Workspace.EnableDesktop = %v, want false", ws.EnableDesktop)
	}
	if ws.Proxy == nil || ws.Proxy.Auth == nil || *ws.Proxy.Auth {
		t.Fatalf("Workspace.Proxy.Auth = %v, want false", ws.Proxy)
	}
	if ws.Proxy.Routes["app"].Port != 3000 || ws.Proxy.Routes["app"].Auth != nil {
		t.Fatalf("app route = %+v", ws.Proxy.Routes["app"])
	}
	if ws.Proxy.Routes["admin"].Port != 8080 || ws.Proxy.Routes["admin"].Auth == nil || !*ws.Proxy.Routes["admin"].Auth {
		t.Fatalf("admin route = %+v", ws.Proxy.Routes["admin"])
	}
}

func TestDesktopValidation(t *testing.T) {
	ws := "[workspaces.w]\npaths = [\"/home/u/p\"]\n"
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "none", in: ws},
		{name: "desktop on", in: ws + "enable_desktop = true\n"},
		{name: "proxy valid", in: ws + "[workspaces.w.proxy]\napp = 3000\napi-2 = { port = 8080, auth = true }\n"},
		{name: "defaults desktop on, workspace off, host_ports 6080", in: "[defaults]\nenable_desktop = true\nhost_ports = [6080, 5901, 6081]\n" + ws + "enable_desktop = false\n"},
		{name: "proxy desktop reserved", in: ws + "[workspaces.w.proxy]\ndesktop = 3000\n", wantErr: "reserved"},
		{name: "proxy desktop reserved when off", in: ws + "enable_desktop = false\n[workspaces.w.proxy]\ndesktop = 3000\n", wantErr: "reserved"},
		{name: "proxy uppercase", in: ws + "[workspaces.w.proxy]\nApp = 3000\n", wantErr: "proxy"},
		{name: "proxy dotted name", in: ws + "[workspaces.w.proxy]\n\"a.b\" = 3000\n", wantErr: "proxy"},
		{name: "proxy edge hyphen", in: ws + "[workspaces.w.proxy]\n\"-a\" = 3000\n", wantErr: "proxy"},
		{name: "proxy trailing hyphen", in: ws + "[workspaces.w.proxy]\n\"a-\" = 3000\n", wantErr: "proxy"},
		{name: "proxy empty name", in: ws + "[workspaces.w.proxy]\n\"\" = 3000\n", wantErr: "proxy"},
		{name: "proxy long name", in: ws + "[workspaces.w.proxy]\n\"" + strings.Repeat("a", 64) + "\" = 3000\n", wantErr: "proxy"},
		{name: "proxy 63 name", in: ws + "[workspaces.w.proxy]\n\"" + strings.Repeat("a", 63) + "\" = 3000\n"},
		{name: "proxy port zero", in: ws + "[workspaces.w.proxy]\napp = 0\n", wantErr: "port"},
		{name: "proxy port high", in: ws + "[workspaces.w.proxy]\napp = 65536\n", wantErr: "port"},
		{name: "proxy relay port", in: ws + "[workspaces.w.proxy]\napp = 6081\n", wantErr: "6081"},
		{name: "proxy port in host_ports", in: ws + "host_ports = [\"3000:4000\"]\n[workspaces.w.proxy]\napp = 3000\n", wantErr: "host_ports"},
		{name: "proxy port in defaults host_ports", in: "[defaults]\nhost_ports = [3000]\n" + ws + "[workspaces.w.proxy]\napp = 3000\n", wantErr: "host_ports"},
		{name: "proxy host port only is fine", in: ws + "host_ports = [\"4000:3000\"]\n[workspaces.w.proxy]\napp = 3000\n"},
		{name: "proxy port in ports guest", in: ws + "ports = [\"8000:3000\"]\n[workspaces.w.proxy]\nweb = 3000\n", wantErr: "ports guest port"},
		{name: "proxy port in ports same host and guest", in: ws + "ports = [3000]\n[workspaces.w.proxy]\nweb = 3000\n", wantErr: "ports guest port"},
		{name: "proxy port in defaults ports inherited", in: "[defaults]\nports = [\"8000:3000\"]\n" + ws + "[workspaces.w.proxy]\nweb = 3000\n", wantErr: "ports guest port"},
		{name: "proxy port in ports of second route", in: ws + "ports = [\"8000:8080\"]\n[workspaces.w.proxy]\napp = 3000\napi = 8080\n", wantErr: "ports guest port"},
		{name: "proxy port ports override replaces defaults guest", in: "[defaults]\nports = [\"8000:3000\"]\n" + ws + "ports = [\"8000:9000\"]\n[workspaces.w.proxy]\nweb = 3000\n"},
		{name: "proxy port ports override sets inherited guest", in: "[defaults]\nports = [\"8000:9000\"]\n" + ws + "ports = [\"8000:3000\"]\n[workspaces.w.proxy]\nweb = 3000\n", wantErr: "ports guest port"},
		{name: "proxy port ports host side only is fine", in: ws + "ports = [\"3000:4000\"]\n[workspaces.w.proxy]\nweb = 3000\n"},
		{name: "proxy port ports other guest is fine", in: ws + "ports = [\"8000:4000\"]\n[workspaces.w.proxy]\nweb = 3000\n"},
		{name: "ports publish relay with proxy", in: ws + "ports = [\"8000:6081\"]\n[workspaces.w.proxy]\napp = 3000\n", wantErr: "6081"},
		{name: "ports publish relay with desktop", in: ws + "enable_desktop = true\nports = [\"8000:6081\"]\n", wantErr: "6081"},
		{name: "ports publish relay without proxy", in: ws + "ports = [\"8000:6081\"]\n"},
		{name: "ports publish desktop 6080", in: ws + "enable_desktop = true\nports = [6080]\n", wantErr: "6080"},
		{name: "ports publish vnc", in: ws + "enable_desktop = true\nports = [\"8000:5901\"]\n", wantErr: "5901"},
		{name: "ports host 6080 is fine", in: ws + "enable_desktop = true\nports = [\"6080:7000\"]\n"},
		{name: "ports 6080 without desktop", in: ws + "ports = [6080, 5901]\n"},
		{name: "defaults ports with desktop", in: "[defaults]\nenable_desktop = true\nports = [5901]\n" + ws, wantErr: "5901"},
		{name: "host_ports 6081 with proxy", in: ws + "host_ports = [6081]\n[workspaces.w.proxy]\napp = 3000\n", wantErr: "6081"},
		{name: "host_ports 6081 with desktop", in: ws + "enable_desktop = true\nhost_ports = [\"6081:7000\"]\n", wantErr: "6081"},
		{name: "host_ports 6081 inactive", in: ws + "host_ports = [6081]\n"},
		{name: "host_ports 6080 with desktop", in: ws + "enable_desktop = true\nhost_ports = [6080]\n", wantErr: "6080"},
		{name: "host_ports 5901 with desktop", in: ws + "enable_desktop = true\nhost_ports = [\"5901:/tmp/a.sock\"]\n", wantErr: "5901"},
		{name: "host_ports 6080 without desktop", in: ws + "host_ports = [6080, 5901]\n"},
		{name: "host_ports 6080 with proxy only", in: ws + "host_ports = [6080]\n[workspaces.w.proxy]\napp = 3000\n"},
		{name: "defaults host_ports 6080 with desktop override", in: "[defaults]\nhost_ports = [6080]\n" + ws + "enable_desktop = true\n", wantErr: "6080"},
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

func TestDesktopWorkspaceNameIsDNSLabel(t *testing.T) {
	tests := []struct {
		name    string
		ws      string
		extra   string
		wantErr bool
	}{
		{name: "edge hyphen without desktop or proxy", ws: "-a", extra: ""},
		{name: "edge hyphen with desktop", ws: "-a", extra: "enable_desktop = true\n", wantErr: true},
		{name: "trailing hyphen with proxy", ws: "a-", extra: "[workspaces.a-.proxy]\napp = 3000\n", wantErr: true},
		{name: "long name with desktop", ws: strings.Repeat("a", 64), extra: "enable_desktop = true\n", wantErr: true},
		{name: "63 name with desktop", ws: strings.Repeat("a", 63), extra: "enable_desktop = true\n"},
		{name: "valid with desktop", ws: "my-ws", extra: "enable_desktop = true\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			in := "[workspaces." + tt.ws + "]\npaths = [\"/home/u/p\"]\n" + tt.extra
			var cfg Config
			if _, err := toml.Decode(in, &cfg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			err := Validate(&cfg)
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "DNS label") {
				t.Fatalf("Validate error = %v, want DNS label", err)
			}
		})
	}
}

func TestDefaultConfigDocumentsDesktop(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"# enable_desktop = false",
		"# [workspaces.default.proxy]",
	} {
		if !strings.Contains(defaultConfigContent, want) {
			t.Errorf("default config lacks %q", want)
		}
	}
	if n := strings.Count(defaultConfigContent, "# enable_desktop = false"); n != 2 {
		t.Errorf("enable_desktop template lines = %d, want 2 (defaults and workspace)", n)
	}
}
