package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadUpstreamProxyTOML(t *testing.T) {
	home := "/data/home_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[defaults.network.upstream_proxy]
url = "http://proxy.corp.example:3128"
ca = "~/corp-ca.pem"
direct = ["*.internal.example", "10.0.0.0/8:443"]

[defaults.network.upstream_proxy.auth]
username = "svc"
from_file = "~/proxy-password"

[workspaces.api]
paths = ["/data/api"]

[workspaces.api.network.upstream_proxy]
command = "corp-proxy-url --json"

[workspaces.off]
paths = ["/data/off"]

[workspaces.off.network.upstream_proxy]
enabled = false
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	d := cfg.Defaults.Network.UpstreamProxy
	want := &UpstreamProxy{
		URL:    "http://proxy.corp.example:3128",
		CA:     home + "/corp-ca.pem",
		Direct: []string{"*.internal.example", "10.0.0.0/8:443"},
		Auth:   &UpstreamAuth{Username: "svc", FromFile: home + "/proxy-password"},
	}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("defaults upstream_proxy = %+v, want %+v", d, want)
	}
	if got := cfg.Workspaces["api"].Network.UpstreamProxy; got == nil || got.Command != "corp-proxy-url --json" || got.URL != "" {
		t.Fatalf("workspace upstream_proxy = %+v", got)
	}
	off := cfg.Workspaces["off"].Network.UpstreamProxy
	if off == nil || off.Enabled == nil || *off.Enabled {
		t.Fatalf("opt-out upstream_proxy = %+v", off)
	}
}

