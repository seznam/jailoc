//go:build linux

package guest

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMountPointRejectsSpecialFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		path    string
		dir     bool
		wantErr bool
	}{
		{"regular file", file, false, false},
		{"missing file", filepath.Join(dir, "new"), false, false},
		{"directory", dir, true, false},
		{"symbolic link", link, false, true},
		{"fifo", fifo, false, true},
		{"file for directory", file, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := mountPoint(tt.path, tt.dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("mountPoint(%s) error = %v, want error %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

// TestBindReadOnlyUnmountsOwnMount needs mount privileges; run it with
// unshare -Urm go test ./internal/guest -run BindReadOnly.
func TestBindReadOnlyUnmountsOwnMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs mount privileges (run under unshare -Urm)")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	// The snapshot lives at parent/target; after the bind the parent is
	// moved away and replaced by a link to decoy, whose target is another
	// mount that a path-based unmount would detach instead.
	parent := filepath.Join(dir, "parent")
	decoy := filepath.Join(dir, "decoy")
	for _, d := range []string{src, parent, decoy, filepath.Join(parent, "target"), filepath.Join(decoy, "target")} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherFD, err := bindReadOnly(src, filepath.Join(decoy, "target"), true)
	if err != nil {
		t.Skipf("mount API unavailable: %v", err)
	}
	t.Cleanup(func() { _ = unmountSnapshot(otherFD) })

	target := filepath.Join(parent, "target")
	fd, err := bindReadOnly(src, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "g"), nil, 0o600); err == nil {
		t.Fatal("write into read-only snapshot succeeded")
	}
	if err := os.Rename(parent, parent+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, parent); err != nil {
		t.Fatal(err)
	}
	if err := unmountSnapshot(fd); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(decoy, "target", "f")) //nolint:gosec // test path
	if err != nil || string(data) != "snapshot" {
		t.Fatalf("decoy mount was detached: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(parent+".moved", "target", "f")); err == nil {
		t.Fatal("moved snapshot mount is still attached")
	}

	if _, err := bindReadOnly(src, filepath.Join(dir, "missing"), true); err == nil {
		t.Fatal("bind onto a missing destination succeeded")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(decoy, link); err != nil {
		t.Fatal(err)
	}
	if _, err := bindReadOnly(src, link, true); err == nil {
		t.Fatal("bind onto a symbolic link succeeded")
	}
}
