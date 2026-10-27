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
	if cfg.Defaults.Backend != config.BackendVM || cfg.Workspaces["web"].Backend != "" {
		t.Fatalf("config not migrated: %+v", cfg.Defaults)
	}

	p, out = scripted()
	if err := runMigrate(context.Background(), path, p); err != nil || !strings.Contains(out.String(), "nothing to migrate") {
		t.Fatalf("second run = %v\n%s", err, out)
	}
}
