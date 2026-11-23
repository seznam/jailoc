//go:build darwin || linux

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
)

func scripted(answers ...string) (*prompter, *bytes.Buffer) {
	out := &bytes.Buffer{}
	in := strings.NewReader(strings.Join(answers, "\n") + "\n")
	return &prompter{in: bufio.NewReader(in), out: out, resolver: &fakeResolver{}}, out
}

func migrationFixture(t *testing.T) (string, string, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "npmrc")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Mode:     "remote",
		Defaults: config.Defaults{SSHAuthSock: true},
		Workspaces: map[string]config.Workspace{
			"api": {Backend: config.BackendDocker, Paths: []string{"/src/api"}, ExposePort: new(true), EnableDocker: new(true),
				Mounts: []string{file + ":/home/agent/.npmrc:ro", data + ":/data:rw", ":/home/agent/.opencode"}},
			"web": {Backend: config.BackendVM, Paths: []string{"/src/web"}},
		},
	}
	return file, data, cfg
}

func TestPlanMigration(t *testing.T) {
	t.Parallel()
	file, data, cfg := migrationFixture(t)
	// defaults: remove incompatible; api: remove incompatible, npmrc -> snapshot (default),
	// data -> invalid answer, then "snapshot".
	p, out := scripted("", "y", "", "maybe", "s")
	m, err := planMigration(cfg, p, os.Stat)
	if err != nil {
		t.Fatalf("planMigration: %v\n%s", err, out)
	}
	if !slices.Equal(m.workspaces, []string{"api"}) {
		t.Fatalf("workspaces = %v", m.workspaces)
	}
	if cfg.Defaults.Backend != config.BackendVM || cfg.Mode != "" || !cfg.Defaults.SSHAuthSock {
		t.Fatalf("defaults not migrated: backend=%q mode=%q ssh=%v", cfg.Defaults.Backend, cfg.Mode, cfg.Defaults.SSHAuthSock)
	}
	api := cfg.Workspaces["api"]
	if api.Backend != "" || api.ExposePort != nil || api.EnableDocker == nil {
		t.Fatalf("api not migrated: backend=%q expose_port=%v enable_docker=%v", api.Backend, api.ExposePort, api.EnableDocker)
	}
	if !slices.Equal(api.Mounts, []string{":/home/agent/.opencode"}) {
		t.Fatalf("api mounts = %v", api.Mounts)
	}
	if want := []string{file + ":/home/agent/.npmrc", data + ":/data"}; !slices.Equal(api.Snapshots, want) {
		t.Fatalf("api snapshots = %v, want %v", api.Snapshots, want)
	}
	if cfg.Workspaces["web"].Backend != "" {
		t.Fatal("explicit vm backend override not removed")
	}
	if !strings.Contains(out.String(), "answer one of") || !strings.Contains(out.String(), "workspace writes no longer reach the host") {
		t.Fatalf("prompt output missing hints:\n%s", out)
	}
}

func TestPlanMigrationDefaults(t *testing.T) {
	t.Parallel()
	file, data, cfg := migrationFixture(t)
	p, _ := scripted()
	p.yes = true
	if _, err := planMigration(cfg, p, os.Stat); err != nil {
		t.Fatalf("planMigration: %v", err)
	}
	api := cfg.Workspaces["api"]
	if want := []string{data + ":/data:rw", ":/home/agent/.opencode"}; !slices.Equal(api.Mounts, want) {
		t.Fatalf("mounts = %v, want %v", api.Mounts, want)
	}
	if want := []string{file + ":/home/agent/.npmrc"}; !slices.Equal(api.Snapshots, want) {
		t.Fatalf("snapshots = %v, want %v", api.Snapshots, want)
	}
}

func TestPlanMigrationDeclined(t *testing.T) {
	t.Parallel()
	_, _, cfg := migrationFixture(t)
	p, _ := scripted("n")
	if _, err := planMigration(cfg, p, os.Stat); err == nil || !strings.Contains(err.Error(), "needs these settings removed") {
		t.Fatalf("planMigration error = %v", err)
	}
}

