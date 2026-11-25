package vm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	microvm "github.com/stacklok/go-microvm"

	"github.com/seznam/jailoc/internal/guest"
)

func diskTmpSpec(stateDir string, size int64) Spec {
	return Spec{
		Kind:        KindWorkspace,
		StateDir:    stateDir,
		TmpDiskSize: size,
		Guest:       &guest.Config{Tmp: guest.TmpConfig{Storage: "disk", SizeBytes: size}},
	}
}

func TestWorkspaceDisks(t *testing.T) {
	t.Parallel()
	const dir = "/state"
	docker := microvm.Disk{ID: "docker", HostPath: filepath.Join(dir, "docker.img")}
	tmp := microvm.Disk{ID: "tmp", HostPath: filepath.Join(dir, "tmp.img")}
	tests := []struct {
		name string
		spec Spec
		want []microvm.Disk
	}{
		{"none", Spec{StateDir: dir}, nil},
		{"docker only", Spec{StateDir: dir, Docker: true}, []microvm.Disk{docker}},
		{"tmp only is the first disk", diskTmpSpec(dir, 1<<30), []microvm.Disk{tmp}},
		{"docker then tmp", func() Spec { s := diskTmpSpec(dir, 1<<30); s.Docker = true; return s }(), []microvm.Disk{docker, tmp}},
		{"memory tmp adds no disk", Spec{StateDir: dir, Docker: true, Guest: &guest.Config{Tmp: guest.TmpConfig{Storage: "memory", SizeBytes: 1 << 30}}}, []microvm.Disk{docker}},
		{"nil guest config", Spec{StateDir: dir, TmpDiskSize: 1 << 30}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workspaceDisks(tt.spec); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("workspaceDisks = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestWorkspaceOptionsDiskCount(t *testing.T) {
	t.Parallel()
	// The tmp disk must not collide with the Docker disk's guest.DockerDisk
	// device: with both, docker is /dev/vda, tmp /dev/vdb; alone, tmp is /dev/vda.
	s := diskTmpSpec("/state", 1<<30)
	s.Docker = true
	disks := workspaceDisks(s)
	if len(disks) != 2 || disks[0].ID != "docker" || disks[1].ID != "tmp" {
		t.Fatalf("disk order = %+v, want docker then tmp", disks)
	}
}

func TestTmpDiskSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec Spec
		want int64
	}{
		{"host-set size wins", Spec{TmpDiskSize: 3 << 30, Guest: &guest.Config{Tmp: guest.TmpConfig{Storage: "disk", SizeBytes: 1 << 30}}}, 3 << 30},
		{"falls back to guest size", Spec{Guest: &guest.Config{Tmp: guest.TmpConfig{Storage: "disk", SizeBytes: 1 << 30}}}, 1 << 30},
		{"unset", Spec{Guest: &guest.Config{}}, 0},
		{"nil guest", Spec{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tmpDiskSize(tt.spec); got != tt.want {
				t.Fatalf("tmpDiskSize = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTmpDiskPathIsPrivateToStateDir(t *testing.T) {
	t.Parallel()
	const dir = "/state"
	p := tmpDiskPath(dir)
	if p == diskPath(dir) {
		t.Fatalf("tmp disk shares the Docker disk path %s", p)
	}
	for _, shared := range []string{GuestDataDir(dir), GuestReadOnlyDir(dir)} {
		if rel, err := filepath.Rel(shared, p); err == nil && !strings.HasPrefix(rel, "..") {
			t.Fatalf("tmp disk %s lies inside the guest-visible share %s", p, shared)
		}
	}
	if filepath.Dir(p) != dir {
		t.Fatalf("tmp disk %s is not directly in the state dir", p)
	}
}

func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no block accounting on this host")
	}
	return int64(sys.Blocks) * 512
}

func TestPrepareTmpDiskCreatesFreshSparsePrivateDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const size = 512 << 20
	release, err := PrepareTmpDisk(dir, size)
	if err != nil {
		t.Fatalf("PrepareTmpDisk: %v", err)
	}
	path := tmpDiskPath(dir)
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat tmp disk: %v", err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want regular 0600", st.Mode())
	}
	if st.Size() != size {
		t.Fatalf("size = %d, want exactly %d", st.Size(), int64(size))
	}
	if got := allocatedBytes(t, path); got > size/100 {
		t.Fatalf("disk allocates %d bytes, want a sparse file", got)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("tmp disk after release: %v, want removed", err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release: %v, want idempotent", err)
	}
}

func TestPrepareTmpDiskReplacesStaleDiskEveryBoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := tmpDiskPath(dir)
	// A disk left by a crashed supervisor (with an orphaned runner still
	// holding its unlinked inode open): different size, old data.
	if err := os.WriteFile(path, bytes.Repeat([]byte("stale"), 1<<20), 0o644); err != nil { //nolint:gosec // G306: simulates a foreign leftover
		t.Fatal(err)
	}
	staleFile, err := os.Open(path) //nolint:gosec // G304: test path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staleFile.Close() })
	old, err := staleFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	const size = 64 << 20
	release, err := PrepareTmpDisk(dir, size)
	if err != nil {
		t.Fatalf("PrepareTmpDisk: %v", err)
	}
	t.Cleanup(func() { _ = release() })
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(old, st) {
		t.Fatal("stale disk was reused, want a new file")
	}
	if st.Size() != size || st.Mode().Perm() != 0o600 {
		t.Fatalf("size/mode = %d/%v, want %d/0600 (also when stale disk was larger or looser)", st.Size(), st.Mode().Perm(), int64(size))
	}
	newHead := make([]byte, 5)
	f, err := os.Open(path) //nolint:gosec // G304: test path
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.ReadAt(newHead, 0)
	_ = f.Close()
	if err != nil || !bytes.Equal(newHead, make([]byte, 5)) {
		t.Fatalf("new disk head = %q, %v; want zeroed sparse file", newHead, err)
	}
	// Grow-only reuse must never happen: a second boot with a smaller size is exact too.
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release2, err := PrepareTmpDisk(dir, size/2)
	if err != nil {
		t.Fatalf("second PrepareTmpDisk: %v", err)
	}
	t.Cleanup(func() { _ = release2() })
	if st, _ := os.Stat(path); st == nil || st.Size() != size/2 {
		t.Fatalf("second boot size = %v, want %d", st, size/2)
	}
}

func TestPrepareTmpDiskDoesNotFollowSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, tmpDiskPath(dir)); err != nil {
		t.Fatal(err)
	}
	release, err := PrepareTmpDisk(dir, 1<<20)
	if err != nil {
		t.Fatalf("PrepareTmpDisk: %v", err)
	}
	t.Cleanup(func() { _ = release() })
	if got, err := os.ReadFile(victim); err != nil || string(got) != "keep" { //nolint:gosec // G304: victim is a t.TempDir() file created by this test
		t.Fatalf("symlink target = %q, %v; want untouched", got, err)
	}
	if st, err := os.Lstat(tmpDiskPath(dir)); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("tmp disk after replace = %v, %v; want a regular file", st, err)
	}
}

