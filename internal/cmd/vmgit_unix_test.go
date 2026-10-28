//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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

	pins, ro, err := gitProtection([]string{root})
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

	pins, ro, err := gitProtection([]string{root})
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

func TestGitProtectionMissingRoot(t *testing.T) {
	t.Parallel()
	if _, _, err := gitProtection([]string{filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("want error for a missing workspace path")
	}
}
