//go:build linux

package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func newTestCleaner(t *testing.T, dir string, ttl time.Duration) *tmpCleaner {
	t.Helper()
	c, err := newTmpCleaner(slog.New(slog.DiscardHandler), dir, ttl, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	return c
}

// newLoggedCleaner is newTestCleaner with its warnings captured.
func newLoggedCleaner(t *testing.T, dir string, ttl time.Duration) (*tmpCleaner, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	c, err := newTmpCleaner(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})), dir, ttl, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	return c, &log
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// lock holds an exclusive flock on path, as a live program would, until the
// test ends.
func lock(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
}

// sweepRoot returns an empty directory on a file system that, like the
// guest's tmpfs and ext4, keeps access times in the kernel and so honours
// O_NOATIME. FUSE file systems such as virtiofs update them on the server.
func sweepRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	if st.Type != unix.FUSE_SUPER_MAGIC {
		return dir
	}
	if err := unix.Statfs("/dev/shm", &st); err != nil || st.Type != unix.TMPFS_MAGIC {
		t.Skip("no local file system for the /tmp sweep tests")
	}
	dir, err := os.MkdirTemp("/dev/shm", "jailoc-tmp-test-")
	if err != nil {
		t.Skipf("no local file system for the /tmp sweep tests: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// ageCutoff returns a cutoff, in the file system's own clock, that
// everything created in root so far is strictly older than and everything
// created afterwards is not.
func ageCutoff(t *testing.T, root string) time.Time {
	t.Helper()
	time.Sleep(20 * time.Millisecond)
	marker := filepath.Join(root, ".marker")
	writeFile(t, marker)
	var st unix.Stat_t
	if err := unix.Stat(marker, &st); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	return time.Unix(st.Mtim.Unix())
}

func atime(t *testing.T, path string) time.Time {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return time.Unix(st.Atim.Unix())
}

func TestTmpExpired(t *testing.T) {
	t.Parallel()
	cutoff := time.Unix(1000, 0)
	old, fresh := cutoff.Add(-time.Nanosecond), cutoff.Add(time.Nanosecond)
	tests := []struct {
		name  string
		times []time.Time
		want  bool
	}{
		{name: "all older", times: []time.Time{old, old, old}, want: true},
		{name: "fresh atime", times: []time.Time{fresh, old, old}},
		{name: "fresh mtime", times: []time.Time{old, fresh, old}},
		{name: "fresh ctime", times: []time.Time{old, old, fresh}},
		{name: "equal to cutoff", times: []time.Time{old, cutoff, old}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tmpExpired(cutoff, tt.times...); got != tt.want {
				t.Fatalf("tmpExpired = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTmpSweep(t *testing.T) {
	t.Parallel()
	root, outside := sweepRoot(t), t.TempDir()
	writeFile(t, filepath.Join(root, "old.txt"))
	mkdir(t, filepath.Join(root, "olddir"))
	writeFile(t, filepath.Join(root, "nested", "a", "b", "file"))
	writeFile(t, filepath.Join(root, "mixed", "old.txt"))
	writeFile(t, filepath.Join(root, "locked.txt"))
	writeFile(t, filepath.Join(root, "lockeddir", "old.txt"))
	writeFile(t, filepath.Join(outside, "target.txt"))
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(sock, &unix.SockaddrUnix{Name: filepath.Join(root, "sock")}); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(sock)
	lock(t, filepath.Join(root, "locked.txt"))
	lock(t, filepath.Join(root, "lockeddir"))

	cutoff := ageCutoff(t, root)
	writeFile(t, filepath.Join(root, "mixed", "fresh.txt"))
	writeFile(t, filepath.Join(root, "fresh.txt"))
	mkdir(t, filepath.Join(root, "freshdir"))

	c, log := newLoggedCleaner(t, root, time.Hour)
	res := c.sweep(context.Background(), cutoff)
	if res.canceled {
		t.Fatal("sweep reported cancellation")
	}
	for _, gone := range []string{"old.txt", "olddir", "nested/a/b/file", "mixed/old.txt"} {
		if exists(t, filepath.Join(root, gone)) {
			t.Errorf("%s survived the sweep", gone)
		}
	}
	for _, kept := range []string{
		"mixed", "mixed/fresh.txt", "fresh.txt", "freshdir", "locked.txt", "lockeddir/old.txt",
		"link", "linkdir", "fifo", "sock",
		// The removal of its file freshened b, which the pass cannot tell
		// from a concurrent change; a later pass removes it.
		"nested/a/b",
	} {
		if !exists(t, filepath.Join(root, kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
	if !exists(t, filepath.Join(outside, "target.txt")) {
		t.Error("the sweep followed a symbolic link")
	}
	if !exists(t, root) {
		t.Error("the sweep removed its root")
	}
	// old.txt, olddir, nested/a/b/file, mixed/old.txt
	if res.removed != 4 {
		t.Errorf("removed = %d, want 4", res.removed)
	}
	// Lock contention, symbolic links, special files and fresh entries are
	// normal and not worth a warning.
	if log.Len() != 0 {
		t.Errorf("sweep logged: %s", log)
	}
}

// TestTmpSweepRemovesEmptiedDirsLater: a directory the pass emptied has a
// fresh modification time, so it goes once that time has expired too, one
// level per pass.
func TestTmpSweepRemovesEmptiedDirsLater(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	writeFile(t, filepath.Join(root, "a", "b", "file"))
	c := newTestCleaner(t, root, time.Hour)
	for _, want := range []string{"a/b", "a", ""} {
		c.sweep(context.Background(), ageCutoff(t, root))
		got := ""
		for _, p := range []string{"a/b/file", "a/b", "a"} {
			if exists(t, filepath.Join(root, p)) {
				got = p
				break
			}
		}
		if got != want {
			t.Fatalf("deepest entry left = %q, want %q", got, want)
		}
	}
}

// TestTmpRecheck: the last check before an unlink rejects a name that no
// longer refers to the checked inode, and an inode accessed, modified or
// changed since the check, even when its times are old again.
func TestTmpRecheck(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	tests := []struct {
		name   string
		change func(t *testing.T, path string)
		want   bool
	}{
		{name: "unchanged", change: func(*testing.T, string) {}, want: true},
		{name: "read", change: func(t *testing.T, path string) {
			if _, err := os.ReadFile(path); err != nil { //nolint:gosec // G304: test file
				t.Fatal(err)
			}
		}},
		{name: "written", change: func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0) //nolint:gosec // G304: test file
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("y"); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "times set back", change: func(t *testing.T, path string) {
			old := time.Now().Add(-72 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "chmod", change: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o400); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaced", change: func(t *testing.T, path string) {
			writeFile(t, path+".new")
			if err := os.Rename(path+".new", path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "removed", change: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	c := newTestCleaner(t, root, time.Hour)
	dir, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(dir) })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, tt.name)
			writeFile(t, path)
			st, err := statxAt(dir, tt.name)
			if err != nil {
				t.Fatal(err)
			}
			// Past the coarse timestamp granularity.
			time.Sleep(20 * time.Millisecond)
			tt.change(t, path)
			if _, got := c.recheck(dir, tt.name, st, time.Now()); got != tt.want {
				t.Fatalf("recheck after %s = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestTmpExpectedErr(t *testing.T) {
	t.Parallel()
	for _, err := range []error{unix.ENOENT, unix.EXDEV, unix.ELOOP, unix.ENOTDIR, unix.EISDIR, unix.EWOULDBLOCK, unix.ENOTEMPTY, unix.EEXIST, unix.ENXIO, unix.EINTR} {
		if !tmpExpectedErr(err) {
			t.Errorf("%v (%d) is unexpected", err, err)
		}
	}
	for _, err := range []error{unix.EACCES, unix.EPERM, unix.EIO, unix.EMFILE, unix.ENOSYS, unix.EBUSY, unix.EROFS} {
		if tmpExpectedErr(err) {
			t.Errorf("%v (%d) is expected", err, err)
		}
	}
}

// TestTmpSweepLogsUnexpectedErrors: failures outside normal races reach the
// log, once per pass with a count.
func TestTmpSweepLogsUnexpectedErrors(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads directories regardless of their mode")
	}
	root := sweepRoot(t)
	for _, d := range []string{"denied1", "denied2"} {
		mkdir(t, filepath.Join(root, d))
		if err := os.Chmod(filepath.Join(root, d), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, d), 0o700) }) //nolint:gosec // G302: restores access for the cleanup
	}
	cutoff := ageCutoff(t, root)
	c, log := newLoggedCleaner(t, root, time.Hour)
	res := c.sweep(context.Background(), cutoff)
	if res.failed != 2 {
		t.Errorf("failed = %d, want 2", res.failed)
	}
	out := log.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "failures=2") || !strings.Contains(out, "permission denied") {
		t.Errorf("log = %q, want one warning with both failures", out)
	}
}

// TestTmpSweepKeepsAtime makes sure the scan itself does not refresh access
// times, which would keep entries alive (or age the scan's own view).
func TestTmpSweepKeepsAtime(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	keep := filepath.Join(root, "keep")
	writeFile(t, filepath.Join(keep, "fresh.txt"))
	seen := filepath.Join(root, "seen.txt")
	writeFile(t, seen)
	old := time.Now().Add(-72 * time.Hour)
	for _, p := range []string{keep, root} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(seen, old, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := newTestCleaner(t, root, time.Hour)
	c.sweep(context.Background(), time.Now().Add(-time.Hour))
	for _, p := range []string{root, keep, seen} {
		if got := atime(t, p); !got.Equal(old) {
			t.Errorf("atime of %s = %v, want %v", p, got, old)
		}
	}
	if !exists(t, filepath.Join(keep, "fresh.txt")) {
		t.Error("fresh file removed")
	}
}

func TestTmpSweepDepthBound(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	deep := root
	for i := range tmpMaxDepth + 2 {
		deep = filepath.Join(deep, strconv.Itoa(i%10))
	}
	writeFile(t, filepath.Join(deep, "file"))
	writeFile(t, filepath.Join(root, "shallow"))
	cutoff := ageCutoff(t, root)
	c := newTestCleaner(t, root, time.Hour)
	c.sweep(context.Background(), cutoff)
	if !exists(t, filepath.Join(deep, "file")) {
		t.Error("removed a file below the depth bound")
	}
	if exists(t, filepath.Join(root, "shallow")) {
		t.Error("kept an expired file within the depth bound")
	}
}

func TestTmpSweepCanceled(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	writeFile(t, filepath.Join(root, "old.txt"))
	cutoff := ageCutoff(t, root)
	c := newTestCleaner(t, root, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := c.sweep(ctx, cutoff); !res.canceled || res.removed != 0 {
		t.Fatalf("canceled sweep = %+v", res)
	}
	if !exists(t, filepath.Join(root, "old.txt")) {
		t.Fatal("canceled sweep removed a file")
	}
}

func TestTmpSweepTTLDisabled(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	writeFile(t, filepath.Join(root, "old.txt"))
	cutoff := ageCutoff(t, root)
	c := newTestCleaner(t, root, 0)
	c.sweepTTL(cutoff.Add(time.Hour))
	if !exists(t, filepath.Join(root, "old.txt")) {
		t.Fatal("sweep ran with the TTL disabled")
	}
	c.SetTTL(time.Millisecond)
	c.sweepTTL(cutoff.Add(time.Millisecond))
	if exists(t, filepath.Join(root, "old.txt")) {
		t.Fatal("sweep did not run after the TTL was enabled")
	}
}

func countEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// TestTmpSetTTLCancelsActiveSweep: once SetTTL returns, a sweep that ran
// with the old TTL deletes nothing more, whether the TTL is disabled or
// raised past the age of the files.
func TestTmpSetTTLCancelsActiveSweep(t *testing.T) {
	t.Parallel()
	for _, next := range []time.Duration{0, time.Hour} {
		t.Run(next.String(), func(t *testing.T) {
			t.Parallel()
			// A fast machine may finish a pass before SetTTL; only an
			// interrupted pass proves anything.
			for range 5 {
				if interruptSweep(t, next) {
					return
				}
			}
			t.Fatal("no sweep was still running when SetTTL returned")
		})
	}
}

// interruptSweep starts a sweep of expired files, calls SetTTL(next) once it
// has removed some and checks that none goes after SetTTL returns. It
// reports whether the sweep was still running.
func interruptSweep(t *testing.T, next time.Duration) bool {
	t.Helper()
	root := sweepRoot(t)
	const n = 3000
	for i := range n {
		writeFile(t, filepath.Join(root, "f"+strconv.Itoa(i)))
	}
	cutoff := ageCutoff(t, root)
	c := newTestCleaner(t, root, time.Nanosecond)
	done := make(chan tmpSweep, 1)
	go func() { done <- c.sweepTTL(cutoff.Add(time.Nanosecond)) }()
	deadline := time.Now().Add(10 * time.Second)
	for countEntries(t, root) == n && time.Now().Before(deadline) {
		time.Sleep(100 * time.Microsecond)
	}
	c.SetTTL(next)
	left := countEntries(t, root)
	res := <-done
	if got := countEntries(t, root); got != left {
		t.Fatalf("%d entries removed after SetTTL returned", left-got)
	}
	if res.removed+left != n {
		t.Fatalf("removed %d, left %d, want %d in total", res.removed, left, n)
	}
	if left == 0 || left == n {
		return false
	}
	if !res.canceled {
		t.Fatalf("interrupted sweep = %+v, want canceled", res)
	}
	// The new TTL keeps the remaining, younger files.
	if res := c.sweepTTL(cutoff.Add(time.Nanosecond)); res.removed != 0 && next != 0 {
		t.Fatalf("sweep with the new TTL removed %d", res.removed)
	}
	return true
}

func TestTmpConcurrentSweeps(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	for i := range 50 {
		writeFile(t, filepath.Join(root, "d"+strconv.Itoa(i%5), "s"+strconv.Itoa(i%3), "f"+strconv.Itoa(i)))
	}
	cutoff := ageCutoff(t, root)
	writeFile(t, filepath.Join(root, "d0", "fresh"))
	c := newTestCleaner(t, root, time.Hour)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { c.sweep(context.Background(), cutoff) })
	}
	wg.Wait()
	var left []string
	err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root {
			left = append(left, strings.TrimPrefix(p, root+"/"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every file went; the emptied directories stay until their own times
	// expire, and the fresh file stays.
	var want []string
	for d := range 5 {
		want = append(want, "d"+strconv.Itoa(d))
		if d == 0 {
			want = append(want, "d0/fresh")
		}
		for s := range 3 {
			want = append(want, "d"+strconv.Itoa(d)+"/s"+strconv.Itoa(s))
		}
	}
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Fatalf("left after concurrent sweeps: %q", left)
	}
}

func TestTmpCleanerClose(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	c, err := newTmpCleaner(slog.New(slog.DiscardHandler), root, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	c.SetTTL(time.Hour)
	closed := make(chan struct{})
	go func() { c.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not join the worker")
	}
	c.SetTTL(time.Minute)
	c.close()
	if res := c.sweepTTL(time.Now()); res.removed != 0 {
		t.Fatalf("sweep after close = %+v", res)
	}
}

func TestTmpCleanerRejectsMissingRoot(t *testing.T) {
	t.Parallel()
	if _, err := newTmpCleaner(slog.New(slog.DiscardHandler), filepath.Join(t.TempDir(), "missing"), 0, false); err == nil {
		t.Fatal("newTmpCleaner accepted a missing root")
	}
}

func TestFitrimRequest(t *testing.T) {
	t.Parallel()
	size := unsafe.Sizeof(fstrimRange{})
	if size != 24 {
		t.Fatalf("fstrim_range is %d bytes, want 24", size)
	}
	// _IOWR('X', 121, struct fstrim_range)
	want := uint(3)<<30 | uint(size)<<16 | uint('X')<<8 | 121
	if fitrim != want {
		t.Fatalf("FITRIM = %#x, want %#x", fitrim, want)
	}
}

func TestTmpStatus(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := writeTmpStatus(&out, os.TempDir()); err != nil {
		t.Fatal(err)
	}
	var u TmpUsage
	if err := json.Unmarshal(out.Bytes(), &u); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if u.CapacityBytes == 0 || u.UsedBytes > u.CapacityBytes {
		t.Fatalf("usage = %+v", u)
	}
}

// TestTmpSweepDirectoryIgnoresCtime verifies that an empty directory with
// expired atime and mtime is removed even when its ctime is newer than the
// cutoff (for example after a chmod or Chtimes), matching the documented
// contract that directory ctime is ignored for age.
func TestTmpSweepDirectoryIgnoresCtime(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	oldDir := filepath.Join(root, "old-dir-fresh-ctime")
	mkdir(t, oldDir)
	oldFile := filepath.Join(root, "old-file-fresh-ctime")
	writeFile(t, oldFile)

	cutoff := ageCutoff(t, root)
	past := cutoff.Add(-2 * time.Hour)
	// Chtimes sets atime and mtime in the past while advancing ctime to now (> cutoff).
	if err := os.Chtimes(oldDir, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatal(err)
	}

	c := newTestCleaner(t, root, time.Hour)
	res := c.sweep(context.Background(), cutoff)
	if exists(t, oldDir) {
		t.Fatal("empty directory with expired atime/mtime and fresh ctime survived the sweep")
	}
	if !exists(t, oldFile) {
		t.Fatal("regular file with fresh ctime was removed")
	}
	if res.removed != 1 {
		t.Fatalf("removed = %d, want 1", res.removed)
	}
}

// TestTmpSweepRetainedPrefixDoesNotStarvePastSeenLimit verifies that when a
// directory contains more retained entries than walkSeenLimit and more expired
// entries than tmpWalkBatch, the sweep continues scanning to EOF once capped
// and removes all expired entries instead of stopping at tmpWalkBatch or
// starving entries later in the directory.
func TestTmpSweepRetainedPrefixDoesNotStarvePastSeenLimit(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	const (
		seenLimit = 16
		retained  = 40
		expired   = tmpWalkBatch + 50
	)
	for i := range expired {
		writeFile(t, filepath.Join(root, "expired-"+strconv.Itoa(i)))
	}
	cutoff := ageCutoff(t, root)
	for i := range retained {
		writeFile(t, filepath.Join(root, "retained-"+strconv.Itoa(i)))
	}

	c := newTestCleaner(t, root, time.Hour)
	c.walkSeenLimit = seenLimit
	res := c.sweep(context.Background(), cutoff)
	if res.removed != expired {
		t.Fatalf("removed = %d, want %d", res.removed, expired)
	}
	for i := range expired {
		if exists(t, filepath.Join(root, "expired-"+strconv.Itoa(i))) {
			t.Fatalf("expired-%d survived behind retained prefix", i)
		}
	}
	for i := range retained {
		if !exists(t, filepath.Join(root, "retained-"+strconv.Itoa(i))) {
			t.Fatalf("retained-%d was removed", i)
		}
	}
}

// TestLiveStateApplyLeavesTmpTTLUnchangedOnFailure verifies that when a reload
// fails in a later step (such as setCABundle), the cleaner's TTL is not
// updated to a shorter value.
func TestLiveStateApplyLeavesTmpTTLUnchangedOnFailure(t *testing.T) {
	t.Parallel()
	root := sweepRoot(t)
	writeFile(t, filepath.Join(root, "old.txt"))
	cutoff := ageCutoff(t, root)

	c := newTestCleaner(t, root, time.Hour)
	state := &liveState{
		logger: slog.New(slog.DiscardHandler),
		tmp:    c,
		setCA: func(path string) error {
			if path != "" {
				return os.ErrNotExist
			}
			return nil
		},
	}
	short := time.Nanosecond
	err := state.apply(Reload{
		CABundle: filepath.Join(root, "missing-ca.crt"),
		TmpTTL:   &short,
	})
	if err == nil {
		t.Fatal("apply succeeded with failing CA bundle, want error")
	}
	// If the failed reload had updated the cleaner to 1ns, a sweep at
	// cutoff + 10ms would expire old.txt; with the original 1h TTL preserved,
	// old.txt stays untouched.
	if res := c.sweepTTL(cutoff.Add(10 * time.Millisecond)); res.removed != 0 {
		t.Fatalf("sweep after failed reload removed %d entries, want 0 (TTL should stay 1h)", res.removed)
	}
	if !exists(t, filepath.Join(root, "old.txt")) {
		t.Fatal("old.txt was removed after failed reload")
	}

	// A succeeding reload applies the new TTL (including 0 to disable cleanup
	// and a shorter positive duration to expire old.txt).
	zero := time.Duration(0)
	if err := state.apply(Reload{TmpTTL: &zero}); err != nil {
		t.Fatalf("apply zero TTL: %v", err)
	}
	if res := c.sweepTTL(cutoff.Add(2 * time.Hour)); res.removed != 0 {
		t.Fatalf("sweep with disabled TTL removed %d entries, want 0", res.removed)
	}
	if err := state.apply(Reload{TmpTTL: &short}); err != nil {
		t.Fatalf("apply short TTL: %v", err)
	}
	if res := c.sweepTTL(cutoff.Add(10 * time.Millisecond)); res.removed != 1 {
		t.Fatalf("sweep after successful short TTL reload removed %d entries, want 1", res.removed)
	}
	if exists(t, filepath.Join(root, "old.txt")) {
		t.Fatal("old.txt survived sweep after successful short TTL reload")
	}
}
