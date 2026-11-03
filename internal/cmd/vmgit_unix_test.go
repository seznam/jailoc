//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/seznam/jailoc/internal/vm"
)

func TestGitProtection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Workspace path itself is a repository with a submodule.
	mk(".git/hooks")
	write(".git/config", "[core]\n")
	mk(".git/modules/sub/hooks")
	write(".git/modules/sub/config", "[core]\n")
	write(".git/modules/sub/HEAD", "ref: refs/heads/main\n")
	mk("sub")
	write("sub/.git", "gitdir: ../.git/modules/sub\n")
	// Nested repository without a hooks directory.
	mk("a/b/repo/.git")
	write("a/b/repo/.git/config", "[core]\n")
	// Repositories inside skipped directories are ignored.
	mk("node_modules/pkg/.git/hooks")
	// Checkout of a separate git directory (git init --separate-git-dir)
	// that has no hooks directory yet.
	mk("meta.git")
	write("meta.git/HEAD", "ref: refs/heads/main\n")
	write("meta.git/config", "[core]\n")
	mk("sep")
	write("sep/.git", "gitdir: ../meta.git\n")
	// Linked worktree of the workspace repository, with an absolute gitdir.
	mk(".git/worktrees/wt")
	write(".git/worktrees/wt/commondir", "../..\n")
	write(".git/worktrees/wt/config.worktree", "[core]\n")
	mk("wt")
	write("wt/.git", "gitdir: "+filepath.Join(root, ".git/worktrees/wt")+"\n")
	// Git directories outside the workspace paths are not shared.
	mk("out")
	write("out/.git", "gitdir: /nonexistent/jailoc/outside.git\n")

	pins, ro, err := gitProtection([]vm.Share{{HostPath: root}})
	if err != nil {
		t.Fatalf("gitProtection: %v", err)
	}
	p := func(rel string) string { return filepath.Join(root, rel) }
	wantPins := []string{
		p(".git"), p(".git/modules"), p(".git/modules/sub"),
		p(".git/worktrees"), p(".git/worktrees/wt"),
		p("a"), p("a/b"), p("a/b/repo"), p("a/b/repo/.git"),
		p("meta.git"), p("out"), p("sep"), p("sub"), p("wt"),
	}
	wantRO := []string{
		p(".git/config"), p(".git/hooks"),
		p(".git/modules/sub/config"), p(".git/modules/sub/hooks"),
		p(".git/worktrees/wt/commondir"), p(".git/worktrees/wt/config.worktree"),
		p("a/b/repo/.git/config"), p("a/b/repo/.git/hooks"),
		p("meta.git/config"), p("meta.git/hooks"),
		p("out/.git"), p("sep/.git"), p("sub/.git"), p("wt/.git"),
	}
	slices.Sort(wantPins)
	slices.Sort(wantRO)
	if !slices.Equal(pins, wantPins) {
		t.Errorf("pins = %v\nwant  %v", pins, wantPins)
	}
	if !slices.Equal(ro, wantRO) {
		t.Errorf("read-only = %v\nwant       %v", ro, wantRO)
	}
	if info, err := os.Stat(p("a/b/repo/.git/hooks")); err != nil || !info.IsDir() {
		t.Errorf("missing hooks directory was not created: %v", err)
	}
}

func TestGitProtectionSymlinkRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realRoot := filepath.Join(base, "workspace")
	root := filepath.Join(base, "link")
	for _, rel := range []string{".git/hooks", ".git/worktrees/wt", "nested/repo/.git", "wt"} {
		if err := os.MkdirAll(filepath.Join(realRoot, rel), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realRoot, root); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(realRoot, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/config", "[core]\n")
	write("nested/repo/.git/config", "[core]\n")
	write(".git/worktrees/wt/commondir", "../..\n")
	write(".git/worktrees/wt/config.worktree", "[core]\n")
	resolved, err := filepath.EvalSymlinks(filepath.Join(realRoot, ".git/worktrees/wt"))
	if err != nil {
		t.Fatal(err)
	}
	write("wt/.git", "gitdir: "+resolved+"\n")

	pins, ro, err := gitProtection([]vm.Share{{HostPath: root}})
	if err != nil {
		t.Fatalf("gitProtection: %v", err)
	}
	guest := func(rel string) string { return filepath.Join(root, rel) }
	wantPins := []string{
		guest(".git"), guest(".git/worktrees"), guest(".git/worktrees/wt"),
		guest("nested"), guest("nested/repo"), guest("nested/repo/.git"), guest("wt"),
	}
	wantRO := []string{
		guest(".git/config"), guest(".git/hooks"),
		guest(".git/worktrees/wt/commondir"), guest(".git/worktrees/wt/config.worktree"),
		guest("nested/repo/.git/config"), guest("nested/repo/.git/hooks"), guest("wt/.git"),
	}
	slices.Sort(wantPins)
	slices.Sort(wantRO)
	if !slices.Equal(pins, wantPins) {
		t.Errorf("pins = %v\nwant  %v", pins, wantPins)
	}
	if !slices.Equal(ro, wantRO) {
		t.Errorf("read-only = %v\nwant       %v", ro, wantRO)
	}
}

func TestGitProtectionShares(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	mk := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(base, rel), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(base, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A mount at another guest path holding a repository with
	// extensions.worktreeConfig, core.hooksPath and an include.
	mk("mnt/repo/.git/hooks")
	mk("mnt/repo/tools/hooks")
	write("mnt/repo/.git/config", "[extensions]\n\tworktreeConfig = true\n[core]\n\thooksPath = tools/hooks\n[include]\n\tpath = ../shared.gitconfig\n")
	write("mnt/repo/shared.gitconfig", "[user]\n\tname = x\n")
	write("mnt/repo/tools/hooks/real-hook", "#!/bin/sh\n")
	if err := os.Symlink("real-hook", filepath.Join(base, "mnt/repo/tools/hooks/pre-commit")); err != nil {
		t.Fatal(err)
	}
	// A bare repository.
	mk("mnt/bare.git/objects")
	mk("mnt/bare.git/refs")
	write("mnt/bare.git/HEAD", "ref: refs/heads/main\n")
	// A read-only mount is not scanned.
	mk("ro/other/.git/hooks")

	shares := []vm.Share{
		{HostPath: filepath.Join(base, "mnt"), GuestPath: "/data"},
		{HostPath: filepath.Join(base, "ro"), GuestPath: "/ro", ReadOnly: true},
	}
	_, ro, err := gitProtection(shares)
	if err != nil {
		t.Fatalf("gitProtection: %v", err)
	}
	wantRO := []string{
		"/data/bare.git/config", "/data/bare.git/hooks",
		"/data/repo/.git/config", "/data/repo/.git/config.worktree", "/data/repo/.git/hooks",
		"/data/repo/shared.gitconfig", "/data/repo/tools/hooks", "/data/repo/tools/hooks/real-hook",
	}
	if !slices.Equal(ro, wantRO) {
		t.Errorf("read-only = %v\nwant       %v", ro, wantRO)
	}
	if _, err := os.Stat(filepath.Join(base, "mnt/repo/.git/config.worktree")); err != nil {
		t.Errorf("config.worktree not created: %v", err)
	}
}

func TestGitProtectionSymlinkedConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo/.git/hooks"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real-config"), []byte("[core]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../real-config", filepath.Join(root, "repo/.git/config")); err != nil {
		t.Fatal(err)
	}
	_, _, err := gitProtection([]vm.Share{{HostPath: root}})
	if err == nil {
		t.Fatal("want error for a symlinked git config")
	}
	assertContains(t, err.Error(), "symbolic link")
}

func TestGitProtectionMissingRoot(t *testing.T) {
	t.Parallel()
	if _, _, err := gitProtection([]vm.Share{{HostPath: filepath.Join(t.TempDir(), "missing")}}); err == nil {
		t.Fatal("want error for a missing workspace path")
	}
}

