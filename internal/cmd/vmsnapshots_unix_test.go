//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestVMSnapshotsGitConfigDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gitconfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		git       bool
		snapshots []string
		want      []string
	}{
		{name: "git_config becomes a snapshot", git: true, want: []string{gitconfig + ":" + guestGitConfig}},
		{name: "git_config off", git: false},
		{name: "snapshot overrides git_config", git: true, snapshots: []string{"/data/team.gitconfig:" + guestGitConfig + ":ro"}, want: []string{"/data/team.gitconfig:" + guestGitConfig}},
		{name: "removal drops git_config", git: true, snapshots: []string{":" + guestGitConfig + ":ro"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := vmSnapshots(&workspace.Resolved{Name: "w", GitConfig: tt.git, Snapshots: tt.snapshots})
			if err != nil {
				t.Fatalf("vmSnapshots: %v", err)
			}
			var pairs []string
			for _, m := range got {
				if m.Mode != "ro" {
					t.Fatalf("snapshot %v is not read-only", m)
				}
				pairs = append(pairs, m.Host+":"+m.Container)
			}
			if strings.Join(pairs, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("snapshots = %v, want %v", pairs, tt.want)
			}
		})
	}
}

func TestCheckSnapshots(t *testing.T) {
	t.Parallel()
	ws := &workspace.Resolved{Name: "w"}
	shares := []vm.Share{{HostPath: "/h/oc", GuestPath: "/home/agent/.config/opencode"}, {HostPath: "/p", GuestPath: "/src/project"}}
	tests := []struct {
		target    string
		wantError string
	}{
		{target: "/home/agent/.npmrc"},
		{target: "/home/agent/.config/gh"},
		{target: "/src/project/.env", wantError: "overlaps the shared path"},
		{target: "/home/agent/.config", wantError: "overlaps the shared path"},
		{target: "/src/project", wantError: "overlaps the shared path"},
		{target: guest.Dir + "/x", wantError: "reserved by jailoc"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			t.Parallel()
			err := checkSnapshots(ws, []config.Mount{{Host: "/h/x", Container: tt.target, Mode: "ro"}}, shares)
			if tt.wantError == "" && err != nil {
				t.Fatalf("checkSnapshots: %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("checkSnapshots error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestStageSnapshots(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	file := filepath.Join(src, "npmrc")
	if err := os.WriteFile(file, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(src, "gh")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "hosts.yml"), []byte("github.com"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/hosts.yml", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	staged := filepath.Join(vm.GuestDataDir(state), snapshotsDir)
	stale := filepath.Join(staged, "stale")
	if err := os.MkdirAll(stale, 0o750); err != nil {
		t.Fatal(err)
	}

	binds, err := stageSnapshots(state, []config.Mount{
		{Host: file, Container: "/home/agent/.npmrc", Mode: "ro"},
		{Host: dir, Container: "/home/agent/.config/gh", Mode: "ro"},
	}, "")
	if err != nil {
		t.Fatalf("stageSnapshots: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale staged entry survived: %v", err)
	}
	gen := snapshotGeneration(binds)
	want := []guest.Snapshot{
		{Source: guest.SnapshotsDir + "/" + gen + "/0", Target: "/home/agent/.npmrc"},
		{Source: guest.SnapshotsDir + "/" + gen + "/1", Target: "/home/agent/.config/gh"},
	}
	if gen == "" || !slices.Equal(binds, want) {
		t.Fatalf("binds = %v, want %v", binds, want)
	}
	genDir := filepath.Join(staged, gen)
	if got, _ := os.ReadFile(filepath.Join(genDir, "0")); string(got) != "token" { //nolint:gosec // G304: test file
		t.Fatalf("staged file = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(genDir, "1", "sub", "hosts.yml")); string(got) != "github.com" { //nolint:gosec // G304: test file
		t.Fatalf("staged nested file = %q", got)
	}
	if link, err := os.Readlink(filepath.Join(genDir, "1", "link")); err != nil || link != "sub/hosts.yml" {
		t.Fatalf("staged link = %q, %v", link, err)
	}

	// A reload stages a new generation with the current content and keeps
	// the one the running guest binds.
	if err := os.WriteFile(file, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := stageSnapshots(state, []config.Mount{{Host: file, Container: "/home/agent/.npmrc", Mode: "ro"}}, gen)
	if err != nil {
		t.Fatalf("stageSnapshots reload: %v", err)
	}
	next := snapshotGeneration(reloaded)
	if next == gen || len(reloaded) != 1 {
		t.Fatalf("reload binds = %v, previous generation %q", reloaded, gen)
	}
	if got, _ := os.ReadFile(filepath.Join(staged, next, "0")); string(got) != "rotated" { //nolint:gosec // G304: test file
		t.Fatalf("reloaded file = %q", got)
	}
	if _, err := os.Stat(genDir); err != nil {
		t.Fatalf("running generation removed: %v", err)
	}

	// The next reload removes generations nothing binds.
	if _, err := stageSnapshots(state, nil, next); err != nil {
		t.Fatalf("stageSnapshots empty: %v", err)
	}
	if _, err := os.Stat(genDir); !os.IsNotExist(err) {
		t.Fatalf("unused generation survived: %v", err)
	}

	if _, err := stageSnapshots(state, []config.Mount{{Host: filepath.Join(src, "missing"), Container: "/home/agent/x", Mode: "ro"}}, ""); err == nil {
		t.Fatal("missing source: want error")
	}
	if entries, _ := os.ReadDir(staged); len(entries) != 0 {
		t.Fatalf("failed staging left %v", entries)
	}
}

func TestStageSnapshotsDirectorySymlinkRoot(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		target func(string) string
	}{
		{name: "absolute", target: func(dir string) string { return dir }},
		{name: "relative", target: func(string) string { return "actual" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := t.TempDir()
			actual := filepath.Join(src, "actual")
			if err := os.Mkdir(actual, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(actual, "data"), []byte("copied"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("data", filepath.Join(actual, "nested-link")); err != nil {
				t.Fatal(err)
			}
			linked := filepath.Join(src, "linked")
			if err := os.Symlink(tt.target(actual), linked); err != nil {
				t.Fatal(err)
			}
			state := t.TempDir()
			binds, err := stageSnapshots(state, []config.Mount{{Host: linked, Container: "/home/agent/data", Mode: "ro"}}, "")
			if err != nil {
				t.Fatalf("stageSnapshots: %v", err)
			}
			staged := filepath.Join(vm.GuestDataDir(state), snapshotsDir, snapshotGeneration(binds), "0")
			info, err := os.Lstat(staged)
			if err != nil {
				t.Fatalf("lstat staged root: %v", err)
			}
			if !info.IsDir() {
				t.Fatalf("staged root mode = %v, want directory", info.Mode())
			}
			content, err := os.ReadFile(filepath.Join(staged, "data")) //nolint:gosec // G304: staged snapshot inside t.TempDir
			if err != nil || string(content) != "copied" {
				t.Fatalf("staged content = %q, %v", content, err)
			}
			target, err := os.Readlink(filepath.Join(staged, "nested-link"))
			if err != nil || target != "data" {
				t.Fatalf("nested link = %q, %v", target, err)
			}
		})
	}
}
