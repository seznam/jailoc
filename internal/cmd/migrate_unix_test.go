//go:build darwin || linux

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
)

func scripted(answers ...string) (*prompter, *bytes.Buffer) {
	out := &bytes.Buffer{}
	in := strings.NewReader(strings.Join(answers, "\n") + "\n")
	return &prompter{in: bufio.NewReader(in), out: out}, out
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
}

func TestRunMigrateMovesNetwork(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `[defaults]
backend = "vm"
filtered_dns = true
dns_upstream = "10.20.30.53"
dns_blocked_zones = ["corp.example.com"]

[workspaces.web]
paths = ["/src/web"]
filtered_dns = false
dns_upstream = "10.20.30.54"
dns_blocked_zones = []

[workspaces.web.network]
dns_upstream = "10.20.30.55"

[workspaces.api]
paths = ["/src/api"]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadRaw(path); err != nil {
		t.Fatal(err)
	}
	p, out := scripted("y")
	if err := runMigrate(context.Background(), path, p); err != nil {
		t.Fatalf("runMigrate: %v\n%s", err, out)
	}
	for _, want := range []string{
		"[defaults]: move filtered_dns to [defaults.network]",
		"[workspaces.web]: move filtered_dns to [workspaces.web.network]",
		"[workspaces.web]: remove dns_upstream (set in [workspaces.web.network])",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output misses %q:\n%s", want, out)
		}
	}
	cfg, err := config.ReadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	d, web := cfg.Defaults, cfg.Workspaces["web"]
	if len(config.LegacyNetwork(d.LegacyFilteredDNS, d.LegacyDNSUpstream, d.LegacyDNSBlockedZones)) > 0 ||
		len(config.LegacyNetwork(web.LegacyFilteredDNS, web.LegacyDNSUpstream, web.LegacyDNSBlockedZones)) > 0 {
		t.Fatalf("legacy settings left: defaults %+v, web %+v", d, web)
	}
	if d.Network.FilteredDNS == nil || !*d.Network.FilteredDNS || d.Network.DNSUpstream != "10.20.30.53" ||
		!slices.Equal(d.Network.DNSBlockedZones, []string{"corp.example.com"}) {
		t.Fatalf("defaults network = %+v", d.Network)
	}
	if web.Network.FilteredDNS == nil || *web.Network.FilteredDNS || web.Network.DNSUpstream != "10.20.30.55" {
		t.Fatalf("web network = %+v", web.Network)
	}
	if web.Network.DNSBlockedZones == nil || len(web.Network.DNSBlockedZones) != 0 {
		t.Fatalf("web dns_blocked_zones = %#v, want the explicitly empty list kept", web.Network.DNSBlockedZones)
	}
	if err := config.ValidateCopy(cfg); err != nil {
		t.Fatalf("migrated config invalid: %v", err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[workspaces.api.network]") {
		t.Fatalf("empty network table written:\n%s", data)
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
			if cfg.Defaults.Network.FilteredDNS == nil || !*cfg.Defaults.Network.FilteredDNS || cfg.Defaults.LegacyFilteredDNS != nil {
				t.Fatalf("filtered_dns not moved: %+v", cfg.Defaults)
			}
		})
	}
}
