//go:build linux

package guest

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/stacklok/go-microvm/guest/mount"
	"golang.org/x/sys/unix"
)

// UsernsHoldCommand is the jailoc-guest subcommand that keeps a user
// namespace alive until its stdin closes, so its idmapping can be attached
// to mounts.
const UsernsHoldCommand = "userns-hold"

// newRootDir is where the idmapped root filesystem is attached before
// pivot_root switches to it.
const newRootDir = Dir + "/newroot"

// idmapper attaches idmapped mounts for a host user that virtio-fs reports
// with its real IDs.
type idmapper struct {
	// shares maps the host user to the agent and root to root.
	shares *os.File
}

// close releases the user namespace.
func (m *idmapper) close() {
	if m != nil && m.shares != nil {
		_ = m.shares.Close()
	}
}

// idmapRoot replaces the root filesystem with an idmapped clone in which the
// host user's files belong to root, and attaches the agent's home with the
// host user mapped to the agent, so setuid programs such as sudo and the
// agent's own files keep their meaning. It returns an idmapper for the
// shares. Nothing changes when it fails.
func idmapRoot(logger *slog.Logger, m IDMap, home string, agentUID, agentGID uint32) (*idmapper, error) {
	if err := os.MkdirAll(home, 0o755); err != nil { //nolint:gosec // G301: home directories are world-readable
		return nil, fmt.Errorf("create %s: %w", home, err)
	}
	if err := os.MkdirAll(newRootDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", newRootDir, err)
	}
	rootNS, err := userNamespace([]syscall.SysProcIDMap{{ContainerID: int(m.UID), HostID: 0, Size: 1}},
		[]syscall.SysProcIDMap{{ContainerID: int(m.GID), HostID: 0, Size: 1}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rootNS.Close() }()
	sharesNS, err := userNamespace(
		[]syscall.SysProcIDMap{{ContainerID: int(m.UID), HostID: int(agentUID), Size: 1}, {ContainerID: 0, HostID: 0, Size: 1}},
		[]syscall.SysProcIDMap{{ContainerID: int(m.GID), HostID: int(agentGID), Size: 1}, {ContainerID: 0, HostID: 0, Size: 1}})
	if err != nil {
		return nil, err
	}
	mapper := &idmapper{shares: sharesNS}

	// pivot_root refuses a shared parent mount.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		mapper.close()
		return nil, fmt.Errorf("make / private: %w", err)
	}
	if err := attachIdmapped("/", newRootDir, rootNS); err != nil {
		mapper.close()
		return nil, fmt.Errorf("idmap root filesystem: %w", err)
	}
	if err := attachIdmapped(home, filepath.Join(newRootDir, home), sharesNS); err != nil {
		_ = unix.Unmount(newRootDir, unix.MNT_DETACH)
		mapper.close()
		return nil, fmt.Errorf("idmap %s: %w", home, err)
	}
	if err := switchRoot(newRootDir); err != nil {
		_ = unix.Unmount(newRootDir, unix.MNT_DETACH)
		mapper.close()
		return nil, err
	}
	logger.Info("idmapped root filesystem", "host_uid", m.UID, "host_gid", m.GID)
	return mapper, nil
}

