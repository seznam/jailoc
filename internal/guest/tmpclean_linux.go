//go:build linux

package guest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// tmpMaxDepth bounds how deep a pass descends, and so the directory
	// descriptors it holds; deeper entries are kept.
	tmpMaxDepth = 128
	// direntBufSize is the getdents buffer of each open directory, which
	// bounds the memory a pass uses on huge directories.
	direntBufSize = 8 << 10
	// tmpWalkBatch is how many entries a walk removes before it rewinds the
	// directory to catch entries a removal may have shifted past the cursor.
	tmpWalkBatch = 512
	// tmpWalkSeen bounds the retained names a walk records for a directory,
	// and so its memory, at a few megabytes; once full, the walk finishes
	// its current linear scan without rewinding so entries later in the
	// directory are still visited.
	tmpWalkSeen = 1 << 18
)

// tmpCleanInterval is the period of the /tmp expiry and trim passes.
// Overridden to a shorter interval in vmintegration builds.
var tmpCleanInterval = 15 * time.Minute

// tmpResolve keeps every lookup of a pass on the /tmp mount: below the
// directory it starts from, without symbolic links and without crossing
// into any other mount, bind mounts of the same file system included.
const tmpResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV

// tmpStatxMask are the statx fields a pass compares.
const tmpStatxMask = unix.STATX_TYPE | unix.STATX_INO | unix.STATX_ATIME | unix.STATX_MTIME | unix.STATX_CTIME | unix.STATX_MNT_ID

// tmpCleaner expires old entries of /tmp and, on disk storage, trims the
// file system so the host can reclaim the freed blocks. One worker runs a
// pass every tmpCleanInterval, so passes never overlap.
//
// A pass removes only regular files and directories, and skips everything
// it cannot check safely: sockets, symbolic links, devices and FIFOs, which
// live programs use; entries another process holds an flock on; entries on
// other mounts; entries it cannot open without touching their access time.
// Each regular file and directory is opened by descriptor relative to its
// parent, flocked exclusively without blocking and aged, then checked again
// by name under that lock, so an entry accessed, modified or changed after
// the first check is kept. A directory whose sweep removed something is
// kept: its modification time is then fresh, and the pass cannot tell that
// from a concurrent change. The checks are best effort: a file renamed into
// the checked name between the last check and the unlink is removed in its
// place, and a program that keeps a file open without an flock does not
// protect it. An unlinked open file stays readable through its descriptor.
type tmpCleaner struct {
	logger *slog.Logger
	// root is an O_PATH descriptor of the /tmp mount root, so passes keep
	// working on that mount even if another is stacked on /tmp.
	root  int
	mntID uint64
	dev   [2]uint32
	trim  bool
	// walkSeenLimit overrides tmpWalkSeen in tests when positive.
	walkSeenLimit int
	// sweepMu serializes sweepTTL calls.
	sweepMu sync.Mutex

	mu     sync.Mutex
	ttl    time.Duration
	closed bool
	// cancel stops the active sweep, which closes swept when it ends.
	cancel context.CancelFunc
	swept  chan struct{}

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// tmpSweep is the result of a sweep. failed counts the unexpected errors,
// of which the first is in err.
type tmpSweep struct {
	removed  int
	canceled bool
	failed   int
	err      error
}

// fail records an unexpected error of the pass.
func (r *tmpSweep) fail(err error) {
	if err == nil || tmpExpectedErr(err) {
		return
	}
	if r.failed == 0 {
		r.err = err
	}
	r.failed++
}

// tmpExpectedErr reports whether err is a normal race of a sweep rather
// than a failure worth reporting: the entry is gone, is of another type,
// crossed a mount or a symbolic link, changed its owner, or is held by
// another process.
func tmpExpectedErr(err error) bool {
	for _, target := range []error{
		syscall.ENOENT, syscall.EXDEV, syscall.ELOOP, syscall.ENOTDIR, syscall.EISDIR,
		syscall.EAGAIN, syscall.ENOTEMPTY, syscall.EEXIST, syscall.ENXIO, syscall.EINTR,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// newTmpCleaner pins the mount root dir and starts the worker. trim enables
// the FITRIM of every pass, independently of ttl.
func newTmpCleaner(logger *slog.Logger, dir string, ttl time.Duration, trim bool) (*tmpCleaner, error) {
	root, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("tmp: open %s: %w", dir, err)
	}
	st, err := statxFD(root)
	if err != nil {
		_ = unix.Close(root)
		return nil, fmt.Errorf("tmp: stat %s: %w", dir, err)
	}
	c := &tmpCleaner{
		logger: logger,
		root:   root,
		mntID:  st.Mnt_id,
		dev:    [2]uint32{st.Dev_major, st.Dev_minor},
		trim:   trim,
		ttl:    max(ttl, 0),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go c.run()
	return c, nil
}

func (c *tmpCleaner) run() {
	defer close(c.done)
	ticker := time.NewTicker(tmpCleanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
		}
		if res := c.sweepTTL(time.Now()); res.removed > 0 || res.canceled {
			c.logger.Info("tmp expiry", "removed", res.removed, "canceled", res.canceled)
		}
		select {
		case <-c.stop:
			return
		default:
		}
		if c.trim {
			c.trimPass()
		}
	}
}

// SetTTL replaces the expiry; zero or less disables it. A sweep running
// with the old value is stopped before SetTTL returns, so it removes
// nothing more.
func (c *tmpCleaner) SetTTL(ttl time.Duration) {
	if c == nil {
		return
	}
	ttl = max(ttl, 0)
	c.mu.Lock()
	if ttl == c.ttl {
		c.mu.Unlock()
		return
	}
	c.ttl = ttl
	cancel, swept := c.cancel, c.swept
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-swept
	}
}

// close stops the worker, waiting for its pass, and releases the root.
func (c *tmpCleaner) close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		cancel, swept := c.cancel, c.swept
		c.mu.Unlock()
		close(c.stop)
		if cancel != nil {
			cancel()
			<-swept
		}
		<-c.done
		_ = unix.Close(c.root)
	})
}

