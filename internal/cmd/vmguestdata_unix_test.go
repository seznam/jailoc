//go:build darwin || linux

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// Claude Code's bypass-permissions disclaimer counts as accepted unless its
// global configuration sets the key; other keys are kept, and a failed write
// leaves the file as it was.
func TestAddStateDefaults(t *testing.T) {
	t.Parallel()
	h, _ := agent.Lookup("claude")
	// tmp is what is left at the temporary path: "file" a stale file, "dir"
	// a directory that cannot be replaced, so the write fails.
	for name, tc := range map[string]struct{ in, tmp, want string }{
		"missing file":    {want: `{"bypassPermissionsModeAccepted":true}`},
		"other keys":      {in: `{"userID":"u","projects":{"/p":{"a":1}}}`, want: `{"bypassPermissionsModeAccepted":true,"projects":{"/p":{"a":1}},"userID":"u"}`},
		"set false":       {in: `{"bypassPermissionsModeAccepted":false}`, want: `{"bypassPermissionsModeAccepted":false}`},
		"null":            {in: `null`, want: `{"bypassPermissionsModeAccepted":true}`},
		"invalid":         {in: `{`, want: `{`},
		"stale temp file": {in: `{"userID":"u"}`, tmp: "file", want: `{"bypassPermissionsModeAccepted":true,"userID":"u"}`},
		"failed write":    {in: `{"userID":"u"}`, tmp: "dir", want: `{"userID":"u"}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			file := filepath.Join(data, "claude", ".claude.json")
			tmp := file + ".jailoc-tmp"
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.in != "" {
				if err := os.WriteFile(file, []byte(tc.in), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.tmp {
			case "file":
				if err := os.WriteFile(tmp, []byte("junk"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dir":
				if err := os.MkdirAll(filepath.Join(tmp, "x"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(data)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			addStateDefaults(root, "claude", h.State[0], h)
			got, err := os.ReadFile(file) //nolint:gosec // G304: test temp file
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			if json.Compact(&b, got) != nil {
				b.Reset()
				b.Write(got)
			}
			if b.String() != tc.want {
				t.Errorf(".claude.json = %s, want %s", b.String(), tc.want)
			}
			if _, err := os.Lstat(tmp); tc.tmp != "dir" && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("temporary file left behind: %v", err)
			}
		})
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

func TestWorkspaceStorageDisks(t *testing.T) {
	t.Parallel()
	oc, _ := agent.Lookup("opencode1")
	names := func(h agent.Harness) []string {
		var out []string
		for _, d := range h.State {
			out = append(out, d.Name)
		}
		return out
	}
	tests := []struct {
		name      string
		shares    []vm.Share
		wantState []string
		wantCache bool
	}{
		{name: "no shares", wantState: names(oc), wantCache: true},
		{name: "one state share", shares: []vm.Share{{GuestPath: oc.State[0].Path}}, wantState: names(oc)[1:], wantCache: true},
		{name: "cache share", shares: []vm.Share{{GuestPath: guestUserCache + "/"}}, wantState: names(oc), wantCache: false},
		{name: "everything shared", shares: func() []vm.Share {
			out := []vm.Share{{GuestPath: guestUserCache}}
			for _, d := range oc.State {
				out = append(out, vm.Share{GuestPath: d.Path})
			}
			return out
		}(), wantState: nil, wantCache: false},
		{name: "unrelated share", shares: []vm.Share{{GuestPath: "/work"}}, wantState: names(oc), wantCache: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, cache := workspaceStorageDisks(&workspace.Resolved{Agent: oc}, tt.shares)
			if !slices.Equal(state, tt.wantState) || cache != tt.wantCache {
				t.Fatalf("workspaceStorageDisks = %v, %v, want %v, %v", state, cache, tt.wantState, tt.wantCache)
			}
		})
	}
}

func TestAgentStateHostDirStateDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oc, _ := agent.Lookup("opencode1")
	oc.Version = "1.19.5"
	ws := &workspace.Resolved{Name: "szn", Agent: oc, AgentInstall: true}
	want := canonicalHostPath(vm.StateDiskPath(vmStateDir("szn"))) + "#" + oc.State[0].Name
	if got := agentStateHostDir(nil, ws, nil); got != want {
		t.Fatalf("agentStateHostDir = %q, want %q", got, want)
	}
	// purgeWorkspaceAgentStateVersions must clean it up.
	recordAgentStateVersion(nil, ws, nil)
	if versions := loadAgentVersions(agentVersionsPath()); versions[want] != "1.19.5" {
		t.Fatalf("versions = %v, want %q recorded", versions, want)
	}
	purgeWorkspaceAgentStateVersions(vmStateDir("szn"))
	if versions := loadAgentVersions(agentVersionsPath()); len(versions) != 0 {
		t.Fatalf("versions after purge = %v, want empty", versions)
	}
	ephemeral := config.EphemeralProfile()
	ws.AgentProfile = &ephemeral
	if got := agentStateHostDir(nil, ws, nil); got != "" {
		t.Fatalf("ephemeral agentStateHostDir = %q, want empty", got)
	}
}

func TestCheckAgentStateVersionMigratesLegacyRecord(t *testing.T) {
	// HOME decides the state directory; t.Setenv forbids t.Parallel.
	oc, _ := agent.Lookup("opencode1")
	oc.Version = "1.19.5"
	name := oc.State[0].Name
	tests := []struct {
		name       string
		legacy     string
		current    string
		want       string
		wantRecord bool
	}{
		{name: "legacy only", legacy: "1.20.0", want: "1.20.0", wantRecord: true},
		{name: "both, legacy higher", legacy: "1.20.0", current: "1.19.0", want: "1.20.0", wantRecord: true},
		{name: "both, new higher", legacy: "1.18.0", current: "1.20.0", want: "1.20.0", wantRecord: true},
		{name: "neither", wantRecord: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			ws := &workspace.Resolved{Name: "szn", Agent: oc, AgentInstall: true}
			newKey := stateDiskVersionKey("szn", name)
			legacyKey := canonicalHostPath(filepath.Join(vm.GuestDataDir(vmStateDir("szn")), guestDataHome, name))
			versions := map[string]string{}
			if tt.legacy != "" {
				versions[legacyKey] = tt.legacy
			}
			if tt.current != "" {
				versions[newKey] = tt.current
			}
			saveAgentVersions(agentVersionsPath(), versions)

			checkAgentStateVersion(nil, ws, nil)

			got := loadAgentVersions(agentVersionsPath())
			if v, ok := got[newKey]; ok != tt.wantRecord || v != tt.want {
				t.Fatalf("record %q = %q, %v, want %q, %v (all: %v)", newKey, v, ok, tt.want, tt.wantRecord, got)
			}
			if tt.legacy != "" && got[legacyKey] != tt.legacy {
				t.Fatalf("legacy record changed: %v", got)
			}
		})
	}
}

func TestCheckAgentStateVersionWarnsAfterLegacyMigration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oc, _ := agent.Lookup("opencode1")
	oc.Version = "1.19.5"
	ws := &workspace.Resolved{Name: "szn", Agent: oc, AgentInstall: true}
	legacyKey := canonicalHostPath(filepath.Join(vm.GuestDataDir(vmStateDir("szn")), guestDataHome, oc.State[0].Name))
	saveAgentVersions(agentVersionsPath(), map[string]string{legacyKey: "1.20.0"})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	checkAgentStateVersion(nil, ws, nil)
	os.Stderr = orig
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	assertContains(t, buf.String(), "newer version 1.20.0")
}

func TestArchiveLegacyHome(t *testing.T) {
	// Writes to os.Stderr through the notice; keep it sequential.
	setup := func(t *testing.T, marker bool) (stateDir, home string) {
		t.Helper()
		stateDir = t.TempDir()
		data := vm.GuestDataDir(stateDir)
		home = filepath.Join(data, guestDataHome)
		if err := os.MkdirAll(filepath.Join(home, "claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "claude", "history"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(data, vm.ControlName), 0o700); err != nil {
			t.Fatal(err)
		}
		if marker {
			if err := os.WriteFile(filepath.Join(data, vm.ControlName, "home-migrated"), []byte("ok\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return stateDir, home
	}

	t.Run("without the guest marker nothing moves", func(t *testing.T) {
		stateDir, home := setup(t, false)
		if err := archiveLegacyHome(stateDir, "w"); err != nil {
			t.Fatalf("archiveLegacyHome: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, "claude", "history")); err != nil {
			t.Fatalf("legacy home moved without a marker: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "legacy-home")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy-home created without a marker: %v", err)
		}
	})

	t.Run("moves the legacy home once migrated", func(t *testing.T) {
		stateDir, home := setup(t, true)
		if err := archiveLegacyHome(stateDir, "w"); err != nil {
			t.Fatalf("archiveLegacyHome: %v", err)
		}
		if b, err := os.ReadFile(filepath.Join(stateDir, "legacy-home", "claude", "history")); err != nil || string(b) != "old" { //nolint:gosec // G304: test file
			t.Fatalf("archived history = %q, %v", b, err)
		}
		if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy home still in the guest data directory: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(vm.GuestDataDir(stateDir), vm.ControlName, "home-migrated")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("marker kept: %v", err)
		}
		// A second run has nothing to do.
		if err := archiveLegacyHome(stateDir, "w"); err != nil {
			t.Fatalf("second archiveLegacyHome: %v", err)
		}
	})

	t.Run("never replaces an earlier archive", func(t *testing.T) {
		stateDir, _ := setup(t, true)
		earlier := filepath.Join(stateDir, "legacy-home")
		if err := os.MkdirAll(earlier, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(earlier, "keep"), []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := archiveLegacyHome(stateDir, "w"); err != nil {
			t.Fatalf("archiveLegacyHome: %v", err)
		}
		if b, err := os.ReadFile(filepath.Join(earlier, "keep")); err != nil || string(b) != "kept" { //nolint:gosec // G304: test file
			t.Fatalf("earlier archive = %q, %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "legacy-home-1", "claude", "history")); err != nil {
			t.Fatalf("second archive missing: %v", err)
		}
	})

	t.Run("a symlinked control directory is not followed", func(t *testing.T) {
		stateDir, home := setup(t, false)
		outside := t.TempDir()
		victim := filepath.Join(outside, "home-migrated")
		if err := os.WriteFile(victim, []byte("host file"), 0o600); err != nil {
			t.Fatal(err)
		}
		control := filepath.Join(vm.GuestDataDir(stateDir), vm.ControlName)
		if err := os.Remove(control); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, control); err != nil {
			t.Fatal(err)
		}
		if err := archiveLegacyHome(stateDir, "w"); err != nil {
			t.Fatalf("archiveLegacyHome: %v", err)
		}
		if b, err := os.ReadFile(victim); err != nil || string(b) != "host file" { //nolint:gosec // G304: test file
			t.Fatalf("file outside the share = %q, %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(home, "claude", "history")); err != nil {
			t.Fatalf("legacy home moved: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(stateDir, "legacy-home")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy-home created: %v", err)
		}
	})

	t.Run("a symlinked home is not archived", func(t *testing.T) {
		stateDir, home := setup(t, true)
		outside := t.TempDir()
		if err := os.RemoveAll(home); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, home); err != nil {
			t.Fatal(err)
		}
		if err := archiveLegacyHome(stateDir, "w"); err != nil {
			t.Fatalf("archiveLegacyHome: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(stateDir, "legacy-home")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy-home created for a symlink: %v", err)
		}
		if info, err := os.Lstat(home); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("home link changed: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(vm.GuestDataDir(stateDir), vm.ControlName, "home-migrated")); err != nil {
			t.Fatalf("marker removed: %v", err)
		}
	})

	t.Run("no legacy home", func(t *testing.T) {
		if err := archiveLegacyHome(t.TempDir(), "w"); err != nil {
			t.Fatalf("archiveLegacyHome: %v", err)
		}
	})
}