func TestUpstreamProxyExpandsWorkspacePaths(t *testing.T) {
	home := "/data/home_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Setenv("HOME", home)
	cfg := &Config{Workspaces: map[string]Workspace{"w": {
		Paths: []string{"/data/w"},
		Network: &Network{UpstreamProxy: &UpstreamProxy{
			URL:  "http://proxy.example",
			CA:   "~/ca.pem",
			Auth: &UpstreamAuth{Username: "u", FromFile: "~/pw"},
		}},
	}}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := cfg.Workspaces["w"].Network.UpstreamProxy
	if got.CA != home+"/ca.pem" || got.Auth.FromFile != home+"/pw" {
		t.Fatalf("paths not expanded: %+v %+v", got, got.Auth)
	}
}

func TestValidateUpstreamProxyInConfigScopes(t *testing.T) {
	t.Parallel()
	bad := &UpstreamProxy{URL: "ftp://proxy.example"}
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "defaults",
			cfg:  &Config{Defaults: Defaults{Network: &Network{UpstreamProxy: bad}}},
			want: "defaults: network.upstream_proxy",
		},
		{
			name: "workspace",
			cfg: &Config{Workspaces: map[string]Workspace{"w": {
				Paths: []string{"/data/w"}, Network: &Network{UpstreamProxy: bad},
			}}},
			want: `workspace "w": network.upstream_proxy`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestValidateUpstreamProxy(t *testing.T) {
	t.Parallel()
	long := "unix://" + "/" + strings.Repeat("a", 103) // path is 104 bytes
	fit := "unix://" + "/" + strings.Repeat("a", 102)  // path is 103 bytes
	tests := []struct {
		name    string
		p       *UpstreamProxy
		wantErr string
	}{
		{name: "nil", p: nil},
		{name: "http url", p: &UpstreamProxy{URL: "http://proxy.example:3128"}},
		{name: "http url default port", p: &UpstreamProxy{URL: "http://proxy.example"}},
		{name: "http url trailing slash", p: &UpstreamProxy{URL: "http://proxy.example:3128/"}},
		{name: "http ipv4", p: &UpstreamProxy{URL: "http://10.1.2.3:8080"}},
		{name: "http ipv6", p: &UpstreamProxy{URL: "http://[fd00::1]:8080"}},
		{name: "unix url", p: &UpstreamProxy{URL: "unix:///run/proxy.sock"}},
		{name: "unix url max length", p: &UpstreamProxy{URL: fit}},
		{name: "command", p: &UpstreamProxy{Command: "corp-proxy"}},
		{name: "disabled alone", p: &UpstreamProxy{Enabled: new(false)}},
		{name: "disabled with both", p: &UpstreamProxy{Enabled: new(false), URL: "http://a.example", Command: "x"}},
		{name: "explicit enabled", p: &UpstreamProxy{Enabled: new(true), URL: "http://proxy.example"}},
		{name: "ca absolute", p: &UpstreamProxy{URL: "http://proxy.example", CA: "/etc/ca.pem"}},
		{
			name: "auth sources",
			p:    &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromEnv: "PROXY_PASSWORD"}},
		},
		{
			name: "auth command ttl",
			p:    &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromCommand: "pass show proxy", TTL: "15m"}},
		},
		{
			name: "direct entries",
			p:    &UpstreamProxy{URL: "http://proxy.example", Direct: []string{"example.com", "*.example.com:8443", "10.0.0.0/8", "[fd00::1]:443"}},
		},

		{name: "neither url nor command", p: &UpstreamProxy{}, wantErr: `exactly one of "url" or "command"`},
		{name: "both url and command", p: &UpstreamProxy{URL: "http://proxy.example", Command: "x"}, wantErr: `exactly one of "url" or "command"`},
		{name: "blank command", p: &UpstreamProxy{Command: "  "}, wantErr: `"command" must not be blank`},
		{name: "https scheme", p: &UpstreamProxy{URL: "https://proxy.example"}, wantErr: "scheme"},
		{name: "socks scheme", p: &UpstreamProxy{URL: "socks5://proxy.example"}, wantErr: "scheme"},
		{name: "no scheme", p: &UpstreamProxy{URL: "proxy.example:3128"}, wantErr: "url"},
		{name: "userinfo", p: &UpstreamProxy{URL: "http://user:redacted@proxy.example"}, wantErr: "userinfo"}, //nolint:gosec // G101: tests rejection of URL userinfo
		{name: "empty userinfo", p: &UpstreamProxy{URL: "http://@proxy.example"}, wantErr: "userinfo"},
		{name: "userinfo with invalid port", p: &UpstreamProxy{URL: "http://user:marker-not-echoed@proxy.example:bad"}, wantErr: "invalid url"}, //nolint:gosec // G101: tests rejection without echoing
		{name: "query", p: &UpstreamProxy{URL: "http://proxy.example?a=b"}, wantErr: "query"},
		{name: "empty query", p: &UpstreamProxy{URL: "http://proxy.example/?"}, wantErr: "query"},
		{name: "fragment", p: &UpstreamProxy{URL: "http://proxy.example#x"}, wantErr: "fragment"},
		{name: "http path", p: &UpstreamProxy{URL: "http://proxy.example/pac"}, wantErr: "path"},
		{name: "http no host", p: &UpstreamProxy{URL: "http://"}, wantErr: "host"},
		{name: "http no host with port", p: &UpstreamProxy{URL: "http://:3128"}, wantErr: "host"},
		{name: "http port zero", p: &UpstreamProxy{URL: "http://proxy.example:0"}, wantErr: "port"},
		{name: "http port too large", p: &UpstreamProxy{URL: "http://proxy.example:65536"}, wantErr: "port"},
		{name: "http port not numeric", p: &UpstreamProxy{URL: "http://proxy.example:abc"}, wantErr: "url"},
		{name: "http bad host", p: &UpstreamProxy{URL: "http://bad_host!.example"}, wantErr: "host"},
		{name: "http wildcard host", p: &UpstreamProxy{URL: "http://*.example"}, wantErr: "host"},
		{name: "http ipv6 zone", p: &UpstreamProxy{URL: "http://[fe80::1%25eth0]:3128"}, wantErr: "host"},
		{name: "http whitespace", p: &UpstreamProxy{URL: "http://proxy.example :3128"}, wantErr: "url"},
		{name: "unix relative", p: &UpstreamProxy{URL: "unix://proxy.sock"}, wantErr: "host"},
		{name: "unix with host", p: &UpstreamProxy{URL: "unix://host/run/proxy.sock"}, wantErr: "host"},
		{name: "unix opaque", p: &UpstreamProxy{URL: "unix:run/proxy.sock"}, wantErr: "unix:///"},
		{name: "unix single slash", p: &UpstreamProxy{URL: "unix:/run/proxy.sock"}, wantErr: "unix:///"},
		{name: "unix empty path", p: &UpstreamProxy{URL: "unix:///"}, wantErr: "path"},
		{name: "unix too long", p: &UpstreamProxy{URL: long}, wantErr: "103"},
		{name: "unix query", p: &UpstreamProxy{URL: "unix:///run/p.sock?x=1"}, wantErr: "query"},
		{name: "unix fragment", p: &UpstreamProxy{URL: "unix:///run/p.sock#x"}, wantErr: "fragment"},
		{name: "unix userinfo", p: &UpstreamProxy{URL: "unix://u@/run/p.sock"}, wantErr: "userinfo"},
		{name: "unix NUL", p: &UpstreamProxy{URL: "unix:///run/p%00.sock"}, wantErr: "path"},
		{name: "ca relative", p: &UpstreamProxy{URL: "http://proxy.example", CA: "ca.pem"}, wantErr: "ca"},
		{
			name:    "auth with command",
			p:       &UpstreamProxy{Command: "x", Auth: &UpstreamAuth{Username: "u", FromEnv: "PW"}},
			wantErr: "auth",
		},
		{
			name:    "auth without url",
			p:       &UpstreamProxy{Auth: &UpstreamAuth{Username: "u", FromEnv: "PW"}},
			wantErr: "auth",
		},
		{
			name:    "auth empty username",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{FromEnv: "PW"}},
			wantErr: "username",
		},
		{
			name:    "auth blank username",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: " ", FromEnv: "PW"}},
			wantErr: "username",
		},
		{
			name:    "auth no source",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u"}},
			wantErr: "exactly one",
		},
		{
			name:    "auth two sources",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromEnv: "PW", FromFile: "/pw"}},
			wantErr: "exactly one",
		},
		{
			name:    "auth bad env name",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromEnv: "1PW"}},
			wantErr: "from_env",
		},
		{
			name:    "auth relative file",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromFile: "pw"}},
			wantErr: "absolute",
		},
		{
			name:    "auth ttl without command",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromEnv: "PW", TTL: "5m"}},
			wantErr: "ttl",
		},
		{
			name:    "auth bad ttl",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromCommand: "x", TTL: "soon"}},
			wantErr: "ttl",
		},
		{
			name:    "auth blank command",
			p:       &UpstreamProxy{URL: "http://proxy.example", Auth: &UpstreamAuth{Username: "u", FromCommand: " "}},
			wantErr: "from_command",
		},
		{name: "bad direct", p: &UpstreamProxy{URL: "http://proxy.example", Direct: []string{"a b"}}, wantErr: "direct"},
		{name: "direct port zero", p: &UpstreamProxy{URL: "http://proxy.example", Direct: []string{"a.example:0"}}, wantErr: "direct"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateUpstreamProxy(tt.p)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateUpstreamProxy: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateUpstreamProxy error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
	t.Run("error does not echo raw url credentials", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{
			"http://user:marker-not-echoed@proxy.example",
			"http://user:marker-not-echoed@proxy.example:bad",
		} {
			err := ValidateUpstreamProxy(&UpstreamProxy{URL: raw})
			if err == nil || strings.Contains(err.Error(), "marker-not-echoed") {
				t.Fatalf("ValidateUpstreamProxy(%q) error = %v, must reject without echoing credentials", raw, err)
			}
		}
	})
}

