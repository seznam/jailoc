package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	microvm "github.com/stacklok/go-microvm"
)

// tmpStorageDisk is guest.TmpConfig.Storage of a /tmp backed by a virtio-blk
// disk instead of guest memory.
const tmpStorageDisk = "disk"

// tmpDiskFile is the sparse image of a disk-backed /tmp in the state
// directory, next to docker.img and outside the guest-visible shares
// (GuestDataDir, GuestReadOnlyDir, user shares).
const tmpDiskFile = "tmp.img"

func tmpDiskPath(stateDir string) string { return filepath.Join(stateDir, tmpDiskFile) }
func tmpLockPath(stateDir string) string { return tmpDiskPath(stateDir) + ".lock" }
func tmpOnDisk(spec Spec) bool           { return spec.Guest != nil && spec.Guest.Tmp.Storage == tmpStorageDisk }
func tmpDiskDevice(spec Spec) microvm.Disk {
	return microvm.Disk{ID: "tmp", HostPath: tmpDiskPath(spec.StateDir)}
}

// tmpDiskSize returns the length of the tmp disk: the size the host set in
// the spec, else the one in the guest configuration.
func tmpDiskSize(spec Spec) int64 {
	if spec.TmpDiskSize > 0 {
		return spec.TmpDiskSize
	}
	if spec.Guest != nil {
		return spec.Guest.Tmp.SizeBytes
	}
	return 0
}

// workspaceDisks lists the virtio-blk disks of a workspace VM in attach
// order, which is the order of the guest's /dev/vdX names (go-microvm and the
// runner keep the order of WithDisks): the Docker disk first, so it stays
// guest.DockerDisk (/dev/vda), then the tmp disk (/dev/vdb, or /dev/vda when
// the workspace has no Docker disk).
func workspaceDisks(spec Spec) []microvm.Disk {
	var disks []microvm.Disk
	if spec.Docker {
		disks = append(disks, microvm.Disk{ID: "docker", HostPath: diskPath(spec.StateDir)})
	}
	if tmpOnDisk(spec) {
		disks = append(disks, tmpDiskDevice(spec))
	}
	return disks
}

// PrepareTmpDisk creates the fresh sparse image of a workspace VM's tmp disk,
// exactly size bytes, and returns release, which removes it. The disk is
// scratch space: every VM boot starts from a new empty file, and an old image
// is replaced, never reused, truncated or grown (unlike the Docker disk, see
// ensureDisk), so no data and no previous size survive a boot.
//
// An exclusive flock on a lock file next to the image, held until release,
// makes the disk exclusive to one VM lifetime: a second caller fails with an
// "in use" error before it touches the image. The lock is dropped when the
// process dies, so a crashed supervisor's leftover image is replaced on the
// next boot; a VM runner that outlives it keeps its own (unlinked) inode.
//
// The image is created 0600 and, as the replaced path is unlinked first and
// then created with O_EXCL and O_NOFOLLOW, a planted symlink is never
// followed. The file is sparse: size is a cap, not a reservation, so the host
// filesystem can run out of space under a guest that fills it. The guest's
// ext4 offers less than size to its users (journal, metadata, reserved
// blocks).
//
// release is safe to call more than once. Call it after the VM runner has
// exited, on every path that follows a successful PrepareTmpDisk.
func PrepareTmpDisk(stateDir string, size int64) (release func() error, err error) {
	if !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("tmp disk: state directory %q is not absolute", stateDir)
	}
	if size <= 0 {
		return nil, fmt.Errorf("tmp disk: size must be positive, got %d", size)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create vm state dir: %w", err)
	}
	lock, err := os.OpenFile(tmpLockPath(stateDir), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("open tmp disk lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: file descriptors fit in int
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("tmp disk %s is in use by another jailoc process", tmpDiskPath(stateDir))
		}
		return nil, fmt.Errorf("lock tmp disk: %w", err)
	}
	path := tmpDiskPath(stateDir)
	if err := createFreshDisk(path, size); err != nil {
		_ = os.Remove(path)
		_ = lock.Close()
		return nil, err
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				releaseErr = fmt.Errorf("remove tmp disk: %w", err)
			}
			_ = lock.Close() // drops the flock after the image is gone
		})
		return releaseErr
	}, nil
}

// createFreshDisk replaces whatever is at path with a new sparse file of
// exactly size bytes.
func createFreshDisk(path string, size int64) error {
	if st, err := os.Lstat(path); err == nil {
		if st.IsDir() {
			return fmt.Errorf("tmp disk path %s is a directory", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove old tmp disk: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat tmp disk: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("create tmp disk: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("restrict tmp disk: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return fmt.Errorf("size tmp disk: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp disk: %w", err)
	}
	return nil
}

// hostFreeBytes returns the space available to unprivileged users on the
// filesystem holding dir.
func hostFreeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // G115: block size is positive
}

// tmpPressure returns a warning when the host filesystem has less free space
// than the tmp disk can grow to, or "".
func tmpPressure(free uint64, size int64) string {
	if size <= 0 || free >= uint64(size) {
		return ""
	}
	return fmt.Sprintf("tmp disk may grow to %d MiB but the host filesystem has only %d MiB free; the disk is sparse and its size is a cap, not a reservation, so a full host fails guest writes with I/O errors",
		size>>20, free>>20)
}

// prepareWorkspaceTmp prepares the tmp disk of a workspace VM that has one
// and warns through logf (which may be nil) when the host runs short of
// space for it. The returned release is never nil.
func prepareWorkspaceTmp(spec Spec, logf func(string, ...any), free func(string) (uint64, error)) (func() error, error) {
	if !tmpOnDisk(spec) {
		return func() error { return nil }, nil
	}
	size := tmpDiskSize(spec)
	if size <= 0 {
		return nil, errors.New("tmp disk has no size")
	}
	release, err := PrepareTmpDisk(spec.StateDir, size)
	if err != nil {
		return nil, err
	}
	if logf == nil {
		return release, nil
	}
	avail, err := free(spec.StateDir)
	if err != nil {
		logf("tmp disk: cannot check host free space: %v", err)
		return release, nil
	}
	if msg := tmpPressure(avail, size); msg != "" {
		logf("%s", msg)
	}
	return release, nil
}
