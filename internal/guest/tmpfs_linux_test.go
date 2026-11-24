//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMountTmpfsDirs(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	acct := account{
		name: "agent",
		uid:  uint32(os.Getuid()), //nolint:gosec // G115: uid fits in uint32
		gid:  uint32(os.Getgid()), //nolint:gosec // G115: gid fits in uint32
		home: home,
	}

	child := filepath.Join(home, ".omo", "agent", "rpc", "tui")
	parent := filepath.Join(home, ".omo", "agent", "rpc")

	var gotTargets []string
	var gotOpts []string
	err := mountTmpfsDirsWith([]string{child, parent}, acct, func(target, opts string) error {
		gotTargets = append(gotTargets, target)
		gotOpts = append(gotOpts, opts)
		return nil
	})
	if err != nil {
		t.Fatalf("mountTmpfsDirsWith: %v", err)
	}

	wantTargets := []string{parent, child}
	if !slices.Equal(gotTargets, wantTargets) {
		t.Fatalf("mounted targets = %q, want %q", gotTargets, wantTargets)
	}
	wantOpts := fmt.Sprintf("mode=0700,uid=%d,gid=%d,size=%s", acct.uid, acct.gid, tmpfsDirSize)
	for i, opts := range gotOpts {
		if opts != wantOpts {
			t.Errorf("mount[%d] opts = %q, want %q", i, opts, wantOpts)
		}
	}
	for _, dir := range wantTargets {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("mount point %s: err=%v info=%v, want directory", dir, err, info)
		}
	}
}

func TestMountTmpfsDirsValidation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	acct := account{
		name: "agent",
		uid:  uint32(os.Getuid()), //nolint:gosec // G115: uid fits in uint32
		gid:  uint32(os.Getgid()), //nolint:gosec // G115: gid fits in uint32
		home: home,
	}

	link := filepath.Join(home, "rpc-link")
	if err := os.Symlink(filepath.Join(home, "target"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	file := filepath.Join(home, "rpc-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		name    string
		dir     string
		wantErr string
	}{
		{"relative path", "relative/rpc", "target must be an absolute path below /"},
		{"root path", "/", "target must be an absolute path below /"},
		{"symlink target", link, "different file type"},
		{"regular file target", file, "different file type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := mountTmpfsDirsWith([]string{tt.dir}, acct, func(_, _ string) error {
				t.Fatal("mountFn should not be called on invalid target")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("mountTmpfsDirsWith(%q) = %v, want error containing %q", tt.dir, err, tt.wantErr)
			}
		})
	}
}