func TestRunMigrateWritesBackup(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# my comment\n[workspaces.web]\nbackend = \"vm\"\npaths = [\"/src/web\"]\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	p, out := scripted("y")
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	backup, err := os.ReadFile(path + ".pre-vm-migration") //nolint:gosec // G304: test file
	if err != nil || string(backup) != original {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	cfg, err := config.ReadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Backend != config.BackendVM || cfg.Workspaces["web"].Backend != "" || len(cfg.Defaults.Network.Get().Rules) != 1 || !slices.Equal(cfg.Defaults.Network.Rules[0].Allow, []string{"*"}) {
		t.Fatalf("config not migrated: %+v", cfg.Defaults)
	}

	p, out = scripted()
	if err := runMigrate(context.Background(), path, p); err != nil || !strings.Contains(out.String(), "nothing to migrate") {
		t.Fatalf("second run = %v\n%s", err, out)
	}

	// Unknown keys alone trigger a rewrite that drops them.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("pathz = [\"/typo\"]\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	p, out = scripted("y")
	if err := runMigrate(context.Background(), path, p); err != nil || !strings.Contains(out.String(), "remove unknown setting") {
		t.Fatalf("unknown-key run = %v\n%s", err, out)
	}
	if cfg, err = config.ReadRaw(path); err != nil || len(cfg.UnknownKeys()) != 0 {
		t.Fatalf("reread after migrate: err %v, cfg %+v", err, cfg)
	}
}

func TestRunMigrateMovesNetwork(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `[defaults]
backend = "vm"
allowed_hosts = ["context7.com", "context7.com"]
allowed_networks = ["10.0.0.0/8"]
filtered_dns = true
dns_upstream = "10.20.30.53"
dns_blocked_zones = ["corp.example.com."]

[workspaces.web]
paths = ["/src/web"]
allowed_hosts = ["gitlab.example.com", "gitlab.example.com", "app.nip.io", "10.1.2.3"]
allowed_networks = ["192.168.1.0/24"]
filtered_dns = false
dns_upstream = "10.20.30.54"
dns_blocked_zones = ["old.example.com"]

[workspaces.web.network]
dns_upstream = "10.20.30.55"
dns_blocked_zones = []

[workspaces.api]
paths = ["/src/api"]

[workspaces.api.network]
filtered_dns = true
dns_upstream = "10.20.30.56"
dns_blocked_zones = ["internal.example.com"]

[workspaces.inherited]
paths = ["/src/inherited"]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadRaw(path); err != nil {
		t.Fatal(err)
	}
	p, out := scripted("", "y")
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	for _, want := range []string{
		"[defaults]: remove filtered_dns (DNS filtering is always on)",
		"[defaults]: move dns_upstream to [defaults.network] dns",
		"[defaults]: move allowed_hosts to [[defaults.network.rules]]",
		"[defaults]: move allowed_networks to [[defaults.network.rules]]",
		"[defaults]: move dns_blocked_zones to inheriting workspaces",
		"[workspaces.web]: remove filtered_dns (DNS filtering is always on)",
		"[workspaces.web.network]: move dns_upstream to dns",
		"[workspaces.web]: remove dns_upstream (set in [workspaces.web.network])",
		"[workspaces.web]: move allowed_hosts to [[workspaces.web.network.rules]]",
		"[defaults]: add allowed_hosts names to [defaults.network] internal_zones (context7.com)",
		"[workspaces.web]: add allowed_hosts names to [workspaces.web.network] internal_zones (gitlab.example.com)",
		"[workspaces.web]: app.nip.io cannot be internal zones; their names no longer resolve to private addresses",
		"[workspaces.web]: move allowed_networks to [[workspaces.web.network.rules]]",
		"[workspaces.web.network]: remove dns_blocked_zones",
		"[workspaces.web]: remove dns_blocked_zones (set in [workspaces.web.network])",
		"[workspaces.api.network]: remove filtered_dns (DNS filtering is always on)",
		"[workspaces.api.network]: move dns_upstream to dns",
		"[workspaces.api.network]: move dns_blocked_zones to [[workspaces.api.network.rules]]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output misses %q:\n%s", want, out)
		}
	}
	cfg, err := config.ReadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if warnings := config.DeprecationWarnings(cfg); len(warnings) > 0 {
		t.Fatalf("DeprecationWarnings after migrate = %v, want none", warnings)
	}
	d, web, api, inherited := cfg.Defaults, cfg.Workspaces["web"], cfg.Workspaces["api"], cfg.Workspaces["inherited"]
	if d.Network.DNS != "10.20.30.53" || d.Network.DNSUpstream != "" || d.Network.FilteredDNS != nil || d.Network.DNSBlockedZones != nil {
		t.Fatalf("defaults network = %+v", d.Network)
	}
	wantDefaultRules := []config.NetworkRule{
		{Name: "internet", Allow: []string{"*"}, Proto: config.RuleProtoAny},
		{Name: "allowed-hosts", Allow: []string{"context7.com"}, Proto: config.RuleProtoAny},
		{Name: "allowed-networks", Allow: []string{"10.0.0.0/8"}, Proto: config.RuleProtoAny},
	}
	if len(d.Network.Rules) != len(wantDefaultRules) {
		t.Fatalf("defaults rules = %+v, want %+v", d.Network.Rules, wantDefaultRules)
	}
	for i, want := range wantDefaultRules {
		got := d.Network.Rules[i]
		if got.Name != want.Name || got.Proto != want.Proto || !slices.Equal(got.Allow, want.Allow) || !slices.Equal(got.Deny, want.Deny) {
			t.Fatalf("defaults rules[%d] = %+v, want %+v", i, got, want)
		}
	}
	if web.Network.DNS != "10.20.30.55" || web.Network.DNSUpstream != "" || web.Network.FilteredDNS != nil || web.Network.DNSBlockedZones != nil {
		t.Fatalf("web network = %+v", web.Network)
	}
	if !slices.Equal(d.Network.InternalZones, []string{"context7.com"}) || !slices.Equal(web.Network.InternalZones, []string{"gitlab.example.com"}) {
		t.Fatalf("internal zones = %v (defaults), %v (web)", d.Network.InternalZones, web.Network.InternalZones)
	}
	if len(web.Network.Rules) != 2 || !slices.Equal(web.Network.Rules[0].Allow, []string{"gitlab.example.com", "app.nip.io", "10.1.2.3"}) || !slices.Equal(web.Network.Rules[1].Allow, []string{"192.168.1.0/24"}) {
		t.Fatalf("web rules = %+v", web.Network.Rules)
	}
	if api.Network.DNS != "10.20.30.56" || len(api.Network.Rules) != 1 || !slices.Equal(api.Network.Rules[0].Deny, []string{"internal.example.com", "*.internal.example.com"}) {
		t.Fatalf("api network = %+v", api.Network)
	}
	if inherited.Network == nil || len(inherited.Network.Rules) != 1 || !slices.Equal(inherited.Network.Rules[0].Deny, []string{"corp.example.com", "*.corp.example.com"}) {
		t.Fatalf("inherited network = %+v, want default blocked-zones deny rule", inherited.Network)
	}
	if err := config.ValidateCopy(cfg); err != nil {
		t.Fatalf("migrated config invalid: %v", err)
	}

	p, out = scripted()
	if err := runMigrate(context.Background(), path, p); err != nil || !strings.Contains(out.String(), "nothing to migrate") {
		t.Fatalf("second run = %v\n%s", err, out)
	}
}

func TestPlanMigrationNetworkOnly(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		answer     string
		workspaces []string
	}{
		{"current configuration", "", nil},
		{"jailoc v1 configuration", "y", []string{"web"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			cfg := &config.Config{
				Defaults: config.Defaults{LegacyFilteredDNS: new(true), Mounts: []string{data + "/defaults:/defaults"}},
				Workspaces: map[string]config.Workspace{
					"web": {Paths: []string{"/src/web"}, Mounts: []string{data + ":/data"}},
				},
			}
			p, out := scripted(tt.answer, "", "")
			m, err := planMigration(cfg, p, os.Stat)
			if err != nil {
				t.Fatalf("planMigration: %v\n%s", err, out)
			}
			if !slices.Equal(m.workspaces, tt.workspaces) {
				t.Fatalf("workspaces = %v, want %v\n%s", m.workspaces, tt.workspaces, out)
			}
			if !strings.Contains(out.String(), "jailoc v1 (Docker) configuration") {
				t.Fatalf("no v1 question:\n%s", out)
			}
			web := cfg.Workspaces["web"]
			if tt.workspaces == nil && (!slices.Equal(web.Mounts, []string{data + ":/data"}) ||
				!slices.Equal(cfg.Defaults.Mounts, []string{data + "/defaults:/defaults"}) || cfg.Defaults.Snapshots != nil ||
				strings.Contains(out.String(), data)) {
				t.Fatalf("mounts of a current configuration touched: %v %+v\n%s", web.Mounts, cfg.Defaults, out)
			}
			if cfg.Defaults.Network.FilteredDNS != nil || cfg.Defaults.LegacyFilteredDNS != nil {
				t.Fatalf("filtered_dns not removed: %+v", cfg.Defaults)
			}
		})
	}
}

func TestRunMigrateKeepsZoneOnlyNetworkTable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `[defaults]
backend = "vm"

[defaults.network]
internal_zones = ["corp.example"]

[[defaults.network.rules]]
name = "internet"
allow = ["*"]

[workspaces.web]
paths = ["/src/web"]
allowed_networks = ["10.1.0.0/16"]

[workspaces.api]
paths = ["/src/api"]

[workspaces.api.network]
internal_zones = ["api.corp.example"]
filtered_dns = true
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	p, out := scripted("y")
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	cfg, err := config.ReadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Defaults.Network.Get().InternalZones, []string{"corp.example"}) {
		t.Fatalf("defaults internal_zones = %v, want [corp.example]", cfg.Defaults.Network.Get().InternalZones)
	}
	if got := cfg.Workspaces["api"].Network.Get().InternalZones; !slices.Equal(got, []string{"api.corp.example"}) {
		t.Fatalf("api internal_zones = %v, want [api.corp.example]\n%s", got, out)
	}
}