func TestPrepareTmpDiskIsExclusiveForTheVMLifetime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	release, err := PrepareTmpDisk(dir, 1<<20)
	if err != nil {
		t.Fatalf("PrepareTmpDisk: %v", err)
	}
	before, err := os.Stat(tmpDiskPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareTmpDisk(dir, 2<<20); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("concurrent PrepareTmpDisk = %v, want an in use error", err)
	}
	after, err := os.Stat(tmpDiskPath(dir))
	if err != nil || !os.SameFile(before, after) || after.Size() != 1<<20 {
		t.Fatalf("the losing caller touched the running VM's disk: %v, %v", after, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	again, err := PrepareTmpDisk(dir, 1<<20)
	if err != nil {
		t.Fatalf("PrepareTmpDisk after release: %v", err)
	}
	_ = again()
}

func TestPrepareTmpDiskRejectsBadInputWithoutKeepingTheLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		dir  string
		size int64
	}{
		"zero size":     {dir, 0},
		"negative size": {dir, -1},
		"relative dir":  {"relative/state", 1 << 20},
		"empty dir":     {"", 1 << 20},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrepareTmpDisk(tc.dir, tc.size); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	// A tmp disk path that cannot be replaced (non-empty directory) fails
	// without leaving the lifetime lock behind.
	blocker := tmpDiskPath(dir)
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareTmpDisk(dir, 1<<20); err == nil {
		t.Fatal("PrepareTmpDisk over a directory succeeded")
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	release, err := PrepareTmpDisk(dir, 1<<20)
	if err != nil {
		t.Fatalf("PrepareTmpDisk after a failed attempt: %v (lock leaked)", err)
	}
	_ = release()
}

func TestTmpPressure(t *testing.T) {
	t.Parallel()
	if got := tmpPressure(10<<30, 4<<30); got != "" {
		t.Fatalf("plenty of space warned: %q", got)
	}
	got := tmpPressure(1<<30, 4<<30)
	for _, want := range []string{"1024 MiB free", "4096 MiB", "sparse", "not a reservation"} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning %q misses %q", got, want)
		}
	}
}

func TestHostFreeBytes(t *testing.T) {
	t.Parallel()
	free, err := hostFreeBytes(t.TempDir())
	if err != nil || free == 0 {
		t.Fatalf("hostFreeBytes = %d, %v; want a positive value", free, err)
	}
	if _, err := hostFreeBytes(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("hostFreeBytes of a missing directory succeeded")
	}
}

