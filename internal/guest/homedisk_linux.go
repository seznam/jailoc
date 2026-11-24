//go:build linux

package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/agentstate"
)

const (
	// homeMountFlags apply to the state and cache disks. They stay
	// executable: tools install binaries below ~/.cache.
	homeMountFlags = unix.MS_NOSUID | unix.MS_NODEV
	// homeEphemeralDir is the directory of the state disk emptied at every
	// boot of a workspace with an ephemeral agent state.
	homeEphemeralDir = "ephemeral"
	// homeCacheDir is the directory of the cache disk that holds ~/.cache.
	homeCacheDir = "cache"
	// homeMigratedMarker is the file in ControlDir that tells the host the
	// legacy home directories were copied to the state disk.
	homeMigratedMarker = "home-migrated"
	// migratingPrefix names the directory a legacy directory is copied to
	// before it is renamed into place.
	migratingPrefix = ".migrating-"
)

// homeDisks tracks the mounted state and cache disks of a workspace VM.
type homeDisks struct {
	logger *slog.Logger
	// dirs are the mount points of the disks, in mount order.
	dirs []string
	// binds are the targets of the binds whose sources are on the disks.
	binds []string

	cancel context.CancelFunc
	done   chan struct{}
}

// mountHomeDisks mounts and prepares the state disk (cfg.State) and the
// cache disk (cfg.Cache) that are configured, below PrivateDir, and starts
// trimming them. It runs after the internal shares are mounted (the legacy
// home directories are read from HostDir) and before the binds of the disks'
// directories are placed. The returned function stops the trimming, syncs and
// unmounts the disks; it is safe to call more than once.
func mountHomeDisks(logger *slog.Logger, cfg *Config, acct account) (func(), error) {
	h := &homeDisks{logger: logger}
	for _, b := range cfg.Binds {
		if within(b.Source, StateDiskDir) || within(b.Source, CacheDiskDir) {
			h.binds = append(h.binds, b.Target)
		}
	}
	if err := h.mountAndPrepare(cfg, acct); err != nil {
		h.stop()
		return nil, err
	}
	h.startTrim()
	return sync.OnceFunc(h.stop), nil
}

func (h *homeDisks) mountAndPrepare(cfg *Config, acct account) error {
	legacyHome := filepath.Join(HostDir, "home")
	st := cfg.State
	hasState := st != nil && st.Disk != ""
	if hasState {
		if err := mountExtDisk(h.logger, st.Disk, StateDiskDir, StateDiskLabel, "state disk"); err != nil {
			return err
		}
		h.dirs = append(h.dirs, StateDiskDir)
		if err := prepareStateDir(h.logger, StateDiskDir, legacyHome, st, acct); err != nil {
			return fmt.Errorf("state disk: %w", err)
		}
	}
	if c := cfg.Cache; c != nil && c.Disk != "" {
		if err := mountExtDisk(h.logger, c.Disk, CacheDiskDir, CacheDiskLabel, "cache disk"); err != nil {
			return err
		}
		h.dirs = append(h.dirs, CacheDiskDir)
		if err := prepareCacheDir(h.logger, CacheDiskDir, acct); err != nil {
			return fmt.Errorf("cache disk: %w", err)
		}
	}
	if hasState && !st.Ephemeral {
		if info, err := os.Lstat(legacyHome); err == nil && info.IsDir() {
			// The host archives the whole legacy home once it sees the
			// marker, so every directory of it has to be on the disk first.
			// A failure fails the boot and leaves the legacy home for the
			// next boot to try again.
			if err := migrateLegacyHome(StateDiskDir, legacyHome, acct); err != nil {
				return fmt.Errorf("migrate legacy home: %w", err)
			}
			if err := writeMigratedMarker(ControlDir); err != nil {
				h.logger.Warn("home migration marker", "error", err)
			}
		}
	}
	return nil
}