func TestRunMigrateWarnsAboutLegacyAliases(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `[defaults]
backend = "vm"
allowed_hosts = ["db.corp.example"]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	p, out := scripted("y")
	p.resolver = &fakeResolver{
		ips:    map[string]string{"db.corp.example": "10.0.0.5"},
		cnames: map[string]string{"db.corp.example": "db-node.corp.example."},
	}
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	want := `warning: [defaults]: allowed host "db.corp.example" is an alias of "db-node.corp.example", outside internal_zones`
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output misses %q:\n%s", want, out)
	}
}

const internalZonesPrompt = "Internal zones"

func vmDefaultsWithRules(allow, deny []string, zones []string) config.Defaults {
	return config.Defaults{
		Backend: config.BackendVM,
		Network: &config.Network{
			InternalZones: zones,
			Rules:         []config.NetworkRule{{Name: "r", Allow: allow, Deny: deny, Proto: config.RuleProtoAny}},
		},
	}
}

func TestPlanMigrationAsksInternalZones(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		cfg  *config.Config
		asks bool
	}{
		{"defaults hostname", &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, nil)}, true},
		{"defaults wildcard hostname", &config.Config{Defaults: vmDefaultsWithRules([]string{"*.corp.example"}, nil, nil)}, true},
		{"empty zones list", &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, []string{})}, true},
		{"workspace hostname", &config.Config{
			Defaults: config.Defaults{Backend: config.BackendVM},
			Workspaces: map[string]config.Workspace{"web": {Network: &config.Network{
				Rules: []config.NetworkRule{{Name: "r", Allow: []string{"git.corp.example"}, Proto: config.RuleProtoAny}},
			}}},
		}, true},
		{"defaults zones", &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, []string{"corp.example"})}, false},
		{"workspace zones", &config.Config{
			Defaults:   vmDefaultsWithRules([]string{"git.corp.example"}, nil, nil),
			Workspaces: map[string]config.Workspace{"web": {Network: &config.Network{InternalZones: []string{"corp.example"}}}},
		}, false},
		{"migrated allowed_hosts zones", &config.Config{Defaults: config.Defaults{Backend: config.BackendVM, AllowedHosts: []string{"git.corp.example"}}}, false},
		{"wildcard allow", &config.Config{Defaults: vmDefaultsWithRules([]string{"*"}, nil, nil)}, false},
		{"ip and cidr allow", &config.Config{Defaults: vmDefaultsWithRules([]string{"10.1.2.3", "10.0.0.0/8"}, nil, nil)}, false},
		{"hostname deny only", &config.Config{Defaults: vmDefaultsWithRules(nil, []string{"git.corp.example"}, nil)}, false},
		{"no rules", &config.Config{Defaults: config.Defaults{Backend: config.BackendVM}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, out := scripted("")
			if _, err := planMigration(tt.cfg, p, os.Stat); err != nil {
				t.Fatalf("planMigration: %v\n%s", err, out)
			}
			if got := strings.Contains(out.String(), internalZonesPrompt); got != tt.asks {
				t.Fatalf("asked = %v, want %v\n%s", got, tt.asks, out)
			}
		})
	}
}

func TestPlanMigrationInternalZonesExplanation(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, nil)}
	p, out := scripted("")
	if _, err := planMigration(cfg, p, os.Stat); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"zones you control",
		"subdomains",
		"private addresses",
		"rebinding",
		"A zone alone grants no access",
		"[defaults.network]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("explanation misses %q:\n%s", want, out)
		}
	}
}

func TestPlanMigrationInternalZonesSkipped(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		yes  bool
		in   []string
	}{
		{"blank answer", false, []string{""}},
		{"whitespace answer", false, []string{" , "}},
		{"yes flag", true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, nil)}
			p, out := scripted(tt.in...)
			p.yes = tt.yes
			m, err := planMigration(cfg, p, os.Stat)
			if err != nil {
				t.Fatalf("planMigration: %v\n%s", err, out)
			}
			if got := cfg.Defaults.Network.InternalZones; len(got) != 0 {
				t.Fatalf("internal_zones = %v, want none", got)
			}
			if want := []string{`[defaults.network]: add agent_endpoints of agent "opencode"`}; !slices.Equal(m.changes, want) {
				t.Fatalf("changes = %v, want %v", m.changes, want)
			}
			if tt.yes && strings.Contains(out.String(), internalZonesPrompt) {
				t.Fatalf("--yes asked:\n%s", out)
			}
		})
	}
}

func TestPlanMigrationInternalZonesInput(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, nil)}
	p, out := scripted("Corp.Example., *.lab.example\tcorp.example,db.example")
	m, err := planMigration(cfg, p, os.Stat)
	if err != nil {
		t.Fatalf("planMigration: %v\n%s", err, out)
	}
	if want := []string{"corp.example", "lab.example", "db.example"}; !slices.Equal(cfg.Defaults.Network.InternalZones, want) {
		t.Fatalf("internal_zones = %v, want %v", cfg.Defaults.Network.InternalZones, want)
	}
	if want := "[defaults.network]: add internal_zones (corp.example, lab.example, db.example)"; !slices.Contains(m.changes, want) {
		t.Fatalf("changes = %v, want %q", m.changes, want)
	}
}

func TestPlanMigrationInternalZonesCreatesNetworkTable(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Defaults: config.Defaults{Backend: config.BackendVM},
		Workspaces: map[string]config.Workspace{"web": {Network: &config.Network{
			Rules: []config.NetworkRule{{Name: "r", Allow: []string{"git.corp.example"}, Proto: config.RuleProtoAny}},
		}}},
	}
	p, out := scripted("corp.example")
	if _, err := planMigration(cfg, p, os.Stat); err != nil {
		t.Fatalf("planMigration: %v\n%s", err, out)
	}
	if got := cfg.Defaults.Network.Get().InternalZones; !slices.Equal(got, []string{"corp.example"}) {
		t.Fatalf("defaults internal_zones = %v", got)
	}
	if got := cfg.Workspaces["web"].Network.InternalZones; len(got) != 0 {
		t.Fatalf("workspace internal_zones = %v, want none", got)
	}
}

func TestPlanMigrationInternalZonesRetry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, nil)}
	// The first answer mixes a valid zone with a public suffix: nothing of it is kept.
	p, out := scripted("corp.example, com", "app.nip.io", "10.0.0.1", "*", "lab.example")
	if _, err := planMigration(cfg, p, os.Stat); err != nil {
		t.Fatalf("planMigration: %v\n%s", err, out)
	}
	if want := []string{"lab.example"}; !slices.Equal(cfg.Defaults.Network.InternalZones, want) {
		t.Fatalf("internal_zones = %v, want %v\n%s", cfg.Defaults.Network.InternalZones, want, out)
	}
	if n := strings.Count(out.String(), internalZonesPrompt); n != 5 {
		t.Fatalf("prompted %d times, want 5\n%s", n, out)
	}
	for _, want := range []string{`"com"`, `"app.nip.io"`, `"10.0.0.1"`, `"*"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output misses error for %s:\n%s", want, out)
		}
	}
}

