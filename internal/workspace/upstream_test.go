package workspace_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func upstreamCfg(defaults *config.UpstreamProxy, ws *config.UpstreamProxy) *config.Config {
	cfg := &config.Config{Workspaces: map[string]config.Workspace{"w": {Paths: []string{"/data/w"}}}}
	if defaults != nil {
		cfg.Defaults.Network = &config.Network{UpstreamProxy: defaults}
	}
	if ws != nil {
		w := cfg.Workspaces["w"]
		w.Network = &config.Network{UpstreamProxy: ws}
		cfg.Workspaces["w"] = w
	}
	return cfg
}

func TestResolveUpstreamProxy(t *testing.T) {
	t.Parallel()
	defaults := &config.UpstreamProxy{
		URL:    "http://default.example:3128",
		Direct: []string{"a.example"},
		Auth:   &config.UpstreamAuth{Username: "d", FromEnv: "PW"},
	}
	tests := []struct {
		name     string
		defaults *config.UpstreamProxy
		ws       *config.UpstreamProxy
		want     *config.UpstreamProxy
	}{
		{name: "none", want: nil},
		{name: "inherits defaults", defaults: defaults, want: defaults},
		{
			name:     "workspace replaces the whole table",
			defaults: defaults,
			ws:       &config.UpstreamProxy{Command: "pick-proxy"},
			want:     &config.UpstreamProxy{Command: "pick-proxy"},
		},
		{
			name: "workspace without defaults",
			ws:   &config.UpstreamProxy{URL: "unix:///run/p.sock"},
			want: &config.UpstreamProxy{URL: "unix:///run/p.sock"},
		},
		{name: "workspace opt-out", defaults: defaults, ws: &config.UpstreamProxy{Enabled: new(false)}, want: nil},
		{name: "opt-out without defaults", ws: &config.UpstreamProxy{Enabled: new(false)}, want: nil},
		{name: "defaults opt-out", defaults: &config.UpstreamProxy{Enabled: new(false), URL: "http://x.example"}, want: nil},
		{
			name:     "workspace overrides disabled defaults",
			defaults: &config.UpstreamProxy{Enabled: new(false)},
			ws:       &config.UpstreamProxy{URL: "http://w.example"},
			want:     &config.UpstreamProxy{URL: "http://w.example"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := workspace.Resolve(upstreamCfg(tt.defaults, tt.ws), "w")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !reflect.DeepEqual(r.UpstreamProxy, tt.want) {
				t.Fatalf("UpstreamProxy = %+v, want %+v", r.UpstreamProxy, tt.want)
			}
		})
	}
}

func TestResolveUpstreamProxyDoesNotAliasConfig(t *testing.T) {
	t.Parallel()
	cfg := upstreamCfg(&config.UpstreamProxy{
		Enabled: new(true),
		URL:     "http://default.example",
		Direct:  []string{"a.example"},
		Auth:    &config.UpstreamAuth{Username: "d", FromEnv: "PW"},
	}, nil)
	r, err := workspace.Resolve(cfg, "w")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r.UpstreamProxy.Direct[0] = "changed"
	r.UpstreamProxy.Auth.Username = "changed"
	*r.UpstreamProxy.Enabled = false
	got := cfg.Defaults.Network.UpstreamProxy
	if got.Direct[0] != "a.example" || got.Auth.Username != "d" || !*got.Enabled {
		t.Fatalf("resolved upstream proxy aliases the config: %+v", got)
	}
}