// migrateLegacyHome copies every directory of the legacy home directory
// legacyHomeRoot, except the cache, that the state disk at stateRoot does
// not have yet onto the disk. Entries that are not directories (symbolic
// links included) are skipped. The cache is not copied: it can be rebuilt
// and a large one would not copy within the boot deadline. Nothing under
// legacyHomeRoot is changed.
func migrateLegacyHome(stateRoot, legacyHomeRoot string, acct account) error {
	entries, err := os.ReadDir(legacyHomeRoot)
	if err != nil {
		return fmt.Errorf("read %s: %w", legacyHomeRoot, err)
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return fmt.Errorf("open %s: %w", stateRoot, err)
	}
	defer func() { _ = root.Close() }()
	for _, e := range entries {
		name := e.Name()
		if name == homeCacheDir || !e.IsDir() {
			continue
		}
		if !validStateName(name) || strings.HasPrefix(name, migratingPrefix) {
			continue
		}
		if err := migrateLegacyDir(root, stateRoot, name, filepath.Join(legacyHomeRoot, name), acct); err != nil {
			return fmt.Errorf("directory %s: %w", name, err)
		}
	}
	return nil
}

func writeMigratedMarker(controlDir string) error {
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", controlDir, err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, homeMigratedMarker), []byte("ok\n"), 0o600); err != nil {
		return fmt.Errorf("write migration marker: %w", err)
	}
	return nil
}

// startTrim trims the mounted disks once and then every tmpCleanInterval.
func (h *homeDisks) startTrim() {
	if len(h.dirs) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		trimLoop(ctx, h.logger, slices.Clone(h.dirs), tmpCleanInterval)
	}()
}