func TestPlanMigrationInternalZonesEOF(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Defaults: vmDefaultsWithRules([]string{"git.corp.example"}, nil, nil)}
	out := &bytes.Buffer{}
	p := &prompter{in: bufio.NewReader(strings.NewReader("")), out: out, resolver: &fakeResolver{}}
	_, err := planMigration(cfg, p, os.Stat)
	if err == nil || !strings.Contains(err.Error(), "read internal zones") || !errors.Is(err, io.EOF) {
		t.Fatalf("planMigration error = %v, want wrapped read error\n%s", err, out)
	}
	if len(cfg.Defaults.Network.InternalZones) != 0 {
		t.Fatalf("internal_zones = %v, want none", cfg.Defaults.Network.InternalZones)
	}
}

const internalZonesConfig = `[defaults]
backend = "vm"

[[defaults.network.rules]]
name = "internet"
allow = ["*", "git.corp.example"]
`

func TestRunMigrateAddsInternalZones(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(internalZonesConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	p, out := scripted("corp.example", "y")
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	backup, err := os.ReadFile(path + ".pre-vm-migration") //nolint:gosec // G304: test file
	if err != nil || string(backup) != internalZonesConfig {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	cfg, err := config.ReadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Defaults.Network.Get().InternalZones; !slices.Equal(got, []string{"corp.example"}) {
		t.Fatalf("internal_zones = %v\n%s", got, out)
	}

	p, out = scripted()
	if err := runMigrate(context.Background(), path, p); err != nil || strings.Contains(out.String(), internalZonesPrompt) || !strings.Contains(out.String(), "nothing to migrate") {
		t.Fatalf("second run = %v\n%s", err, out)
	}
}

func TestRunMigrateDeclinedKeepsInternalZonesUnapplied(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(internalZonesConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	p, out := scripted("corp.example", "n")
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != internalZonesConfig { //nolint:gosec // G304: test file
		t.Fatalf("config changed = %q, %v", data, err)
	}
	if _, err := os.Stat(path + ".pre-vm-migration"); !os.IsNotExist(err) {
		t.Fatalf("backup exists: %v", err)
	}
}

// TestRunMigrateDryRunAsksInternalZones is not parallel: it sets the
// migrateDryRun flag, and parallel tests only resume after serial ones finish.
func TestRunMigrateDryRunAsksInternalZones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(internalZonesConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	migrateDryRun = true
	t.Cleanup(func() { migrateDryRun = false })
	p, out := scripted("corp.example")
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), internalZonesPrompt) || !strings.Contains(out.String(), `internal_zones = ["corp.example"]`) {
		t.Fatalf("dry run output:\n%s", out)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != internalZonesConfig { //nolint:gosec // G304: test file
		t.Fatalf("config changed = %q, %v", data, err)
	}
	if _, err := os.Stat(path + ".pre-vm-migration"); !os.IsNotExist(err) {
		t.Fatalf("backup exists: %v", err)
	}
}

func TestAddAgentEndpoints(t *testing.T) {
	t.Parallel()
	opencode, _ := agent.Lookup("opencode")
	claude, _ := agent.Lookup("claude")

	t.Run("defaults and differing workspaces", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Defaults: config.Defaults{Agents: map[string]config.AgentSettings{"mine": {From: "claude"}}},
			Workspaces: map[string]config.Workspace{
				"web":  {},
				"api":  {Agent: "claude"},
				"tool": {Agent: "mine"},
			},
		}
		m := &migration{}
		addAgentEndpoints(cfg, []string{"api", "tool", "web"}, m)
		if got := cfg.Defaults.Network.Get().AgentEndpoints; !slices.Equal(got, opencode.Endpoints) {
			t.Fatalf("defaults agent_endpoints = %v, want %v", got, opencode.Endpoints)
		}
		for _, name := range []string{"api", "tool"} {
			if got := cfg.Workspaces[name].Network.Get().AgentEndpoints; !slices.Equal(got, claude.Endpoints) {
				t.Fatalf("%s agent_endpoints = %v, want %v", name, got, claude.Endpoints)
			}
		}
		if got := cfg.Workspaces["web"].Network.Get().AgentEndpoints; got != nil {
			t.Fatalf("web agent_endpoints = %v, want inherited", got)
		}
		want := []string{
			`[defaults.network]: add agent_endpoints of agent "opencode"`,
			`[workspaces.api.network]: add agent_endpoints of agent "claude"`,
			`[workspaces.tool.network]: add agent_endpoints of agent "mine"`,
		}
		if !slices.Equal(m.changes, want) {
			t.Fatalf("changes = %v, want %v", m.changes, want)
		}
	})

	t.Run("already configured", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{Workspaces: map[string]config.Workspace{
			"web": {Network: &config.Network{AgentEndpoints: []string{}}},
		}}
		m := &migration{}
		addAgentEndpoints(cfg, []string{"web"}, m)
		if cfg.Defaults.Network != nil || len(m.changes) != 0 {
			t.Fatalf("changed configured agent_endpoints: network %+v, changes %v", cfg.Defaults.Network, m.changes)
		}
	})
}
