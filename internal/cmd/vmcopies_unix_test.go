//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestVMCopiesGitConfigDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gitconfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		git    bool
		copies []string
		want   []string
	}{
		{name: "git_config becomes a copy", git: true, want: []string{gitconfig + ":" + guestGitConfig}},
		{name: "git_config off", git: false},
		{name: "copy overrides git_config", git: true, copies: []string{"/data/team.gitconfig:" + guestGitConfig + ":ro"}, want: []string{"/data/team.gitconfig:" + guestGitConfig}},
		{name: "removal drops git_config", git: true, copies: []string{":" + guestGitConfig + ":ro"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := vmCopies(&workspace.Resolved{Name: "w", GitConfig: tt.git, Copies: tt.copies})
			if err != nil {
				t.Fatalf("vmCopies: %v", err)
			}
			var pairs []string
			for _, m := range got {
				if m.Mode != "ro" {
					t.Fatalf("copy %v is not read-only", m)
				}
				pairs = append(pairs, m.Host+":"+m.Container)
			}
			if strings.Join(pairs, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("copies = %v, want %v", pairs, tt.want)
			}
		})
	}
}

func TestCheckCopies(t *testing.T) {
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
			err := checkCopies(ws, []config.Mount{{Host: "/h/x", Container: tt.target, Mode: "ro"}}, shares)
			if tt.wantError == "" && err != nil {
				t.Fatalf("checkCopies: %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("checkCopies error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestStageCopies(t *testing.T) {
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
	stale := filepath.Join(state, "copies", "stale")
	if err := os.MkdirAll(stale, 0o750); err != nil {
		t.Fatal(err)
	}

	staged, binds, err := stageCopies(state, []config.Mount{
		{Host: file, Container: "/home/agent/.npmrc", Mode: "ro"},
		{Host: dir, Container: "/home/agent/.config/gh", Mode: "ro"},
	})
	if err != nil {
		t.Fatalf("stageCopies: %v", err)
	}
	if staged != filepath.Join(state, "copies") {
		t.Fatalf("staged dir = %q", staged)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale staged entry survived: %v", err)
	}
	want := []guest.Copy{{Source: guest.CopiesDir + "/0", Target: "/home/agent/.npmrc"}, {Source: guest.CopiesDir + "/1", Target: "/home/agent/.config/gh"}}
	if len(binds) != 2 || binds[0] != want[0] || binds[1] != want[1] {
		t.Fatalf("binds = %v, want %v", binds, want)
	}
	if got, _ := os.ReadFile(filepath.Join(staged, "0")); string(got) != "token" { //nolint:gosec // G304: test file
		t.Fatalf("staged file = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(staged, "1", "sub", "hosts.yml")); string(got) != "github.com" { //nolint:gosec // G304: test file
		t.Fatalf("staged nested file = %q", got)
	}
	if link, err := os.Readlink(filepath.Join(staged, "1", "link")); err != nil || link != "sub/hosts.yml" {
		t.Fatalf("staged link = %q, %v", link, err)
	}

	if _, _, err := stageCopies(state, []config.Mount{{Host: filepath.Join(src, "missing"), Container: "/home/agent/x", Mode: "ro"}}); err == nil {
		t.Fatal("missing source: want error")
	}
	if dir, binds, err := stageCopies(state, nil); err != nil || dir != "" || binds != nil {
		t.Fatalf("no copies = %q, %v, %v", dir, binds, err)
	}
}