// sweepTTL runs a sweep with the current TTL as of now, unless it is
// disabled; calls never overlap. SetTTL and close cancel it.
func (c *tmpCleaner) sweepTTL(now time.Time) tmpSweep {
	c.sweepMu.Lock()
	defer c.sweepMu.Unlock()
	c.mu.Lock()
	if c.closed || c.ttl <= 0 {
		c.mu.Unlock()
		return tmpSweep{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	swept := make(chan struct{})
	c.cancel, c.swept = cancel, swept
	cutoff := now.Add(-c.ttl)
	c.mu.Unlock()
	res := c.sweep(ctx, cutoff)
	c.mu.Lock()
	c.cancel, c.swept = nil, nil
	c.mu.Unlock()
	cancel()
	close(swept)
	return res
}

// trimPass trims the disk-backed /tmp. Failures are logged: a disk without
// discard support keeps its blocks allocated on the host.
func (c *tmpCleaner) trimPass() {
	fd, err := unix.Openat2(c.root, ".", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOATIME | unix.O_CLOEXEC, Resolve: tmpResolve})
	if err != nil {
		c.logger.Warn("tmp trim", "error", fmt.Errorf("open /tmp: %w", err))
		return
	}
	defer func() { _ = unix.Close(fd) }()
	trimmed, err := trimFS(fd)
	if err != nil {
		c.logger.Warn("tmp trim", "error", err)
		return
	}
	c.logger.Debug("tmp trim", "trimmed_bytes", trimmed)
}

// sweep removes the regular files older than cutoff and the directories
// that are empty and older than cutoff below the root.
func (c *tmpCleaner) sweep(ctx context.Context, cutoff time.Time) tmpSweep {
	var res tmpSweep
	fd, err := unix.Openat2(c.root, ".", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOATIME | unix.O_CLOEXEC, Resolve: tmpResolve})
	if err != nil {
		res.fail(fmt.Errorf("open /tmp: %w", err))
		c.report(&res)
		return res
	}
	defer func() { _ = unix.Close(fd) }()
	c.walk(ctx, fd, 0, cutoff, &res)
	res.canceled = ctx.Err() != nil
	c.report(&res)
	return res
}

// report logs the unexpected errors of a pass, once with their count. A
// pass that only met normal races stays quiet.
func (c *tmpCleaner) report(res *tmpSweep) {
	if res.failed > 0 {
		c.logger.Warn("tmp expiry", "failures", res.failed, "error", res.err)
	}
}