func TestBuilderUpstreamProxy(t *testing.T) {
	t.Parallel()
	static := &config.UpstreamProxy{
		URL:    "http://default.example:3128",
		CA:     "/etc/ca.pem",
		Direct: []string{"a.example"},
		Auth:   &config.UpstreamAuth{Username: "d", FromFile: "/etc/pw"},
	}
	tests := []struct {
		name string
		cfg  *config.Config
		want *config.UpstreamProxy
	}{
		{name: "nil config", cfg: nil},
		{name: "no defaults", cfg: upstreamCfg(nil, nil)},
		{name: "static defaults", cfg: upstreamCfg(static, nil), want: static},
		{name: "workspace table is ignored", cfg: upstreamCfg(static, &config.UpstreamProxy{URL: "http://w.example"}), want: static},
		{name: "command defaults", cfg: upstreamCfg(&config.UpstreamProxy{Command: "pick-proxy"}, nil)},
		{name: "disabled defaults", cfg: upstreamCfg(&config.UpstreamProxy{Enabled: new(false), URL: "http://x.example"}, nil)},
		{name: "workspace-only", cfg: upstreamCfg(nil, &config.UpstreamProxy{URL: "http://w.example"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := workspace.BuilderUpstreamProxy(tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("BuilderUpstreamProxy = %+v, want %+v", got, tt.want)
			}
		})
	}

	t.Run("clone", func(t *testing.T) {
		t.Parallel()
		cfg := upstreamCfg(&config.UpstreamProxy{URL: "http://x.example", Direct: []string{"a.example"}, Auth: &config.UpstreamAuth{Username: "u", FromEnv: "PW"}}, nil)
		got := workspace.BuilderUpstreamProxy(cfg)
		got.Direct[0] = "changed"
		got.Auth.Username = "changed"
		src := cfg.Defaults.Network.UpstreamProxy
		if src.Direct[0] != "a.example" || src.Auth.Username != "u" {
			t.Fatalf("builder proxy aliases the config: %+v", src)
		}
	})
}

func TestResolveUpstreamDirectWarnings(t *testing.T) {
	t.Parallel()
	rule := func(proto string, ports []config.RulePort, allow ...string) config.NetworkRule {
		return config.NetworkRule{Allow: allow, Proto: proto, Ports: ports}
	}
	tests := []struct {
		name   string
		rules  []config.NetworkRule
		direct []string
		want   []string // substrings, one per expected warning
	}{
		{name: "no direct", rules: []config.NetworkRule{rule("", nil, "a.example")}},
		{name: "exact name allowed", rules: []config.NetworkRule{rule("", nil, "a.example")}, direct: []string{"a.example"}},
		{name: "wildcard allows subdomain", rules: []config.NetworkRule{rule("", nil, "*.example.com")}, direct: []string{"a.example.com"}},
		{name: "allow star", rules: []config.NetworkRule{rule("", nil, "*")}, direct: []string{"a.example", "10.0.0.1:22"}},
		{name: "name vs cidr is unknowable", rules: []config.NetworkRule{rule("", nil, "10.0.0.0/8")}, direct: []string{"a.example"}},
		{name: "cidr vs name is unknowable", rules: []config.NetworkRule{rule("", nil, "a.example")}, direct: []string{"10.0.0.1"}},
		{name: "direct cidr contains allowed address", rules: []config.NetworkRule{rule("", nil, "10.1.2.3")}, direct: []string{"10.0.0.0/8"}},
		{name: "direct wildcard overlaps allowed name", rules: []config.NetworkRule{rule("", nil, "a.example.com")}, direct: []string{"*.example.com"}},
		{name: "tcp rule", rules: []config.NetworkRule{rule("tcp", nil, "a.example")}, direct: []string{"a.example"}},
		{name: "port in rule", rules: []config.NetworkRule{rule("", []config.RulePort{"8000-9000"}, "a.example")}, direct: []string{"a.example:8443"}},
		{name: "direct any port with port rule", rules: []config.NetworkRule{rule("", []config.RulePort{"443"}, "a.example")}, direct: []string{"a.example"}},
		{
			name:   "unrelated name",
			rules:  []config.NetworkRule{rule("", nil, "a.example")},
			direct: []string{"b.example"},
			want:   []string{`"b.example"`},
		},
		{
			name:   "exact allow does not cover subdomain direct",
			rules:  []config.NetworkRule{rule("", nil, "example.com")},
			direct: []string{"sub.example.com"},
			want:   []string{`"sub.example.com"`},
		},
		{
			name:   "wildcard allow does not cover apex direct",
			rules:  []config.NetworkRule{rule("", nil, "*.example.com")},
			direct: []string{"example.com"},
			want:   []string{`"example.com"`},
		},
		{
			name:   "no rules at all",
			direct: []string{"a.example"},
			want:   []string{`"a.example"`},
		},
		{
			name:   "disjoint cidr",
			rules:  []config.NetworkRule{rule("", nil, "10.0.0.0/8")},
			direct: []string{"192.168.0.0/16"},
			want:   []string{`"192.168.0.0/16"`},
		},
		{
			name:   "wrong port",
			rules:  []config.NetworkRule{rule("", []config.RulePort{"443"}, "a.example")},
			direct: []string{"a.example:22"},
			want:   []string{`"a.example:22"`},
		},
		{
			name:   "udp rule matches direct entry",
			rules:  []config.NetworkRule{rule("udp", nil, "a.example")},
			direct: []string{"a.example"},
		},
		{
			name:   "deny rule is not an allow",
			rules:  []config.NetworkRule{{Deny: []string{"a.example"}}},
			direct: []string{"a.example"},
			want:   []string{`"a.example"`},
		},
		{
			name:   "only unreachable entries warn, in order",
			rules:  []config.NetworkRule{rule("", nil, "a.example")},
			direct: []string{"z.example", "a.example", "y.example"},
			want:   []string{`"z.example"`, `"y.example"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Workspaces: map[string]config.Workspace{"w": {
					Paths: []string{"/data/w"},
					Network: &config.Network{
						Rules:         tt.rules,
						UpstreamProxy: &config.UpstreamProxy{URL: "http://p.example", Direct: tt.direct},
					},
				}},
			}
			r, err := workspace.Resolve(cfg, "w")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			got := r.UpstreamDirectWarnings
			if len(got) != len(tt.want) {
				t.Fatalf("warnings = %q, want %d containing %q", got, len(tt.want), tt.want)
			}
			for i, sub := range tt.want {
				if !strings.Contains(got[i], sub) || !strings.Contains(got[i], "direct") {
					t.Errorf("warning %d = %q, want it to mention direct and %s", i, got[i], sub)
				}
			}
		})
	}
}

func TestResolveUpstreamDirectWarningsOnlyForActiveProxy(t *testing.T) {
	t.Parallel()
	for name, p := range map[string]*config.UpstreamProxy{
		"disabled": {Enabled: new(false), Direct: []string{"nowhere.example"}},
		"none":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, err := workspace.Resolve(upstreamCfg(nil, p), "w")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(r.UpstreamDirectWarnings) != 0 {
				t.Fatalf("warnings = %q, want none", r.UpstreamDirectWarnings)
			}
		})
	}
}

func TestResolveUpstreamDirectWarningsSeeAgentRule(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Defaults: config.Defaults{Agent: "claude", Network: &config.Network{UpstreamProxy: &config.UpstreamProxy{
			URL: "http://p.example", Direct: []string{"api.anthropic.com:443"},
		}}},
		Workspaces: map[string]config.Workspace{"w": {Paths: []string{"/data/w"}}},
	}
	r, err := workspace.Resolve(cfg, "w")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(r.UpstreamDirectWarnings) != 0 {
		t.Fatalf("agent endpoint warned: %q", r.UpstreamDirectWarnings)
	}
}
