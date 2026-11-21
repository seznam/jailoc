//go:build linux

package guest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const tmpDir = "/tmp"

// tmpMountFlags apply to both backends: /tmp stays executable for build
// tools and test runners, and strictatime keeps access times exact for the
// expiry.
const tmpMountFlags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_STRICTATIME

// mountTmp replaces the bootstrap /tmp tmpfs of the essential mounts with the
// configured one. RAM storage remounts the tmpfs with its size; disk
// storage formats disk, the scratch disk, as ext4 and mounts it instead.
// Both get mode 01777.
func mountTmp(logger *slog.Logger, t TmpConfig, disk string) error {
	switch t.Storage {
	case TmpStorageDisk:
		if err := formatTmpDisk(disk); err != nil {
			return err
		}
		if err := unix.Unmount(tmpDir, 0); err != nil {
			return fmt.Errorf("tmp: unmount the bootstrap tmpfs: %w", err)
		}
		if err := unix.Mount(disk, tmpDir, "ext4", tmpMountFlags, "errors=remount-ro"); err != nil {
			return fmt.Errorf("tmp: mount %s: %w", disk, err)
		}
		// A scratch file system formatted at every boot never needs
		// fsck's lost+found, which would only clutter /tmp.
		if err := unix.Rmdir(filepath.Join(tmpDir, "lost+found")); err != nil && !errors.Is(err, unix.ENOENT) {
			logger.Warn("remove /tmp/lost+found", "error", err)
		}
	default:
		size := "size=" + strconv.FormatInt(t.SizeBytes, 10)
		if err := unix.Mount("tmpfs", tmpDir, "tmpfs", unix.MS_REMOUNT|tmpMountFlags, size); err != nil {
			return fmt.Errorf("tmp: remount tmpfs with %s: %w", size, err)
		}
	}
	// tmpfs ignores mode= on a remount, and mkfs leaves the root 0755.
	if err := unix.Chmod(tmpDir, 0o1777); err != nil {
		return fmt.Errorf("tmp: chmod %s: %w", tmpDir, err)
	}
	logger.Info("mounted /tmp", "storage", t.Storage, "size_bytes", t.SizeBytes, "ttl", t.TTL)
	return nil
}

// formatTmpDisk creates a fresh ext4 file system on disk. It needs
// mkfs.ext4 from the workspace image and installs nothing.
func formatTmpDisk(disk string) error {
	mkfs, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		return fmt.Errorf("tmp: storage %q needs mkfs.ext4 (e2fsprogs) in the workspace image: %w", TmpStorageDisk, err)
	}
	out, err := exec.Command(mkfs, tmpMkfsArgs(disk)...).CombinedOutput() //nolint:gosec // G204: mkfs.ext4 from PATH, device from the guest config
	if err != nil {
		return fmt.Errorf("tmp: format %s: %w: %s", disk, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TmpStatus writes the TmpUsage of /tmp to w as JSON.
func TmpStatus(w io.Writer) error {
	return writeTmpStatus(w, tmpDir)
}

func writeTmpStatus(w io.Writer, dir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("tmp-status: statfs %s: %w", dir, err)
	}
	// Block counts are in units of the fragment size.
	bsize := st.Frsize
	if bsize <= 0 {
		bsize = st.Bsize
	}
	if err := json.NewEncoder(w).Encode(tmpUsage(st.Blocks, st.Bfree, st.Bavail, uint64(max(bsize, 0)))); err != nil { //nolint:gosec // G115: clamped to zero
		return fmt.Errorf("tmp-status: %w", err)
	}
	return nil
}

// fstrimRange is struct fstrim_range of <linux/fs.h>.
type fstrimRange struct {
	start, length, minLen uint64
}

// fitrim is FITRIM, _IOWR('X', 121, struct fstrim_range), the same on every
// Linux architecture jailoc supports.
const fitrim = 0xc0185879

// trimFS discards the unused blocks of the file system fd is open on with
// FITRIM and returns the bytes the kernel reports as trimmed. It needs
// CAP_SYS_ADMIN and a block device that supports discard; otherwise the
// kernel fails it (EOPNOTSUPP, EPERM) and nothing falls back.
func trimFS(fd int) (uint64, error) {
	r := fstrimRange{length: math.MaxUint64}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fitrim, uintptr(unsafe.Pointer(&r))); errno != 0 { //nolint:gosec // G103: ioctl argument, valid for the call
		return 0, fmt.Errorf("FITRIM: %w", errno)
	}
	return r.length, nil
}