// walk processes the entries of the directory dir at depth and returns how
// many of them it removed. Removing an entry while listings are in progress
// can shift the positions getdents reports on some file systems, so after a
// walk processes the entries of the directory dir at depth and returns how
// many of them it removed. Removing an entry while listings are in progress
// can shift the positions getdents reports on some file systems, so after a
// round removes entries it rewinds and skips the retained names it already
// inspected, until cumulative rescan work reaches walkSeenLimit. Once no
// entries are removed or that budget is reached, the listing finishes at EOF
// without rewinding, avoiding quadratic rescans and preventing a retained
// prefix from starving later entries.
func (c *tmpCleaner) walk(ctx context.Context, dir, depth int, cutoff time.Time, res *tmpSweep) int {
	limit := c.walkSeenLimit
	if limit <= 0 {
		limit = tmpWalkSeen
	}
	removed, rescanned := 0, 0
	var seen map[string]struct{}
	buf := make([]byte, direntBufSize)
	for ctx.Err() == nil {
		roundRemoved, eof, capped := 0, false, rescanned >= limit
		for (capped || roundRemoved < tmpWalkBatch) && ctx.Err() == nil {
			n, err := unix.Getdents(dir, buf)
			if err != nil {
				res.fail(fmt.Errorf("read directory: %w", err))
				return removed
			}
			if n <= 0 {
				eof = true
				break
			}
			for rec := buf[:n]; len(rec) > 0 && (capped || roundRemoved < tmpWalkBatch) && ctx.Err() == nil; {
				name, typ, next, ok := parseDirent(rec)
				if !ok {
					eof = true
					break
				}
				rec = next
				if name == "." || name == ".." {
					continue
				}
				if _, ok := seen[name]; ok {
					rescanned++
					if rescanned >= limit {
						capped = true
					}
					continue
				}
				if typ == unix.DT_UNKNOWN {
					typ = c.direntType(dir, name)
				}
				var gone bool
				switch typ {
				case unix.DT_REG:
					gone = c.removeFile(ctx, dir, name, cutoff, res)
				case unix.DT_DIR:
					gone = c.sweepDir(ctx, dir, name, depth+1, cutoff, res)
				}
				if gone {
					removed++
					roundRemoved++
					res.removed++
					continue
				}
				if len(seen) < limit && !capped {
					if seen == nil {
						seen = make(map[string]struct{}, min(limit, tmpWalkBatch))
					}
					seen[name] = struct{}{}
				} else {
					capped = true
				}
			}
			if eof {
				break
			}
		}
		if roundRemoved == 0 || capped || ctx.Err() != nil {
			return removed
		}
		if _, err := unix.Seek(dir, 0, 0); err != nil {
			res.fail(fmt.Errorf("rewind directory: %w", err))
			return removed
		}
	}
	return removed
}

// direntType returns the d_type of name in dir for file systems that do not
// report it.
func (c *tmpCleaner) direntType(dir int, name string) uint8 {
	st, err := statxAt(dir, name)
	if err != nil {
		return unix.DT_UNKNOWN
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return unix.DT_REG
	case unix.S_IFDIR:
		return unix.DT_DIR
	}
	return unix.DT_UNKNOWN
}

// removeFile removes the regular file name in dir when it has expired and
// reports whether it did.
func (c *tmpCleaner) removeFile(ctx context.Context, dir int, name string, cutoff time.Time, res *tmpSweep) bool {
	// O_NONBLOCK: an entry swapped for a FIFO since the listing must not
	// block the open. No fallback without O_NOATIME, which would age
	// nothing here but must never refresh what it checks.
	fd, err := unix.Openat2(dir, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_NOATIME | unix.O_CLOEXEC, Resolve: tmpResolve})
	if err != nil {
		if !tmpExpectedErr(err) {
			res.fail(fmt.Errorf("open %s: %w", name, err))
		}
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return false
	}
	// Stat after the lock, so activity up to the lock counts.
	st, err := statxFD(fd)
	if err != nil {
		res.fail(fmt.Errorf("stat %s: %w", name, err))
		return false
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || !c.onMount(st) || !tmpExpired(cutoff, statxTime(st.Atime), statxTime(st.Mtime), statxTime(st.Ctime)) {
		return false
	}
	// The last check, under the lock: the name still refers to the aged,
	// expired inode, so activity since the first check keeps it.
	if _, ok := c.recheck(dir, name, st, cutoff); !ok || ctx.Err() != nil {
		return false
	}
	if err := unix.Unlinkat(dir, name, 0); err != nil {
		res.fail(fmt.Errorf("remove %s: %w", name, err))
		return false
	}
	return true
}

