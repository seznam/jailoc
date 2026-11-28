package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestProxyTOML(t *testing.T) {
	t.Parallel()
	var cfg Config
	in := "[defaults.proxy]\nauth = true\n[workspaces.w.proxy]\nauth = false\napp = 3000\nadmin = { port = 8080, auth = true }\n"
	if _, err := toml.Decode(in, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Defaults.Proxy == nil || cfg.Defaults.Proxy.Auth == nil || !*cfg.Defaults.Proxy.Auth {
		t.Fatalf("Defaults.Proxy.Auth = %v, want true", cfg.Defaults.Proxy)
	}
	ws := cfg.Workspaces["w"]
	if ws.Proxy == nil || ws.Proxy.Auth == nil || *ws.Proxy.Auth {
		t.Fatalf("Workspace.Proxy.Auth = %v, want false", ws.Proxy)
	}
	if ws.Proxy.Routes["app"].Port != 3000 || ws.Proxy.Routes["app"].Auth != nil {
		t.Fatalf("app route = %+v", ws.Proxy.Routes["app"])
	}
	if ws.Proxy.Routes["admin"].Port != 8080 || ws.Proxy.Routes["admin"].Auth == nil || !*ws.Proxy.Routes["admin"].Auth {
		t.Fatalf("admin route = %+v", ws.Proxy.Routes["admin"])
	}
	encoded, err := Encode(&cfg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var roundTrip Config
	if _, err := toml.Decode(string(encoded), &roundTrip); err != nil {
		t.Fatalf("round-trip decode: %v\n%s", err, encoded)
	}
	rtWS := roundTrip.Workspaces["w"]
	if rtWS.Proxy == nil || rtWS.Proxy.Auth == nil || *rtWS.Proxy.Auth || rtWS.Proxy.Routes["app"].Port != 3000 || rtWS.Proxy.Routes["app"].Auth != nil || rtWS.Proxy.Routes["admin"].Port != 8080 || rtWS.Proxy.Routes["admin"].Auth == nil || !*rtWS.Proxy.Routes["admin"].Auth {
		t.Fatalf("round-trip proxy = %+v\n%s", rtWS.Proxy, encoded)
	}
}

func TestProxyValidation(t *testing.T) {
	ws := "[workspaces.w]\npaths = [\"/home/u/p\"]\n"
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "none", in: ws},
		{name: "proxy valid", in: ws + "[workspaces.w.proxy]\napp = 3000\napi-2 = { port = 8080, auth = true }\n"},
		{name: "proxy redirect defaults", in: "[defaults.proxy]\nredirect = false\n" + ws + "[workspaces.w.proxy]\nredirect = true\napp = { port = 3000, redirect = false }\n"},
		{name: "proxy redirect not boolean in defaults", in: "[defaults.proxy]\nredirect = 1\n" + ws, wantErr: "decode"},
		{name: "proxy route named redirect stays valid", in: ws + "[workspaces.w.proxy]\nredirect = 3000\n"},
		{name: "proxy redirect default and route named redirect duplicate key", in: ws + "[workspaces.w.proxy]\nredirect = true\n\"redirect\" = 3000\n", wantErr: "decode"},
		{name: "proxy route redirect not boolean", in: ws + "[workspaces.w.proxy]\napp = { port = 3000, redirect = \"no\" }\n", wantErr: "decode"},
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
		{name: "ports publish relay without proxy", in: ws + "ports = [\"8000:6081\"]\n", wantErr: "reserved for the proxy relay"},
		{name: "ports publish relay from defaults without proxy", in: "[defaults]\nports = [\"8000:6081\"]\n" + ws, wantErr: "reserved for the proxy relay"},
		{name: "ports host side 6081 without proxy is fine", in: ws + "ports = [\"6081:4000\"]\n"},
		{name: "host_ports 6081 with proxy", in: ws + "host_ports = [6081]\n[workspaces.w.proxy]\napp = 3000\n", wantErr: "6081"},
		{name: "host_ports 6081 without proxy", in: ws + "host_ports = [6081]\n", wantErr: "reserved for the proxy relay"},
		{name: "host_ports 6081 from defaults without proxy", in: "[defaults]\nhost_ports = [6081]\n" + ws, wantErr: "reserved for the proxy relay"},
		{name: "host_ports host side 6081 without proxy is fine", in: ws + "host_ports = [\"4000:6081\"]\n"},
		{name: "host_ports 6080 with proxy only", in: ws + "host_ports = [6080]\n[workspaces.w.proxy]\napp = 3000\n"},
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
				if tt.wantErr == "decode" {
					return
				}
				t.Fatalf("decode: %v", err)
			}
			err := Validate(&cfg)
			if tt.wantErr == "decode" {
				t.Fatal("decode succeeded, want a decode error")
			}
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

func TestProxyWorkspaceNameIsDNSLabel(t *testing.T) {
	tests := []struct {
		name    string
		ws      string
		extra   string
		wantErr bool
	}{
		{name: "edge hyphen without proxy", ws: "-a", extra: ""},
		{name: "edge hyphen with proxy", ws: "-a", extra: "[workspaces.-a.proxy]\napp = 3000\n", wantErr: true},
		{name: "trailing hyphen with proxy", ws: "a-", extra: "[workspaces.a-.proxy]\napp = 3000\n", wantErr: true},
		{name: "long name with proxy", ws: strings.Repeat("a", 64), extra: "[workspaces." + strings.Repeat("a", 64) + ".proxy]\napp = 3000\n", wantErr: true},
		{name: "63 name with proxy", ws: strings.Repeat("a", 63), extra: "[workspaces." + strings.Repeat("a", 63) + ".proxy]\napp = 3000\n"},
		{name: "valid with proxy", ws: "my-ws", extra: "[workspaces.my-ws.proxy]\napp = 3000\n"},
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

func TestDefaultConfigDocumentsProxy(t *testing.T) {
	t.Parallel()
	if !strings.Contains(defaultConfigContent, "# [workspaces.default.proxy]") {
		t.Error("default config lacks # [workspaces.default.proxy]")
	}
}

func TestProxyRedirectTOML(t *testing.T) {
	t.Parallel()
	var cfg Config
	in := "[defaults.proxy]\nredirect = false\n[workspaces.w.proxy]\nredirect = true\napp = 3000\nplain = { port = 8080, redirect = false }\nboth = { port = 9000, auth = true, redirect = true }\n"
	if _, err := toml.Decode(in, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Defaults.Proxy == nil || cfg.Defaults.Proxy.Redirect == nil || *cfg.Defaults.Proxy.Redirect {
		t.Fatalf("Defaults.Proxy.Redirect = %+v, want false", cfg.Defaults.Proxy)
	}
	ws := cfg.Workspaces["w"]
	if ws.Proxy == nil || ws.Proxy.Redirect == nil || !*ws.Proxy.Redirect {
		t.Fatalf("Workspace.Proxy.Redirect = %+v, want true", ws.Proxy)
	}
	if _, ok := ws.Proxy.Routes["redirect"]; ok {
		t.Fatal("boolean redirect decoded as a route")
	}
	if r := ws.Proxy.Routes["app"]; r.Port != 3000 || r.Redirect != nil {
		t.Fatalf("app route = %+v", r)
	}
	if r := ws.Proxy.Routes["plain"]; r.Port != 8080 || r.Redirect == nil || *r.Redirect {
		t.Fatalf("plain route = %+v", r)
	}
	encoded, err := Encode(&cfg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var rt Config
	if _, err := toml.Decode(string(encoded), &rt); err != nil {
		t.Fatalf("round-trip decode: %v\n%s", err, encoded)
	}
	rtWS := rt.Workspaces["w"]
	if rt.Defaults.Proxy == nil || rt.Defaults.Proxy.Redirect == nil || *rt.Defaults.Proxy.Redirect ||
		rtWS.Proxy == nil || rtWS.Proxy.Redirect == nil || !*rtWS.Proxy.Redirect ||
		rtWS.Proxy.Routes["plain"].Redirect == nil || *rtWS.Proxy.Routes["plain"].Redirect ||
		rtWS.Proxy.Routes["both"].Auth == nil || !*rtWS.Proxy.Routes["both"].Auth ||
		rtWS.Proxy.Routes["both"].Redirect == nil || !*rtWS.Proxy.Routes["both"].Redirect ||
		rtWS.Proxy.Routes["app"].Redirect != nil {
		t.Fatalf("round-trip proxy = %+v\n%s", rtWS.Proxy, encoded)
	}
}

func TestProxyRouteNamedRedirectRoundTrip(t *testing.T) {
	t.Parallel()
	var cfg Config
	if _, err := toml.Decode("[workspaces.w.proxy]\nredirect = 3000\n", &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ws := cfg.Workspaces["w"]
	if ws.Proxy.Redirect != nil || ws.Proxy.Routes["redirect"].Port != 3000 {
		t.Fatalf("proxy = %+v, want route redirect on 3000", ws.Proxy)
	}
	encoded, err := Encode(&cfg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var rt Config
	if _, err := toml.Decode(string(encoded), &rt); err != nil {
		t.Fatalf("round-trip decode: %v\n%s", err, encoded)
	}
	if got := rt.Workspaces["w"].Proxy; got.Redirect != nil || got.Routes["redirect"].Port != 3000 {
		t.Fatalf("round-trip proxy = %+v\n%s", got, encoded)
	}
}

func TestProxyPorts(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    ProxyPorts
		wantErr string
	}{
		{name: "absent uses defaults", in: "", want: ProxyPorts{HTTP: 80, HTTPS: 443, StandaloneHTTP: 7080, StandaloneHTTPS: 7043}},
		{name: "empty table uses defaults", in: "[proxy]\n", want: ProxyPorts{HTTP: 80, HTTPS: 443, StandaloneHTTP: 7080, StandaloneHTTPS: 7043}},
		{
			name: "all set",
			in:   "[proxy]\nhttp_port = 8080\nhttps_port = 8443\nstandalone_http_port = 9080\nstandalone_https_port = 9443\n",
			want: ProxyPorts{HTTP: 8080, HTTPS: 8443, StandaloneHTTP: 9080, StandaloneHTTPS: 9443},
		},
		{name: "partly set", in: "[proxy]\nhttps_port = 8443\n", want: ProxyPorts{HTTP: 80, HTTPS: 8443, StandaloneHTTP: 7080, StandaloneHTTPS: 7043}},
		{name: "min and max", in: "[proxy]\nhttp_port = 1\nhttps_port = 65535\n", want: ProxyPorts{HTTP: 1, HTTPS: 65535, StandaloneHTTP: 7080, StandaloneHTTPS: 7043}},
		{name: "normal pair may reuse standalone ports", in: "[proxy]\nhttp_port = 7080\nhttps_port = 7043\n", want: ProxyPorts{HTTP: 7080, HTTPS: 7043, StandaloneHTTP: 7080, StandaloneHTTPS: 7043}},
		{name: "http_port zero", in: "[proxy]\nhttp_port = 0\n", wantErr: "http_port 0 out of range"},
		{name: "https_port high", in: "[proxy]\nhttps_port = 65536\n", wantErr: "https_port 65536 out of range"},
		{name: "standalone_http_port negative", in: "[proxy]\nstandalone_http_port = -1\n", wantErr: "standalone_http_port -1 out of range"},
		{name: "standalone_https_port zero", in: "[proxy]\nstandalone_https_port = 0\n", wantErr: "standalone_https_port 0 out of range"},
		{name: "pair equal explicit", in: "[proxy]\nhttp_port = 9000\nhttps_port = 9000\n", wantErr: "http_port and https_port must differ"},
		{name: "http_port equals default https", in: "[proxy]\nhttp_port = 443\n", wantErr: "http_port and https_port must differ"},
		{name: "https_port equals default http", in: "[proxy]\nhttps_port = 80\n", wantErr: "http_port and https_port must differ"},
		{name: "standalone pair equal", in: "[proxy]\nstandalone_http_port = 9000\nstandalone_https_port = 9000\n", wantErr: "standalone_http_port and standalone_https_port must differ"},
		{name: "standalone http equals default standalone https", in: "[proxy]\nstandalone_http_port = 7043\n", wantErr: "standalone_http_port and standalone_https_port must differ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var cfg Config
			if _, err := toml.Decode(tt.in, &cfg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			err := Validate(&cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Validate error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := cfg.Proxy.Ports(); got != tt.want {
				t.Fatalf("Ports() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestProxyPortsRoundTrip(t *testing.T) {
	t.Parallel()
	var cfg Config
	in := "[proxy]\nhttp_port = 8080\nstandalone_https_port = 9443\n"
	if _, err := toml.Decode(in, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := Encode(&cfg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var rt Config
	if _, err := toml.Decode(string(encoded), &rt); err != nil {
		t.Fatalf("round-trip decode: %v\n%s", err, encoded)
	}
	want := ProxyPorts{HTTP: 8080, HTTPS: 443, StandaloneHTTP: 7080, StandaloneHTTPS: 9443}
	if got := rt.Proxy.Ports(); got != want {
		t.Fatalf("round-trip Ports() = %+v, want %+v\n%s", got, want, encoded)
	}
	var zero Config
	zeroEncoded, err := Encode(&zero)
	if err != nil {
		t.Fatalf("Encode zero: %v", err)
	}
	if strings.Contains(string(zeroEncoded), "[proxy]") {
		t.Fatalf("zero config encodes an empty [proxy] table:\n%s", zeroEncoded)
	}
}

func TestProxyRedirectDefaultConflictsWithRouteName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := Config{Workspaces: map[string]Workspace{"w": {
		Paths: []string{"/home/u/p"},
		Proxy: &ProxyTable{Redirect: new(true), Routes: map[string]ProxyRoute{"redirect": {Port: 3000}}},
	}}}
	err := Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "conflicts with the proxy redirect default") {
		t.Fatalf("Validate error = %v, want redirect conflict", err)
	}
}
