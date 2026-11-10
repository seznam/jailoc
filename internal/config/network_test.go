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
				{Name: "dev-deps", Allow: []string{"@dev"}},
				{Name: "no-paste", Deny: []string{"pastebin.com", "gist.github.com", "*.ngrok.io"}},
				{Name: "db", Allow: []string{"10.20.5.10"}, Proto: "tcp", Ports: []RulePort{"5432"}},
				{Name: "internal-dev", Allow: []string{"10.20.0.0/16", "fd42::/48"}, Ports: []RulePort{"9090", "8000-8100"}},
				{Name: "ntp", Allow: []string{"time.example.com"}, Proto: "udp", Ports: []RulePort{"123"}},
				{Name: "internet", Allow: []string{"*"}, Proto: "any"},
			},
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
			name:      "unknown list",
			rules:     []NetworkRule{{Name: "bad-list", Allow: []string{"@unknown"}}},
			wantError: `unknown list "@unknown"`,
		},
		{
			name:      "empty list name",
			rules:     []NetworkRule{{Allow: []string{"@"}}},
			wantError: "empty @list name",
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

func TestLoadNetworkRulesTOML(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[defaults.network]
dns = "9.9.9.9"

[[defaults.network.rules]]
name = "dev-deps"
allow = ["@dev"]

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
	if cfg.Defaults.Network.DNS != "9.9.9.9" || len(cfg.Defaults.Network.Rules) != 2 {
		t.Fatalf("defaults network = %+v", cfg.Defaults.Network)
	}
	r := cfg.Workspaces["api"].Network.Rules
	if len(r) != 1 || r[0].Name != "internal-dev" || !slices.Equal(r[0].Ports, []RulePort{"9090", "8000-8100"}) {
		t.Fatalf("workspace rules = %+v", r)
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