// sweepDir sweeps the directory name in dir, then removes it when it is
// empty and expired, and reports whether it did. Its times from before the
// sweep decide, through the same last check as a file: anything the pass
// itself removed set a fresh modification time, which is indistinguishable
// from a concurrent change, so such a directory goes in a later pass.
func (c *tmpCleaner) sweepDir(ctx context.Context, dir int, name string, depth int, cutoff time.Time, res *tmpSweep) bool {
	if depth > tmpMaxDepth {
		return false
	}
	fd, err := unix.Openat2(dir, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_NOATIME | unix.O_CLOEXEC, Resolve: tmpResolve})
	if err != nil {
		if !tmpExpectedErr(err) {
			res.fail(fmt.Errorf("open %s: %w", name, err))
		}
		return false
	}
	defer func() { _ = unix.Close(fd) }()
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return false
	}
	before, err := statxFD(fd)
	if err != nil {
		res.fail(fmt.Errorf("stat %s: %w", name, err))
		return false
	}
	if before.Mode&unix.S_IFMT != unix.S_IFDIR || !c.onMount(before) {
		return false
	}
	c.walk(ctx, fd, depth, cutoff, res)
	if !tmpExpired(cutoff, statxTime(before.Atime), statxTime(before.Mtime)) {
		return false
	}
	after, err := statxFD(fd)
	if err != nil {
		res.fail(fmt.Errorf("stat %s: %w", name, err))
		return false
	}
	if _, ok := c.recheck(dir, name, after, cutoff); !ok || after != before || ctx.Err() != nil {
		return false
	}
	// Fails with ENOTEMPTY while anything, fresh or kept, is left.
	if err := unix.Unlinkat(dir, name, unix.AT_REMOVEDIR); err != nil {
		if !errors.Is(err, syscall.ENOTEMPTY) {
			res.fail(fmt.Errorf("remove %s: %w", name, err))
		}
		return false
	}
	return true
}

// onMount reports whether st belongs to the pinned /tmp mount.
func (c *tmpCleaner) onMount(st unix.Statx_t) bool {
	return st.Mask&unix.STATX_MNT_ID != 0 && st.Mnt_id == c.mntID && st.Dev_major == c.dev[0] && st.Dev_minor == c.dev[1]
}

// recheck is the last check before an unlink: name in dir still refers to
// the inode of st, with the same type, on the same mount, and with the
// times it had when st was taken. Any fresh access, modification or change
// since st fails it, and so does the expiry of the entry as it is now
// (ignoring ctime for directories, whose age depends only on atime and
// mtime). It returns the current stat.
func (c *tmpCleaner) recheck(dir int, name string, st unix.Statx_t, cutoff time.Time) (unix.Statx_t, bool) {
	cur, err := statxAt(dir, name)
	if err != nil || cur.Ino != st.Ino || cur.Mode&unix.S_IFMT != st.Mode&unix.S_IFMT || !c.onMount(cur) {
		return cur, false
	}
	if cur.Atime != st.Atime || cur.Mtime != st.Mtime || cur.Ctime != st.Ctime {
		return cur, false
	}
	if cur.Mode&unix.S_IFMT == unix.S_IFDIR {
		return cur, tmpExpired(cutoff, statxTime(cur.Atime), statxTime(cur.Mtime))
	}
	return cur, tmpExpired(cutoff, statxTime(cur.Atime), statxTime(cur.Mtime), statxTime(cur.Ctime))
}

// tmpExpired reports whether every time is strictly older than cutoff.
func tmpExpired(cutoff time.Time, times ...time.Time) bool {
	for _, t := range times {
		if !t.Before(cutoff) {
			return false
		}
	}
	return true
}

func statxFD(fd int) (unix.Statx_t, error) {
	var st unix.Statx_t
	err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_STATX_SYNC_AS_STAT, tmpStatxMask, &st)
	return st, err
}

// statxAt stats name in dir without following a final symbolic link.
func statxAt(dir int, name string) (unix.Statx_t, error) {
	var st unix.Statx_t
	err := unix.Statx(dir, name, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT|unix.AT_STATX_SYNC_AS_STAT, tmpStatxMask, &st)
	return st, err
}

func statxTime(ts unix.StatxTimestamp) time.Time {
	return time.Unix(ts.Sec, int64(ts.Nsec))
}

// parseDirent splits the first linux_dirent64 record off buf.
func parseDirent(buf []byte) (name string, typ uint8, rest []byte, ok bool) {
	const nameOff = 19 // d_ino (8), d_off (8), d_reclen (2), d_type (1)
	if len(buf) < nameOff {
		return "", 0, nil, false
	}
	reclen := int(binary.NativeEndian.Uint16(buf[16:18]))
	if reclen < nameOff || reclen > len(buf) {
		return "", 0, nil, false
	}
	raw := buf[nameOff:reclen]
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}
	return string(raw), buf[18], buf[reclen:], true
}