// gitTree returns helpers creating directories and files under base.
func gitTree(t *testing.T, base string) (mk func(string), write func(string, string)) {
	t.Helper()
	mk = func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(base, rel), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write = func(rel, content string) {
		t.Helper()
		mk(filepath.Dir(rel))
		if err := os.WriteFile(filepath.Join(base, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return mk, write
}

func TestGitProtectionWorktreeConfigStaysPerWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk, write := gitTree(t, root)
	mk(".git/hooks")
	write(".git/config", "[extensions]\n\tworktreeConfig = true\n")
	write(".git/worktrees/w1/commondir", "../..\n")
	write(".git/worktrees/w1/config.worktree", "[core]\n\thooksPath = h1\n")
	write("w1/.git", "gitdir: ../.git/worktrees/w1\n")
	write(".git/worktrees/w2/commondir", "../..\n")
	write("w2/.git", "gitdir: ../.git/worktrees/w2\n")

	_, ro, err := gitProtection([]vm.Share{{HostPath: root}})
	if err != nil {
		t.Fatalf("gitProtection: %v", err)
	}
	if !slices.Contains(ro, filepath.Join(root, "w1/h1")) {
		t.Errorf("read-only = %v, want w1/h1", ro)
	}
	for _, p := range []string{filepath.Join(root, "w2/h1"), filepath.Join(root, "h1")} {
		if slices.Contains(ro, p) {
			t.Errorf("read-only = %v, has %s from another worktree", ro, p)
		}
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s created from another worktree's config", p)
		}
	}
}

func TestGitProtectionEveryGuestView(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	mk, write := gitTree(t, base)
	mk("proj/.git/hooks")
	write("proj/.git/config", "[core]\n")
	proj := filepath.Join(base, "proj")

	_, ro, err := gitProtection([]vm.Share{{HostPath: proj, GuestPath: "/a"}, {HostPath: proj, GuestPath: "/b"}})
	if err != nil {
		t.Fatalf("gitProtection: %v", err)
	}
	want := []string{"/a/.git/config", "/a/.git/hooks", "/b/.git/config", "/b/.git/hooks"}
	if !slices.Equal(ro, want) {
		t.Errorf("read-only = %v\nwant       %v", ro, want)
	}
}

func TestGitProtectionEveryHooksPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk, write := gitTree(t, root)
	mk(".git/hooks")
	write(".git/config", "[core]\n\thooksPath = h1\n[includeIf \"gitdir:/elsewhere/\"]\n\tpath = other.gitconfig\n")
	write(".git/other.gitconfig", "[core]\n\thooksPath = h2\n")

	_, ro, err := gitProtection([]vm.Share{{HostPath: root}})
	if err != nil {
		t.Fatalf("gitProtection: %v", err)
	}
	for _, p := range []string{"h1", "h2", ".git/other.gitconfig"} {
		if !slices.Contains(ro, filepath.Join(root, p)) {
			t.Errorf("read-only = %v, want %s", ro, p)
		}
	}
}

func TestGitProtectionHookErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{
			name: "too many hooks",
			setup: func(t *testing.T, root string) {
				t.Helper()
				for i := range maxHookEntries + 1 {
					if err := os.WriteFile(filepath.Join(root, ".git/hooks", "h"+strconv.Itoa(i)), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "more than 1024 entries",
		},
		{
			name: "hook pointing at a missing file",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Symlink("../../scripts/pre-commit", filepath.Join(root, ".git/hooks/pre-commit")); err != nil {
					t.Fatal(err)
				}
			},
			want: "does not exist",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mk, write := gitTree(t, root)
			mk(".git/hooks")
			write(".git/config", "[core]\n")
			tt.setup(t, root)
			_, _, err := gitProtection([]vm.Share{{HostPath: root}})
			if err == nil {
				t.Fatal("want error")
			}
			assertContains(t, err.Error(), tt.want)
		})
	}
}

func TestGitProtectionCommondirLoop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "commondir pointing at itself",
			files: map[string]string{
				".git/worktrees/w/commondir": ".\n",
				"w/.git":                     "gitdir: ../.git/worktrees/w\n",
			},
		},
		{
			name: "commondirs pointing at each other",
			files: map[string]string{
				".git/worktrees/w1/commondir": "../w2\n",
				".git/worktrees/w2/commondir": "../w1\n",
				"w/.git":                      "gitdir: ../.git/worktrees/w1\n",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mk, write := gitTree(t, root)
			mk(".git/hooks")
			write(".git/config", "[core]\n")
			for rel, content := range tt.files {
				write(rel, content)
			}
			_, _, err := gitProtection([]vm.Share{{HostPath: root}})
			if err == nil {
				t.Fatal("want error")
			}
			assertContains(t, err.Error(), "commondir")
		})
	}
}

func TestParseGitConfigContinuation(t *testing.T) {
	t.Parallel()
	got := parseGitConfig([]byte("[core]\n\thooksPath = /a/\\\nb\n# note \\\n\teditor = vi\n"))
	want := []gitConfigEntry{
		{section: "core", key: "hookspath", value: "/a/b"},
		{section: "core", key: "editor", value: "vi"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parseGitConfig = %v, want %v", got, want)
	}
}
