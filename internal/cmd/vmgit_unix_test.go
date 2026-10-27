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

	pins, ro, err := gitProtection([]string{root})
	if err != nil {
		t.Fatalf("gitProtection: %v", err)
	}
	p := func(rel string) string { return filepath.Join(root, rel) }
	wantPins := []string{p(".git"), p("a"), p("a/b"), p("a/b/repo"), p("a/b/repo/.git"), p("sub")}
	wantRO := []string{
		p(".git/config"), p(".git/hooks"),
		p(".git/modules/sub/config"), p(".git/modules/sub/hooks"),
		p("a/b/repo/.git/config"), p("a/b/repo/.git/hooks"),
		p("sub/.git"),
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

func TestGitProtectionMissingRoot(t *testing.T) {
	t.Parallel()
	if _, _, err := gitProtection([]string{filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("want error for a missing workspace path")
	}
}