// attachIdmapped clones the mount at src without its submounts, applies the
// idmapping of the user namespace ns and attaches it at dst.
func attachIdmapped(src, dst string, ns *os.File) error {
	fd, err := unix.OpenTree(unix.AT_FDCWD, src, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return fmt.Errorf("clone %s: %w", src, err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.MountSetattr(fd, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_IDMAP, Userns_fd: uint64(ns.Fd())}); err != nil {
		return fmt.Errorf("set idmapping: %w", err)
	}
	if err := unix.MoveMount(fd, "", unix.AT_FDCWD, dst, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach at %s: %w", dst, err)
	}
	return nil
}

// switchRoot makes dir the root filesystem and drops the old root with all
// its submounts.
func switchRoot(dir string) error {
	if err := unix.Chdir(dir); err != nil {
		return fmt.Errorf("enter new root: %w", err)
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		_ = unix.Chdir("/")
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("enter root: %w", err)
	}
	return nil
}

// mountShare mounts a virtio-fs share at target, idmapped when m is set.
func (m *idmapper) mountShare(tag, target string, readOnly bool) error {
	if m == nil {
		flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV)
		if readOnly {
			flags |= syscall.MS_RDONLY
		}
		return syscall.Mount(tag, target, "virtiofs", flags, "")
	}
	fs, err := unix.Fsopen("virtiofs", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("open virtiofs: %w", err)
	}
	defer func() { _ = unix.Close(fs) }()
	if err := unix.FsconfigSetString(fs, "source", tag); err != nil {
		return fmt.Errorf("set source: %w", err)
	}
	if err := unix.FsconfigCreate(fs); err != nil {
		return fmt.Errorf("create superblock: %w", err)
	}
	attrs := unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV
	if readOnly {
		attrs |= unix.MOUNT_ATTR_RDONLY
	}
	mnt, err := unix.Fsmount(fs, unix.FSMOUNT_CLOEXEC, attrs)
	if err != nil {
		return fmt.Errorf("create mount: %w", err)
	}
	defer func() { _ = unix.Close(mnt) }()
	// FUSE allows idmapped mounts only after the server's INIT reply, which
	// arrives asynchronously after the mount is created. A request on the
	// mount waits for INIT; retry briefly in case it is still in flight.
	var st unix.Stat_t
	_ = unix.Fstatat(mnt, "", &st, unix.AT_EMPTY_PATH)
	var err2 error
	for range 40 {
		err2 = unix.MountSetattr(mnt, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_IDMAP, Userns_fd: uint64(m.shares.Fd())})
		if !errors.Is(err2, unix.EINVAL) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err2 != nil {
		return fmt.Errorf("set idmapping: %w", err2)
	}
	if err := unix.MoveMount(mnt, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	return nil
}

// userNamespace creates a user namespace with the given ID mappings and
// returns a file that keeps it alive. A mapping's ContainerID is the ID on
// the filesystem and its HostID the ID the idmapped mount shows.
func userNamespace(uids, gids []syscall.SysProcIDMap) (*os.File, error) {
	cmd := exec.Command("/proc/self/exe", UsernsHoldCommand)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: uids,
		GidMappings: gids,
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("user namespace: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start user namespace: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	f, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("open user namespace: %w", err)
	}
	return f, nil
}

// HoldUserNamespace blocks until stdin closes.
func HoldUserNamespace(stdin io.Reader) error {
	_, err := io.Copy(io.Discard, stdin)
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

// remountAfterPivot recreates the mounts the old root carried: the
// essential filesystems, the ones libkrun's init adds (cgroup2, /dev/shm,
// /dev/fd) and a tmpfs on /var/tmp, because the agent cannot create files
// in the idmapped root filesystem.
func remountAfterPivot(logger *slog.Logger) error {
	if err := mount.Essential(logger, tmpSizeMiB); err != nil {
		return fmt.Errorf("essential mounts: %w", err)
	}
	for _, m := range []struct {
		source, target, fstype string
		flags                  uintptr
		data                   string
	}{
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", syscall.MS_NODEV | syscall.MS_NOEXEC | syscall.MS_NOSUID | syscall.MS_RELATIME, ""},
		{"tmpfs", "/dev/shm", "tmpfs", syscall.MS_NOEXEC | syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777"},
		{"tmpfs", "/var/tmp", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777"},
	} {
		if err := os.MkdirAll(m.target, 0o755); err != nil { //nolint:gosec // G301: system mount points
			return fmt.Errorf("create %s: %w", m.target, err)
		}
		if err := syscall.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil && !errors.Is(err, syscall.EBUSY) {
			return fmt.Errorf("mount %s: %w", m.target, err)
		}
	}
	if err := os.Symlink("/proc/self/fd", "/dev/fd"); err != nil && !errors.Is(err, os.ErrExist) {
		logger.Warn("create /dev/fd", "error", err)
	}
	return nil
}
