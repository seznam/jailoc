//go:build linux

package vm

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// normalizeOwnership gives every entry under root the IDs of the current
// user. Unpacking an image as a non-root user keeps a tar entry's IDs only
// when the user may chown to them, for example the GID of a supplementary
// group, so a few entries end up with IDs the guest's idmapped root
// filesystem does not map; the guest then fails on them with EOVERFLOW.
// The other entries already belong to the user.
func normalizeOwnership(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("normalize ownership: %w", err)
	}
	defer func() { _ = root.Close() }()
	uid, gid := os.Getuid(), os.Getgid()
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (int(st.Uid) == uid && int(st.Gid) == gid) {
			return nil
		}
		if err := root.Lchown(path, uid, gid); err != nil {
			return err
		}
		// A successful chown clears setuid and setgid.
		if mode := info.Mode(); mode.IsRegular() && mode&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
			return root.Chmod(path, mode)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("normalize ownership in %s: %w", dir, err)
	}
	return nil
}