func trimLoop(ctx context.Context, logger *slog.Logger, dirs []string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for _, dir := range dirs {
			if ctx.Err() != nil {
				return
			}
			if err := trimDir(logger, dir); err != nil {
				logger.Warn("trim", "dir", dir, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// trimDir discards the unused blocks of the file system mounted at dir. A
// disk without discard support keeps its blocks allocated on the host.
func trimDir(logger *slog.Logger, dir string) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = unix.Close(fd) }()
	trimmed, err := trimFS(fd)
	if err != nil {
		return err
	}
	logger.Debug("trim", "dir", dir, "trimmed_bytes", trimmed)
	return nil
}

// stop ends the trimming, syncs and unmounts the disks. Binds of their
// directories are detached first, as they would keep the disks busy; a disk
// that is still busy is detached lazily after the sync.
func (h *homeDisks) stop() {
	if h.cancel != nil {
		h.cancel()
		<-h.done
		h.cancel = nil
	}
	if len(h.dirs) == 0 {
		return
	}
	syscall.Sync()
	for _, target := range slices.Backward(h.binds) {
		if err := unix.Unmount(target, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			h.logger.Warn("unmount home bind", "target", target, "error", err)
		}
	}
	for _, dir := range slices.Backward(h.dirs) {
		err := unix.Unmount(dir, 0)
		if errors.Is(err, unix.EBUSY) {
			err = unix.Unmount(dir, unix.MNT_DETACH)
		}
		if err != nil {
			h.logger.Warn("unmount disk", "dir", dir, "error", err)
		}
	}
	h.dirs = nil
}

// extDiskMkfsArgs are the mkfs.ext4 arguments for a persistent disk: quiet,
// no reserved blocks, the volume label, and no discard at format time.
func extDiskMkfsArgs(dev, label string) []string {
	return []string{"-q", "-F", "-m", "0", "-L", label, "-E", "nodiscard", dev}
}

// inspectExtFS reports whether dev carries an ext file system and its
// volume label.
func inspectExtFS(dev string) (bool, string, error) {
	f, err := os.Open(dev) //nolint:gosec // G304: device from the guest config
	if err != nil {
		return false, "", err
	}
	defer func() { _ = f.Close() }()
	return inspectExt(f)
}

func inspectExt(r io.ReaderAt) (bool, string, error) {
	formatted, err := extMagicAt(r)
	if err != nil || !formatted {
		return false, "", err
	}
	label, err := extVolumeLabelAt(r)
	if err != nil {
		return false, "", err
	}
	return true, label, nil
}

// mountExtDisk mounts the ext4 disk dev at target, formatting it with the
// volume label wantLabel on first use and growing the file system to the
// disk. A disk with another label is refused: it is not the disk jailoc
// attached.
func mountExtDisk(logger *slog.Logger, dev, target, wantLabel, name string) error {
	formatted, label, err := inspectExtFS(dev)
	if err != nil {
		return fmt.Errorf("%s: inspect %s: %w", name, dev, err)
	}
	if !formatted {
		mkfs, err := exec.LookPath("mkfs.ext4")
		if err != nil {
			return fmt.Errorf("%s: needs mkfs.ext4 (e2fsprogs) in the workspace image: %w", name, err)
		}
		logger.Info("formatting disk", "name", name, "device", dev)
		out, err := exec.Command(mkfs, extDiskMkfsArgs(dev, wantLabel)...).CombinedOutput() //nolint:gosec // G204: mkfs.ext4 from PATH, device from the guest config
		if err != nil {
			return fmt.Errorf("%s: format %s: %w: %s", name, dev, err, strings.TrimSpace(string(out)))
		}
	} else if label != "" && label != wantLabel {
		return fmt.Errorf("%s: %s has filesystem label %q, want %q", name, dev, label, wantLabel)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("%s: create %s: %w", name, target, err)
	}
	if err := unix.Mount(dev, target, "ext4", homeMountFlags, ""); err != nil {
		return fmt.Errorf("%s: mount %s: %w", name, dev, err)
	}
	// The host grows the disk image when its size is raised; grow the
	// mounted file system with it. A failure leaves the old size usable.
	if resize, err := exec.LookPath("resize2fs"); err == nil {
		if out, err := exec.Command(resize, dev).CombinedOutput(); err != nil { //nolint:gosec // G204: resize2fs from PATH, device from the guest config
			logger.Warn("disk not grown", "name", name, "device", dev, "err", err, "output", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// validStateName reports whether name is one path component.
func validStateName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

// prepareStateDir prepares the state directories of st below stateRoot, the
// mounted state disk: it empties the ephemeral directory first when st is
// ephemeral, copies a directory that exists only in the legacy home
// directory legacyHomeRoot onto the disk once, hands the directories to
// acct, and sets the defaults of the agent. Nothing under legacyHomeRoot is
// changed.
func prepareStateDir(logger *slog.Logger, stateRoot, legacyHomeRoot string, st *StateDisk, acct account) error {
	if st.Ephemeral {
		if err := os.RemoveAll(filepath.Join(stateRoot, homeEphemeralDir)); err != nil {
			return fmt.Errorf("empty ephemeral agent state: %w", err)
		}
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return fmt.Errorf("open %s: %w", stateRoot, err)
	}
	defer func() { _ = root.Close() }()
	h, hasHarness := agent.Lookup(st.Agent)
	for _, name := range st.Dirs {
		if !validStateName(name) {
			return fmt.Errorf("invalid state directory name %q", name)
		}
		rel := name
		if st.Ephemeral {
			rel = path.Join(homeEphemeralDir, name)
		} else if err := migrateLegacyDir(root, stateRoot, rel, filepath.Join(legacyHomeRoot, name), acct); err != nil {
			return fmt.Errorf("state directory %s: %w", name, err)
		}
		if err := ensureStateDir(root, stateRoot, rel, acct); err != nil {
			return fmt.Errorf("state directory %s: %w", name, err)
		}
		if !hasHarness {
			continue
		}
		i := slices.IndexFunc(h.State, func(d agent.StateDir) bool { return d.Name == name })
		if i < 0 {
			continue
		}
		d := h.State[i]
		if err := agentstate.RemoveConfigMountPoints(root, rel, d, h); err != nil {
			logger.Warn("agent state mount points", "error", err)
		}
		for _, werr := range agentstate.AddStateDefaults(root, rel, d, h) {
			logger.Warn("agent state default", "error", werr)
		}
		if err := agentstate.AddStatusLine(root, rel, h); err != nil {
			logger.Warn("agent status line", "error", err)
		}
		// The writers above run as root on a directory the agent owns.
		if err := ownAgentWrites(root, rel, d, h, acct); err != nil {
			return fmt.Errorf("state directory %s: %w", name, err)
		}
	}
	return nil
}

// ownAgentWrites gives acct the files that the agentstate writers create or
// replace in state directory d at rel inside root (the state defaults and
// the settings file with the status line) and the directories above them
// below rel. Entries acct owns are left alone, so a normal boot changes
// nothing, and only these paths are visited rather than the whole state.
// Symbolic links are never followed.
func ownAgentWrites(root *os.Root, rel string, d agent.StateDir, h agent.Harness, acct account) error {
	var inner []string
	for _, sd := range h.StateDefaults {
		if p, ok := strings.CutPrefix(sd.File, d.Path+"/"); ok {
			inner = append(inner, p)
		}
	}
	if file, _, ok := h.TUIPlugin.SettingsFile(); ok {
		inner = append(inner, file)
	}
	uid, gid, err := acct.chownIDs()
	if err != nil {
		return err
	}
	for _, p := range inner {
		if err := ownPath(root, rel, p, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// ownPath gives the uid and gid to every component of inner below rel inside
// root that exists and has another owner, stopping at a symbolic link.
func ownPath(root *os.Root, rel, inner string, uid, gid int) error {
	cur := rel
	for part := range strings.SplitSeq(inner, "/") {
		cur = path.Join(cur, part)
		info, err := root.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", cur, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int64(st.Uid) == int64(uid) && int64(st.Gid) == int64(gid) {
			continue
		}
		if err := root.Lchown(cur, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", cur, err)
		}
	}
	return nil
}

// prepareCacheDir prepares the cache directory below cacheRoot, the mounted
// cache disk. The legacy cache directory is not copied onto it: the cache
// can be rebuilt and a large one would not copy within the boot deadline.
func prepareCacheDir(_ *slog.Logger, cacheRoot string, acct account) error {
	if err := ownedDir(filepath.Join(cacheRoot, homeCacheDir), acct, 0o700); err != nil {
		return fmt.Errorf("cache directory: %w", err)
	}
	return nil
}

// migrateLegacyDir copies legacy, a directory of the legacy home directory,
// to rel on the disk at diskRoot when the disk has no rel yet and legacy is
// a directory.
func migrateLegacyDir(root *os.Root, diskRoot, rel, legacy string, acct account) error {
	if _, err := root.Lstat(rel); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("inspect %s: %w", rel, err)
		}
		return nil
	}
	info, err := os.Lstat(legacy)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", legacy, err)
	}
	return migrateDirToDisk(diskRoot, rel, legacy, acct)
}

// migrateDirToDisk copies the directory src to rel below diskRoot, owned by
// acct. The copy is made under a temporary name and renamed into place, so an
// interrupted copy never shows as the directory and the next boot starts it
// again. src is left untouched.
func migrateDirToDisk(diskRoot, rel, src string, acct account) error {
	tmp := filepath.Join(diskRoot, migratingPrefix+rel)
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("remove stale %s: %w", migratingPrefix+rel, err)
	}
	if err := copyTree(src, tmp, acct); err != nil {
		_ = os.RemoveAll(tmp) // best effort; the copy error is reported
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := os.Rename(tmp, filepath.Join(diskRoot, rel)); err != nil {
		_ = os.RemoveAll(tmp) // best effort; the rename error is reported
		return fmt.Errorf("move %s into place: %w", migratingPrefix+rel, err)
	}
	return nil
}

// copyTree copies the directory src to dst, which must not exist. It keeps
// directories, regular files, symbolic links (not followed), permissions and
// file times, and gives everything to acct. Other file types are skipped.
func copyTree(src, dst string, acct account) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			// The owner needs access to fill the directory.
			if err := os.Mkdir(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			if err := os.Chmod(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			return chown(target, acct)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil { //nolint:gosec // G122: target is in the fresh temporary copy; no session runs at boot
				return err
			}
			return chown(target, acct)
		case info.Mode().IsRegular():
			return copyStateFile(p, target, info, acct)
		default:
			return nil
		}
	})
}

func copyStateFile(src, dst string, info fs.FileInfo, acct account) (err error) {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0) //nolint:gosec // G304: legacy path below the internal share
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: path below the state disk
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	uid, gid, err := acct.chownIDs()
	if err != nil {
		return fmt.Errorf("chown %s: %w", dst, err)
	}
	if err := out.Chown(uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", dst, err)
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// ensureStateDir creates rel below the disk at diskRoot and makes acct its
// owner, recursively when the directory belonged to someone else.
func ensureStateDir(root *os.Root, diskRoot, rel string, acct account) error {
	if info, err := root.Lstat(rel); err == nil && !info.IsDir() {
		return fmt.Errorf("%s is not a directory", rel)
	}
	if err := root.MkdirAll(rel, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", rel, err)
	}
	return ensureOwned(filepath.Join(diskRoot, rel), acct)
}

// ensureOwned gives dir to acct. The tree below is walked only when dir
// itself had another owner, so a normal boot does not visit every file.
func ensureOwned(dir string, acct account) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && (st.Uid != acct.uid || st.Gid != acct.gid) {
		err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return chown(p, acct)
		})
		if err != nil {
			return err
		}
	}
	return chown(dir, acct)
}