func TestPrepareWorkspaceTmp(t *testing.T) {
	t.Parallel()
	type logs struct{ lines []string }
	logger := func(l *logs) func(string, ...any) {
		return func(format string, args ...any) { l.lines = append(l.lines, fmt.Sprintf(format, args...)) }
	}
	t.Run("memory tmp creates nothing", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		var l logs
		release, err := prepareWorkspaceTmp(Spec{StateDir: dir, Guest: &guest.Config{}}, logger(&l), hostFreeBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(tmpDiskPath(dir)); !os.IsNotExist(err) {
			t.Fatalf("tmp disk exists: %v", err)
		}
	})
	t.Run("disk tmp is sized from the spec and warns under pressure", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		var l logs
		release, err := prepareWorkspaceTmp(diskTmpSpec(dir, 8<<20), logger(&l), func(string) (uint64, error) { return 1 << 20, nil })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = release() })
		if st, err := os.Stat(tmpDiskPath(dir)); err != nil || st.Size() != 8<<20 {
			t.Fatalf("tmp disk = %v, %v; want 8 MiB", st, err)
		}
		if len(l.lines) != 1 || !strings.Contains(l.lines[0], "not a reservation") {
			t.Fatalf("log = %q, want one sparse-cap warning", l.lines)
		}
	})
	t.Run("no warning with enough space", func(t *testing.T) {
		t.Parallel()
		var l logs
		release, err := prepareWorkspaceTmp(diskTmpSpec(t.TempDir(), 1<<20), logger(&l), func(string) (uint64, error) { return 1 << 40, nil })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = release() })
		if len(l.lines) != 0 {
			t.Fatalf("log = %q, want none", l.lines)
		}
	})
	t.Run("unreadable free space is logged, not fatal", func(t *testing.T) {
		t.Parallel()
		var l logs
		release, err := prepareWorkspaceTmp(diskTmpSpec(t.TempDir(), 1<<20), logger(&l), func(string) (uint64, error) { return 0, os.ErrPermission })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = release() })
		if len(l.lines) != 1 {
			t.Fatalf("log = %q, want one line", l.lines)
		}
	})
	t.Run("disk tmp without size is an error", func(t *testing.T) {
		t.Parallel()
		s := diskTmpSpec(t.TempDir(), 0)
		if _, err := prepareWorkspaceTmp(s, func(string, ...any) {}, hostFreeBytes); err == nil {
			t.Fatal("want an error for a disk tmp of unknown size")
		}
	})
	t.Run("nil logger is allowed", func(t *testing.T) {
		t.Parallel()
		release, err := prepareWorkspaceTmp(diskTmpSpec(t.TempDir(), 1<<20), nil, func(string) (uint64, error) { return 0, nil })
		if err != nil {
			t.Fatal(err)
		}
		_ = release()
	})
}

func TestWorkspaceDisksStateAndCache(t *testing.T) {
	t.Parallel()
	const dir = "/state"
	state := microvm.Disk{ID: "state", HostPath: filepath.Join(dir, "state.img")}
	cache := microvm.Disk{ID: "cache", HostPath: filepath.Join(dir, "cache.img")}
	docker := microvm.Disk{ID: "docker", HostPath: filepath.Join(dir, "docker.img")}
	tmp := microvm.Disk{ID: "tmp", HostPath: filepath.Join(dir, "tmp.img")}
	all := diskTmpSpec(dir, 1<<30)
	all.Docker, all.StateDisk, all.CacheDisk = true, true, true
	tests := []struct {
		name     string
		spec     Spec
		want     []microvm.Disk
		stateDev string
		cacheDev string
	}{
		{"state only", Spec{StateDir: dir, StateDisk: true}, []microvm.Disk{state}, "/dev/vda", ""},
		{"cache only", Spec{StateDir: dir, CacheDisk: true}, []microvm.Disk{cache}, "", "/dev/vda"},
		{"state and cache", Spec{StateDir: dir, StateDisk: true, CacheDisk: true}, []microvm.Disk{state, cache}, "/dev/vda", "/dev/vdb"},
		{"docker, state, cache", Spec{StateDir: dir, Docker: true, StateDisk: true, CacheDisk: true}, []microvm.Disk{docker, state, cache}, "/dev/vdb", "/dev/vdc"},
		{"all in order", all, []microvm.Disk{docker, tmp, state, cache}, "/dev/vdc", "/dev/vdd"},
		{"none", Spec{StateDir: dir}, nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := workspaceDisks(tt.spec); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("workspaceDisks = %+v, want %+v", got, tt.want)
			}
			stateDev, cacheDev := workspaceDiskDevices(tt.spec)
			if stateDev != tt.stateDev || cacheDev != tt.cacheDev {
				t.Fatalf("workspaceDiskDevices = %q, %q, want %q, %q", stateDev, cacheDev, tt.stateDev, tt.cacheDev)
			}
		})
	}
}