func TestParseUpstreamDirect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		target  string
		want    NetworkRule
		wantErr bool
	}{
		{name: "name", target: "example.com", want: NetworkRule{Allow: []string{"example.com"}, Proto: RuleProtoAny}},
		{name: "name with port", target: "example.com:8443", want: NetworkRule{Allow: []string{"example.com"}, Proto: RuleProtoAny, Ports: []RulePort{"8443"}}},
		{name: "wildcard name", target: "*.example.com", want: NetworkRule{Allow: []string{"*.example.com"}, Proto: RuleProtoAny}},
		{name: "wildcard name with port", target: "*.example.com:443", want: NetworkRule{Allow: []string{"*.example.com"}, Proto: RuleProtoAny, Ports: []RulePort{"443"}}},
		{name: "star", target: "*", want: NetworkRule{Allow: []string{"*"}, Proto: RuleProtoAny}},
		{name: "star with port", target: "*:443", want: NetworkRule{Allow: []string{"*"}, Proto: RuleProtoAny, Ports: []RulePort{"443"}}},
		{name: "ipv4", target: "10.1.2.3", want: NetworkRule{Allow: []string{"10.1.2.3"}, Proto: RuleProtoAny}},
		{name: "ipv4 with port", target: "10.1.2.3:22", want: NetworkRule{Allow: []string{"10.1.2.3"}, Proto: RuleProtoAny, Ports: []RulePort{"22"}}},
		{name: "cidr", target: "10.0.0.0/8", want: NetworkRule{Allow: []string{"10.0.0.0/8"}, Proto: RuleProtoAny}},
		{name: "cidr with port", target: "10.0.0.0/8:443", want: NetworkRule{Allow: []string{"10.0.0.0/8"}, Proto: RuleProtoAny, Ports: []RulePort{"443"}}},
		{name: "bare ipv6", target: "fd00::1", want: NetworkRule{Allow: []string{"fd00::1"}, Proto: RuleProtoAny}},
		{name: "bare ipv6 cidr", target: "fd00::/8", want: NetworkRule{Allow: []string{"fd00::/8"}, Proto: RuleProtoAny}},
		{name: "bracket ipv6", target: "[fd00::1]", want: NetworkRule{Allow: []string{"fd00::1"}, Proto: RuleProtoAny}},
		{name: "bracket ipv6 port", target: "[fd00::1]:443", want: NetworkRule{Allow: []string{"fd00::1"}, Proto: RuleProtoAny, Ports: []RulePort{"443"}}},
		{name: "bracket ipv6 cidr port", target: "[fd00::/8]:443", want: NetworkRule{Allow: []string{"fd00::/8"}, Proto: RuleProtoAny, Ports: []RulePort{"443"}}},
		{name: "trimmed", target: "  example.com:80 ", want: NetworkRule{Allow: []string{"example.com"}, Proto: RuleProtoAny, Ports: []RulePort{"80"}}},

		{name: "empty", target: "", wantErr: true},
		{name: "blank", target: "  ", wantErr: true},
		{name: "empty port", target: "example.com:", wantErr: true},
		{name: "port zero", target: "example.com:0", wantErr: true},
		{name: "port too large", target: "example.com:65536", wantErr: true},
		{name: "port range", target: "example.com:80-90", wantErr: true},
		{name: "port not numeric", target: "example.com:http", wantErr: true},
		{name: "two colons name", target: "example.com:1:2", wantErr: true},
		{name: "bad host", target: "bad host", wantErr: true},
		{name: "bad host with port", target: "bad_host:80", wantErr: true},
		{name: "bad wildcard", target: "a.*.example.com", wantErr: true},
		{name: "bad cidr", target: "10.0.0.0/33", wantErr: true},
		{name: "bracket name", target: "[example.com]:80", wantErr: true},
		{name: "bracket unclosed", target: "[fd00::1:443", wantErr: true},
		{name: "bracket trailing junk", target: "[fd00::1]x", wantErr: true},
		{name: "bracket empty port", target: "[fd00::1]:", wantErr: true},
		{name: "bracket bad port", target: "[fd00::1]:99999", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseUpstreamDirect(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseUpstreamDirect(%q) = %+v, want error", tt.target, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUpstreamDirect(%q): %v", tt.target, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseUpstreamDirect(%q) = %+v, want %+v", tt.target, got, tt.want)
			}
		})
	}
}

func TestUpstreamProxyEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    *UpstreamProxy
		want bool
	}{
		{name: "nil", p: nil, want: false},
		{name: "unset", p: &UpstreamProxy{URL: "http://a.example"}, want: true},
		{name: "true", p: &UpstreamProxy{Enabled: new(true)}, want: true},
		{name: "false", p: &UpstreamProxy{Enabled: new(false)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.p.IsEnabled(); got != tt.want {
				t.Fatalf("IsEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpstreamProxyClone(t *testing.T) {
	t.Parallel()
	if (*UpstreamProxy)(nil).Clone() != nil {
		t.Fatal("Clone of nil must be nil")
	}
	orig := &UpstreamProxy{
		Enabled: new(true),
		URL:     "http://a.example",
		Direct:  []string{"x.example"},
		Auth:    &UpstreamAuth{Username: "u", FromEnv: "PW"},
	}
	c := orig.Clone()
	if !reflect.DeepEqual(c, orig) {
		t.Fatalf("Clone = %+v, want %+v", c, orig)
	}
	*c.Enabled = false
	c.Direct[0] = "y.example"
	c.Auth.Username = "other"
	if !*orig.Enabled || orig.Direct[0] != "x.example" || orig.Auth.Username != "u" {
		t.Fatalf("Clone aliases the original: %+v", orig)
	}
}
func TestValidateRejectsWorkspaceShareOverlappingCustomConfig(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(cwd, ".test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "proj")
	cfgPath := filepath.Join(proj, "config.toml")
	writeFile(t, cfgPath, `[workspaces.api]
paths = [`+`"`+proj+`"`+`]
`)
	SetPath(cfgPath)
	t.Cleanup(func() { SetPath("") })

	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "holds the jailoc configuration") {
		t.Fatalf("Load() with workspace path containing custom config = %v, want forbidden error", err)
	}

	// Default ~/.config/jailoc/config.toml symlinked into a shared workspace
	// directory must also be rejected even when a separate custom --config
	// path outside the workspace is active.
	defPath := defaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(defPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cfgPath, defPath); err != nil {
		t.Fatal(err)
	}
	otherCfg := filepath.Join(home, "other", "config.toml")
	writeFile(t, otherCfg, `[workspaces.api]
paths = [`+`"`+proj+`"`+`]
`)
	SetPath(otherCfg)
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "holds the jailoc configuration") {
		t.Fatalf("Load() with custom config and default config symlinked into shared workspace path = %v, want forbidden error", err)
	}

	SetPath("")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "holds the jailoc configuration") {
		t.Fatalf("Load() with default config symlinked into shared workspace path = %v, want forbidden error", err)
	}

	// LoadFrom(path) without SetPath must also reject a workspace share overlapping path or its symlink target.
	_ = os.Remove(defPath)
	directProj := filepath.Join(home, "direct-proj")
	directCfg := filepath.Join(directProj, "config.toml")
	writeFile(t, directCfg, `[workspaces.api]
paths = [`+`"`+directProj+`"`+`]
`)
	_, err = LoadFrom(directCfg)
	if err == nil || !strings.Contains(err.Error(), "holds the jailoc configuration") {
		t.Fatalf("LoadFrom() with workspace path containing loaded config = %v, want forbidden error", err)
	}

	symlinkCfg := filepath.Join(home, "symlinked-config.toml")
	if err := os.Symlink(directCfg, symlinkCfg); err != nil {
		t.Fatal(err)
	}
	_, err = LoadFrom(symlinkCfg)
	if err == nil || !strings.Contains(err.Error(), "holds the jailoc configuration") {
		t.Fatalf("LoadFrom() with symlinked config targeting shared workspace path = %v, want forbidden error", err)
	}
}
