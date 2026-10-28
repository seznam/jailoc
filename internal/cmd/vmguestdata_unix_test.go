//go:build darwin || linux

package cmd

import (
	"os"
	"path/filepath"
	"testing"
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
