//go:build linux

package vm

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNormalizeOwnership(t *testing.T) {
	t.Parallel()

	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	other := -1
	for _, g := range groups {
		if g != os.Getgid() {
			other = g
			break
		}
	}
	if other < 0 {
		t.Skip("the user has no supplementary group")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "home", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: image directory fixture
		t.Fatal(err)
	}
	file := filepath.Join(dir, "tool")
	if err := os.WriteFile(file, nil, 0o755); err != nil { //nolint:gosec // G306: image executable fixture
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("tool", link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, file, link} {
		if err := os.Lchown(p, -1, other); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(file, 0o755|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}

	if err := normalizeOwnership(root); err != nil {
		t.Fatalf("normalizeOwnership: %v", err)
	}
	for _, p := range []string{dir, file, link} {
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		if int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
			t.Errorf("%s owned by %d:%d, want %d:%d", p, st.Uid, st.Gid, os.Getuid(), os.Getgid())
		}
	}
	if info, err := os.Stat(file); err != nil || info.Mode()&os.ModeSetgid == 0 {
		t.Errorf("setgid bit of %s lost: %v %v", file, info.Mode(), err)
	}
}
