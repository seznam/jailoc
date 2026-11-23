package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNetworkRulesValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rules     []NetworkRule
		dns       string
		wantError string
	}{
		{
			name: "valid allow and deny rules",
			dns:  "9.9.9.9",
			rules: []NetworkRule{
				{Name: "dev-deps", Allow: []string{"github.com", "registry.npmjs.org", "*.githubusercontent.com"}},
				{Name: "no-paste", Deny: []string{"pastebin.com", "gist.github.com", "*.ngrok.io"}},
				{Name: "db", Allow: []string{"10.20.5.10"}, Proto: "tcp", Ports: []RulePort{"5432"}},
				{Name: "internal-dev", Allow: []string{"10.20.0.0/16", "fd42::/48"}, Ports: []RulePort{"9090", "8000-8100"}},
				{Name: "ntp", Allow: []string{"time.example.com"}, Proto: "udp", Ports: []RulePort{"123"}},
				{Name: "internet", Allow: []string{"*"}, Proto: "any"},
				{Name: "gh-ro", Allow: []string{"api.github.com"}, HTTP: &HTTPMatch{Methods: []string{"GET", "head", "*", "M-SEARCH"}, Paths: []string{"/", "/repos/seznam/*", "/repos/**", "/a/*/b", "/dir/", "/**"}}},
				{Name: "gh-deny", Deny: []string{"*"}, Proto: "tcp", HTTP: &HTTPMatch{Methods: []string{"DELETE"}}},
				{Name: "any-http", Allow: []string{"10.0.0.0/8"}, HTTP: &HTTPMatch{}},
			},
		},
		{
			name:      "http with udp",
			rules:     []NetworkRule{{Name: "u", Allow: []string{"example.com"}, Proto: "udp", HTTP: &HTTPMatch{}}},
			wantError: `network rule "u": "http" requires proto "tcp"`,
		},
		{
			name:      "http with any proto",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, Proto: "any", HTTP: &HTTPMatch{}}},
			wantError: `"http" requires proto "tcp"`,
		},
		{
			name:      "http empty method",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Methods: []string{""}}}},
			wantError: "empty HTTP method",
		},
		{
			name:      "http bad method token",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Methods: []string{"GET POST"}}}},
			wantError: `invalid HTTP method "GET POST"`,
		},
		{
			name:      "http bad method comma",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Methods: []string{"GET,POST"}}}},
			wantError: `invalid HTTP method "GET,POST"`,
		},
		{
			name:      "http path without slash",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"repos"}}}},
			wantError: `must start with "/"`,
		},
		{
			name:      "http path empty",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{""}}}},
			wantError: `must start with "/"`,
		},
		{
			name:      "http path partial wildcard",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/foo*"}}}},
			wantError: "wildcards must be a whole segment",
		},
		{
			name:      "http path double star in middle",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a/**/b"}}}},
			wantError: `"**" is only allowed as the last segment`,
		},
		{
			name:      "http path triple star",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a/***"}}}},
			wantError: "wildcards must be a whole segment",
		},
		{
			name:      "http path empty middle segment",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a//b"}}}},
			wantError: "empty segment",
		},
		{
			name:      "http path dot segment",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a/./b"}}}},
			wantError: "segments are not allowed",
		},
		{
			name:      "http path dotdot segment",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a/.."}}}},
			wantError: "segments are not allowed",
		},
		{
			name:      "http path query",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a?x=1"}}}},
			wantError: "is not allowed",
		},
		{
			name:      "http path fragment",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a#b"}}}},
			wantError: "is not allowed",
		},
		{
			name:      "http path percent",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a%2fb"}}}},
			wantError: "is not allowed",
		},
		{
			name:      "http path backslash",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{`/a\b`}}}},
			wantError: "is not allowed",
		},
		{
			name:      "http path control char",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, HTTP: &HTTPMatch{Paths: []string{"/a\x01"}}}},
			wantError: "is not allowed",
		},
		{
			name:      "neither allow nor deny",
			rules:     []NetworkRule{{Name: "empty"}},
			wantError: `network rule "empty": exactly one of "allow" or "deny"`,
		},
		{
			name:      "both allow and deny",
			rules:     []NetworkRule{{Name: "both", Allow: []string{"a.example"}, Deny: []string{"b.example"}}},
			wantError: `network rule "both": cannot set both "allow" and "deny"`,
		},
		{
			name:      "empty allow slice",
			rules:     []NetworkRule{{Allow: []string{}}},
			wantError: "network.rules[0]: exactly one",
		},
		{
			name:      "bad cidr",
			rules:     []NetworkRule{{Allow: []string{"10.0.0.0/33"}}},
			wantError: `invalid CIDR "10.0.0.0/33"`,
		},
		{
			name:      "bad wildcard middle",
			rules:     []NetworkRule{{Allow: []string{"foo.*.example.com"}}},
			wantError: `invalid hostname "foo.*.example.com"`,
		},
		{
			name:      "bad wildcard double",
			rules:     []NetworkRule{{Allow: []string{"*.*.example.com"}}},
			wantError: `invalid wildcard hostname "*.*.example.com"`,
		},
		{
			name:      "bad hostname label",
			rules:     []NetworkRule{{Allow: []string{"-bad.example.com"}}},
			wantError: `invalid hostname "-bad.example.com"`,
		},
		{
			name:      "bad proto",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, Proto: "icmp"}},
			wantError: `invalid proto "icmp"`,
		},
		{
			name:      "bad port zero",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, Ports: []RulePort{"0"}}},
			wantError: `invalid port "0"`,
		},
		{
			name:      "bad port reversed range",
			rules:     []NetworkRule{{Allow: []string{"example.com"}, Ports: []RulePort{"9000-8000"}}},
			wantError: `invalid port "9000-8000"`,
		},
		{
			name:      "bad dns scalar",
			dns:       "127.0.0.1",
			wantError: "dns_upstream must be a routable IPv4 address",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Workspaces: map[string]Workspace{
					"api": {
						Paths:   []string{"/data/api"},
						Network: &Network{DNS: tc.dns, Rules: tc.rules},
					},
				},
			}
			err := Validate(cfg)
			if tc.wantError == "" && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("Validate error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestInternalZonesValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		zone      string
		wantError string
	}{
		{zone: "internal.example.com"},
		{zone: "Corp.Example."},
		{zone: "*.corp.example"},
		{zone: "internal"},
		{zone: "lan"},
		{zone: "home.arpa"},
		{zone: "myapp.github.io"},
		{zone: "seznam.cz"},
		{zone: "*", wantError: "must be a domain name"},
		{zone: "", wantError: "must be a domain name"},
		{zone: "a.*.example.com", wantError: "must be a domain name"},
		{zone: "-bad.example.com", wantError: "invalid domain label"},
		{zone: "com", wantError: "is a public suffix"},
		{zone: "co.uk", wantError: "is a public suffix"},
		{zone: "github.io", wantError: "is a public suffix"},
		{zone: "arpa", wantError: "is a public suffix"},
		{zone: "nip.io", wantError: "wildcard DNS service"},
		{zone: "*.sslip.io", wantError: "wildcard DNS service"},
		{zone: "dev.localtest.me", wantError: "wildcard DNS service"},
	}
	for _, tc := range tests {
		t.Run(tc.zone, func(t *testing.T) {
			t.Parallel()
			for _, cfg := range []*Config{
				{Defaults: Defaults{Network: &Network{InternalZones: []string{tc.zone}}}},
				{Workspaces: map[string]Workspace{"api": {Paths: []string{"/data/api"}, Network: &Network{InternalZones: []string{tc.zone}}}}},
			} {
				err := Validate(cfg)
				if tc.wantError == "" && err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
					t.Fatalf("Validate error = %v, want %q", err, tc.wantError)
				}
			}
		})
	}
}

func TestValidateAgentEndpoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		endpoint  string
		wantError string
	}{
		{endpoint: "api.anthropic.com"},
		{endpoint: "*.githubcopilot.com"},
		{endpoint: "API.OpenAI.com."},
		{endpoint: "*", wantError: "must be a host name"},
		{endpoint: "10.0.0.0/8", wantError: "must be a host name"},
		{endpoint: "1.2.3.4", wantError: "must be a host name"},
		{endpoint: "", wantError: "empty rule target"},
		{endpoint: "a.*.example.com", wantError: "invalid hostname"},
	}
	for _, tc := range tests {
		t.Run(tc.endpoint, func(t *testing.T) {
			t.Parallel()
			for _, cfg := range []*Config{
				{Defaults: Defaults{Network: &Network{AgentEndpoints: []string{tc.endpoint}}}},
				{Workspaces: map[string]Workspace{"api": {Paths: []string{"/data/api"}, Network: &Network{AgentEndpoints: []string{tc.endpoint}}}}},
			} {
				err := Validate(cfg)
				if tc.wantError == "" && err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError) || !strings.Contains(err.Error(), "agent_endpoints")) {
					t.Fatalf("Validate error = %v, want %q", err, tc.wantError)
				}
			}
		})
	}
}

func TestLoadNetworkRulesTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[defaults.network]
dns = "9.9.9.9"
internal_zones = ["internal.example.com"]

[[defaults.network.rules]]
name = "dev-deps"
allow = ["github.com", "registry.npmjs.org"]

[[defaults.network.rules]]
name = "no-paste"
deny = ["pastebin.com", "*.ngrok.io"]

[workspaces.api]
paths = ["/data/api"]

[[workspaces.api.network.rules]]
name = "internal-dev"
allow = ["10.20.0.0/16"]
ports = [9090, "8000-8100"]
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Defaults.Network.DNS != "9.9.9.9" || len(cfg.Defaults.Network.Rules) != 2 || !slices.Equal(cfg.Defaults.Network.InternalZones, []string{"internal.example.com"}) {
		t.Fatalf("defaults network = %+v", cfg.Defaults.Network)
	}
	r := cfg.Workspaces["api"].Network.Rules
	if len(r) != 1 || r[0].Name != "internal-dev" || !slices.Equal(r[0].Ports, []RulePort{"9090", "8000-8100"}) {
		t.Fatalf("workspace rules = %+v", r)
	}
}

func TestLoadNetworkRulesHTTPTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[workspaces.api]
paths = ["/data/api"]

[[workspaces.api.network.rules]]
name = "github-readonly"
allow = ["api.github.com"]
http.methods = ["GET", "HEAD"]
http.paths = ["/repos/seznam/*"]

[[workspaces.api.network.rules]]
name = "any-request"
allow = ["example.com"]
http = {}

[[workspaces.api.network.rules]]
name = "plain"
allow = ["plain.example.com"]
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if u := cfg.UnknownKeys(); len(u) != 0 {
		t.Fatalf("unknown keys = %v", u)
	}
	r := cfg.Workspaces["api"].Network.Rules
	if len(r) != 3 {
		t.Fatalf("rules = %+v", r)
	}
	if r[0].HTTP == nil || !slices.Equal(r[0].HTTP.Methods, []string{"GET", "HEAD"}) || !slices.Equal(r[0].HTTP.Paths, []string{"/repos/seznam/*"}) {
		t.Fatalf("rule 0 http = %+v", r[0].HTTP)
	}
	if r[1].HTTP == nil || len(r[1].HTTP.Methods) != 0 || len(r[1].HTTP.Paths) != 0 {
		t.Fatalf("rule 1 http = %+v, want empty non-nil table", r[1].HTTP)
	}
	if r[2].HTTP != nil {
		t.Fatalf("rule 2 http = %+v, want nil", r[2].HTTP)
	}
}

func TestDefaultConfigDocumentsHTTPRule(t *testing.T) {
	t.Parallel()
	_, after, ok := strings.Cut(defaultConfigContent, "# name = \"github-readonly\"\n")
	if !ok {
		t.Fatal("defaultConfigContent does not document an http rule")
	}
	// Uncommenting the example must give a valid rule.
	body := "[[defaults.network.rules]]\nname = \"github-readonly\"\n"
	for _, line := range strings.Split(after, "\n") {
		rest, ok := strings.CutPrefix(line, "# ")
		if !ok {
			break
		}
		body += rest + "\n"
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, body)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v\n%s", err, body)
	}
	r := cfg.Defaults.Network.Rules
	if len(r) != 1 || r[0].HTTP == nil || len(r[0].HTTP.Methods) == 0 || len(r[0].HTTP.Paths) == 0 {
		t.Fatalf("rules = %+v", r)
	}
}

func TestNormalizeHTTPMethod(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "get", want: "GET"},
		{in: "GET", want: "GET"},
		{in: "*", want: "*"},
		{in: "m-search", want: "M-SEARCH"},
		{in: "", wantErr: true},
		{in: "GET ", wantErr: true},
		{in: "a/b", wantErr: true},
		{in: "é", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeHTTPMethod(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("NormalizeHTTPMethod(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestDeprecationWarnings(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Defaults: Defaults{
			AllowedHosts:    []string{"a.example"},
			AllowedNetworks: []string{"10.0.0.0/8"},
			Network: &Network{
				FilteredDNS:     new(true),
				DNSUpstream:     "1.1.1.1",
				DNSBlockedZones: []string{"bad.example"},
			},
		},
	}
	warnings := DeprecationWarnings(cfg)
	if len(warnings) != 5 {
		t.Fatalf("warnings = %v, want 5 warnings", warnings)
	}
}

func TestParseRuleTargetMappedIPv4(t *testing.T) {
	t.Parallel()
	kind, norm, err := ParseRuleTarget("::ffff:10.20.5.10")
	if err != nil || kind != "cidr" || norm != "10.20.5.10/32" {
		t.Fatalf("ParseRuleTarget(::ffff:10.20.5.10) = %q, %q, %v; want cidr, 10.20.5.10/32, nil", kind, norm, err)
	}
}

func TestLoadNetworkRuleLogTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[workspaces.api]
paths = ["/data/api"]

[[workspaces.api.network.rules]]
name = "quiet-allow"
allow = ["registry.example.com"]
log = false

[[workspaces.api.network.rules]]
name = "quiet-deny-http"
deny = ["telemetry.example.com"]
http.methods = ["POST"]
log = false

[[workspaces.api.network.rules]]
name = "explicit-log"
allow = ["a.example.com"]
log = true

[[workspaces.api.network.rules]]
name = "default-log"
allow = ["b.example.com"]
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if u := cfg.UnknownKeys(); len(u) != 0 {
		t.Fatalf("unknown keys = %v", u)
	}
	want := []struct {
		name  string
		set   bool
		quiet bool
	}{
		{"quiet-allow", true, true},
		{"quiet-deny-http", true, true},
		{"explicit-log", true, false},
		{"default-log", false, false},
	}
	rules := cfg.Workspaces["api"].Network.Rules
	if len(rules) != len(want) {
		t.Fatalf("rules = %+v", rules)
	}
	for i, w := range want {
		r := rules[i]
		if r.Name != w.name || (r.Log != nil) != w.set || r.Quiet() != w.quiet {
			t.Errorf("rule %d = %+v (quiet %v), want %+v", i, r, r.Quiet(), w)
		}
	}
}

func TestLoadNetworkRuleLogInvalidType(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[workspaces.api]
paths = ["/data/api"]

[[workspaces.api.network.rules]]
allow = ["example.com"]
log = "no"
`)
	if _, err := LoadFrom(path); err == nil {
		t.Fatal("LoadFrom accepted a string log value")
	}
}

func TestDeprecationWarningsAgentEndpoints(t *testing.T) {
	t.Parallel()
	const want = "no network.agent_endpoints configured"
	for _, tt := range []struct {
		name string
		cfg  *Config
		warn bool
	}{
		{name: "no workspaces", cfg: &Config{}},
		{name: "unset", cfg: &Config{Workspaces: map[string]Workspace{"api": {}}}, warn: true},
		{name: "single-provider agent", cfg: &Config{Workspaces: map[string]Workspace{"api": {Agent: "claude"}}}},
		{name: "custom agent from omo", cfg: &Config{
			Defaults:   Defaults{Agents: map[string]AgentSettings{"mine": {From: "omo"}}},
			Workspaces: map[string]Workspace{"api": {Agent: "mine"}},
		}, warn: true},
		{name: "defaults", cfg: &Config{
			Defaults:   Defaults{Network: &Network{AgentEndpoints: []string{"api.anthropic.com"}}},
			Workspaces: map[string]Workspace{"api": {}},
		}},
		{name: "workspace empty list", cfg: &Config{
			Workspaces: map[string]Workspace{"api": {Network: &Network{AgentEndpoints: []string{}}}},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := slices.ContainsFunc(DeprecationWarnings(tt.cfg), func(w string) bool { return strings.Contains(w, want) })
			if got != tt.warn {
				t.Fatalf("warning %q present = %v, want %v", want, got, tt.warn)
			}
		})
	}
}
