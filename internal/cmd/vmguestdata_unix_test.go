//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// The guest can replace anything in the guest data directory; syncing must
// neither follow its links nor keep entries the host no longer has.
func TestSyncDirStaysInsideRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	data := filepath.Join(dir, "data")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{filepath.Join(src, "sub"), data, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Planted by a malicious guest: a link out of the root where the copy
	// goes, and a stale file.
	if err := os.MkdirAll(filepath.Join(data, "cfg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(data, "cfg", "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "cfg", "stale"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := syncDir(root, "cfg", src); err != nil {
		t.Fatalf("syncDir: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "a.txt")); string(b) != "host secret" { //nolint:gosec // G304: test file
		t.Fatalf("sync wrote through the planted link: %q", b)
	}
	if b, err := os.ReadFile(filepath.Join(data, "cfg", "sub", "a.txt")); err != nil || string(b) != "new" { //nolint:gosec // G304: test file
		t.Fatalf("copy = %q, %v", b, err)
	}
	if info, err := os.Lstat(filepath.Join(data, "cfg", "sub")); err != nil || !info.IsDir() {
		t.Fatalf("planted link not replaced by a directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(data, "cfg", "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale entry kept: %v", err)
	}
	if err := syncDir(root, "missing", filepath.Join(dir, "nope")); err != nil {
		t.Fatalf("syncDir of a missing source: %v", err)
	}
}

// Mount points the guest created for import_agent_config snapshots are removed
// before a start; agent files with content are kept.
func TestRemoveConfigMountPoints(t *testing.T) {
	t.Parallel()
	h, _ := agent.Lookup("claude")
	data := t.TempDir()
	state := filepath.Join(data, "claude")
	for _, d := range []string{filepath.Join(state, "agents"), filepath.Join(state, "skills", "x"), filepath.Join(state, "projects")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"settings.json": "", "CLAUDE.md": "# notes", ".claude.json": ""} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := removeConfigMountPoints(root, "claude", h.State[0], h); err != nil {
		t.Fatalf("removeConfigMountPoints: %v", err)
	}
	for name, want := range map[string]bool{"settings.json": false, "agents": false, "CLAUDE.md": true, "skills": true, "projects": true, ".claude.json": true} {
		if _, err := os.Lstat(filepath.Join(state, name)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", name, err == nil, want)
		}
	}
}

func TestCheckAndRecordAgentStateVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h1, _ := agent.Lookup("opencode1")
	h2, _ := agent.Lookup("opencode2")
	ws := &workspace.Resolved{Name: "szn", Agent: h1, AgentInstall: true}

	// Pre-start check alone does not record the version before the VM boots.
	ws.Agent.Version = "1.19.5"
	checkAgentStateVersion(nil, ws, nil)
	if _, err := os.Stat(agentVersionsPath()); !os.IsNotExist(err) {
		t.Fatalf("checkAgentStateVersion created versions file before start: %v", err)
	}

	// Direct image strategies skip recording unverified agent versions.
	wsDirect := &workspace.Resolved{Name: "szn", Agent: h1, AgentInstall: true, Image: "custom:latest"}
	wsDirect.Agent.Version = "1.19.5"
	recordAgentStateVersion(&config.Config{}, wsDirect, nil)
	if _, err := os.Stat(agentVersionsPath()); !os.IsNotExist(err) {
		t.Fatalf("recordAgentStateVersion recorded version for direct image: %v", err)
	}

	// JSON null in the metadata file must not panic when recording.
	if err := os.MkdirAll(filepath.Dir(agentVersionsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentVersionsPath(), []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws.Agent.Version = "1.18.4"
	recordAgentStateVersion(nil, ws, nil)
	ws.Agent.Version = "1.19.5"
	recordAgentStateVersion(nil, ws, nil)

	data, err := os.ReadFile(agentVersionsPath()) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatalf("read versions file: %v", err)
	}
	if !strings.Contains(string(data), `"1.19.5"`) {
		t.Fatalf("versions file = %s, want 1.19.5", data)
	}

	// Downgrading must not overwrite the recorded higher version.
	ws.Agent.Version = "1.18.4"
	checkAgentStateVersion(nil, ws, nil)
	recordAgentStateVersion(nil, ws, nil)
	data, err = os.ReadFile(agentVersionsPath()) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatalf("read versions file after downgrade: %v", err)
	}
	if !strings.Contains(string(data), `"1.19.5"`) {
		t.Fatalf("versions file after downgrade = %s, want 1.19.5 preserved", data)
	}

	// Purging the workspace removes its workspace-owned state history.
	purgeWorkspaceAgentStateVersions(vmStateDir("szn"))
	versions := loadAgentVersions(agentVersionsPath())
	if len(versions) != 0 {
		t.Fatalf("versions after purge = %v, want empty", versions)
	}

	// An explicit mount (even with an ephemeral profile or symlink alias) tracks
	// history by the canonical backing host directory.
	realDir := filepath.Join(t.TempDir(), "shared-opencode")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(t.TempDir(), "shared-opencode-link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	ephemeral := config.EphemeralProfile()
	shares := []vm.Share{{HostPath: linkDir, GuestPath: "/home/agent/.local/share/opencode"}}
	ws2 := &workspace.Resolved{Name: "other", Agent: h2, AgentInstall: true, AgentProfile: &ephemeral}
	ws2.Agent.Version = "2.2.5"
	recordAgentStateVersion(nil, ws2, shares)

	versions = loadAgentVersions(agentVersionsPath())
	wantKey := canonicalHostPath(realDir)
	if got := versions[wantKey]; got != "2.2.5" {
		t.Fatalf("versions[%q] = %q, want 2.2.5 (all: %v)", wantKey, got, versions)
	}
}
