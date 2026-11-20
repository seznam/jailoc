package vm

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	initFuncsBegin = "# jailoc-init:functions begin"
	initFuncsEnd   = "# jailoc-init:functions end"

	// testDiskSize is the DISK_SIZE the harness renders (20 GiB).
	testDiskSize = 21474836480
	// ext4 superblock fields (little-endian) at their absolute device offsets.
	sbBlocksLo           = 1028
	sbLogBlockSize       = 1048
	sbFeatureIncompat    = 1120
	sbBlocksHi           = 1360
	featureIncompat64Bit = 0x80
)

// initFunctions returns the function definitions of init.sh, which the tests
// run under real sh with stub tools on PATH.
func initFunctions(t *testing.T) string {
	t.Helper()
	_, rest, ok := strings.Cut(initScript, initFuncsBegin+"\n")
	if !ok {
		t.Fatalf("init.sh lacks %q", initFuncsBegin)
	}
	body, _, ok := strings.Cut(rest, initFuncsEnd)
	if !ok {
		t.Fatalf("init.sh lacks %q", initFuncsEnd)
	}
	return body
}

func requireSh(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	return sh
}

type initHarness struct {
	t     *testing.T
	sh    string
	root  string
	dev   string
	bin   string
	state string
	calls string
}

func newInitHarness(t *testing.T) *initHarness {
	t.Helper()
	root := t.TempDir()
	h := &initHarness{t: t, sh: requireSh(t), root: root, dev: filepath.Join(root, "vda"), bin: filepath.Join(root, "bin"), state: filepath.Join(root, "state"), calls: filepath.Join(root, "calls")}
	for _, d := range []string{h.bin, h.state, filepath.Join(root, "docker"), filepath.Join(root, "run"), filepath.Join(root, "control")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// Tools the init script calls; each records its invocation.
	h.stub("blkid", `echo ext4`)
	h.stub("mount", ``)
	// mkfs.ext4 records the file system size it was asked for in the
	// superblock of the device file (4 KiB blocks, no 64bit feature).
	h.stub("mkfs.ext4", `[ -e "$STATE/installed/e2fsprogs" ] && [ -e "$STATE/installed/e2fsprogs-libs" ] || { echo "mkfs.ext4: not found" >&2; exit 127; }
for blocks; do :; done
le32() { printf "$(printf '\\%03o\\%03o\\%03o\\%03o' $(($1 & 255)) $(($1 >> 8 & 255)) $(($1 >> 16 & 255)) $(($1 >> 24 & 255)))"; }
: > "$DEV"
le32 "$blocks" | dd of="$DEV" bs=1 seek=1028 conv=notrunc 2>/dev/null
le32 2 | dd of="$DEV" bs=1 seek=1048 conv=notrunc 2>/dev/null
le32 0 | dd of="$DEV" bs=1 seek=1120 conv=notrunc 2>/dev/null
le32 0 | dd of="$DEV" bs=1 seek=1360 conv=notrunc 2>/dev/null`)
	h.stub("poweroff", ``)
	h.stub("df", `echo "Filesystem 1K-blocks Used Available Use% Mounted on"`)
	// By default the data disk holds an older file system 1 GiB smaller than
	// the 20 GiB disk, so a boot has to grow it.
	h.setFS(19<<30/4096, 2, 0)
	return h
}

// setFS writes the little-endian ext4 superblock fields the init script
// measures into the device file: the block count (low and high half), the
// log2(block size)-10 and the incompatible feature flags.
func (h *initHarness) setFS(blocks uint64, logBlockSize, incompat uint32) {
	h.t.Helper()
	sb := make([]byte, sbBlocksHi+4)
	binary.LittleEndian.PutUint32(sb[sbBlocksLo:], uint32(blocks&0xffffffff))
	binary.LittleEndian.PutUint32(sb[sbLogBlockSize:], logBlockSize)
	binary.LittleEndian.PutUint32(sb[sbFeatureIncompat:], incompat)
	binary.LittleEndian.PutUint32(sb[sbBlocksHi:], uint32(blocks>>32))
	if err := os.WriteFile(h.dev, sb, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *initHarness) stub(name, body string) {
	h.t.Helper()
	script := fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> \"$CALLS\"\n%s\n", name, body)
	if err := os.WriteFile(filepath.Join(h.bin, name), []byte(script), 0o755); err != nil { //nolint:gosec // G306: test stub must be executable
		h.t.Fatal(err)
	}
}

func (h *initHarness) mark(name string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.state, name), nil, 0o644); err != nil { //nolint:gosec // G306: test marker
		h.t.Fatal(err)
	}
}

// cacheDir and stageDir are the persistent (data disk) and the disposable
// (root filesystem) APK cache of the init script under test.
func (h *initHarness) cacheDir() string { return filepath.Join(h.root, "docker", ".jailoc-apk-cache") }
func (h *initHarness) stageDir() string { return filepath.Join(h.root, "apk-stage") }

// restartRoot simulates a reboot of the guest: the root filesystem (installed
// packages, the staging APK cache, the call log) is discarded, the data disk
// (its APK cache, the full and resized state) is kept.
func (h *initHarness) restartRoot() {
	h.t.Helper()
	for _, p := range []string{filepath.Join(h.state, "installed"), filepath.Join(h.bin, "resize2fs"), h.stageDir(), h.calls} {
		if err := os.RemoveAll(p); err != nil {
			h.t.Fatal(err)
		}
	}
}

// cached lists the package files of the persistent APK cache.
func (h *initHarness) cached() []string {
	h.t.Helper()
	entries, err := os.ReadDir(h.cacheDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// warmCache puts package files into the persistent APK cache.
func (h *initHarness) warmCache(pkgs ...string) {
	h.t.Helper()
	if err := os.MkdirAll(h.cacheDir(), 0o750); err != nil {
		h.t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := os.WriteFile(filepath.Join(h.cacheDir(), p+".apk"), nil, 0o644); err != nil { //nolint:gosec // G306: test package file
			h.t.Fatal(err)
		}
	}
}

// apk simulates the package manager of the pinned Alpine (apk-tools 3.0):
//   - add is the only command and -q, --no-cache, --no-network and --cache-dir
//     the only options; others (apk-tools 3 only flags such as
//     --cache-packages, which apk-tools 2 lacks) fail the call.
//   - A --cache-dir must exist. Packages downloaded with one are kept in it;
//     packages found in it are installed from it, with or without network.
//   - Installing a package installs its dependencies, but downloads only what
//     the root filesystem lacks. Installing e2fsprogs-extra provides
//     resize2fs, which needs e2fsprogs-libs; mkfs.ext4 needs e2fsprogs.
//   - Downloading into a cache on a data disk marked full fails until
//     resize2fs ran. The no-network mark makes the network unreachable.
func (h *initHarness) apk() {
	h.t.Helper()
	h.stub("apk", `
apk_deps() {
	case "$1" in
	e2fsprogs) echo "e2fsprogs-libs libcom_err" ;;
	e2fsprogs-extra) echo "e2fsprogs-libs" ;;
	nftables) echo "libmnl" ;;
	esac
}
want=
closure() {
	case " $want " in *" $1 "*) return 0 ;; esac
	want="$want $1"
	for dep in $(apk_deps "$1"); do closure "$dep"; done
}
[ "${1:-}" = add ] || { echo "apk: unsupported command $*" >&2; exit 99; }
shift
nonet= cdir= pkgs=
while [ $# -gt 0 ]; do
	case "$1" in
	-q|--no-cache) ;;
	--no-network) nonet=1 ;;
	--cache-dir) shift; cdir=${1:-} ;;
	-*) echo "apk: unsupported option $1" >&2; exit 99 ;;
	*) pkgs="$pkgs $1" ;;
	esac
	shift
done
if [ -n "$cdir" ] && [ ! -d "$cdir" ]; then
	echo "apk: $cdir: No such file or directory" >&2
	exit 1
fi
for p in $pkgs; do closure "$p"; done
mkdir -p "$STATE/installed"
fetch=
for p in $want; do
	[ ! -e "$STATE/installed/$p" ] || continue
	if [ -n "$cdir" ] && [ -e "$cdir/$p.apk" ]; then continue; fi
	if [ -n "$nonet" ] || [ -e "$STATE/no-network" ]; then
		echo "apk: unable to select packages: $p (network unreachable or not cached)" >&2
		exit 1
	fi
	fetch="$fetch $p"
done
if [ -n "$fetch" ] && [ -n "$cdir" ]; then
	case "$cdir" in
	"$DATA_MOUNT"/*)
		if [ -e "$STATE/full" ] && [ ! -e "$STATE/resized" ]; then
			echo "apk: $cdir: No space left on device" >&2
			exit 1
		fi ;;
	esac
	for p in $fetch; do touch "$cdir/$p.apk"; done
fi
for p in $want; do touch "$STATE/installed/$p"; done
case " $want " in *" e2fsprogs-extra "*)
	printf '#!/bin/sh\necho "resize2fs $*" >> "$CALLS"\n[ -e "$STATE/installed/e2fsprogs-libs" ] || exit 127\n[ ! -e "$STATE/resize-fails" ] || exit 1\ntouch "$STATE/resized"\n' > "$BIN/resize2fs"
	chmod +x "$BIN/resize2fs" ;;
esac
`)
}

// run executes script after the init.sh functions with the harness paths.
func (h *initHarness) run(script string, env ...string) (string, int) {
	h.t.Helper()
	preamble := fmt.Sprintf(`set -u
DATA_DEV=%[2]s
DATA_DIR=%[1]s/docker
APKCACHE=$DATA_DIR/.jailoc-apk-cache
APKSTAGE=%[1]s/apk-stage
RUN_DIR=%[1]s/run
DAEMON_CONFIG=$RUN_DIR/jailoc-dockerd.json
CONTROL=%[1]s/control
GATEWAY=192.168.127.1
GUEST=192.168.127.2
DOCKER_PORT=2375
AGENT_GID=1000
DISK_SIZE=%[3]d
STOP_TICKS=3
export CONTROL
`, h.root, h.dev, testDiskSize)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// G204: runs the embedded init.sh functions with a test-controlled script
	// and stub tools on PATH; no external input reaches the shell.
	cmd := exec.CommandContext(ctx, h.sh, "-c", preamble+initFunctions(h.t)+script) //nolint:gosec // G204: trusted embedded init functions, test-controlled script
	cmd.Env = append([]string{"PATH=" + h.bin + ":/usr/bin:/bin", "CALLS=" + h.calls, "STATE=" + h.state, "BIN=" + h.bin, "DEV=" + h.dev, "DATA_MOUNT=" + filepath.Join(h.root, "docker")}, env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	default:
		h.t.Fatalf("run sh: %v", err)
		return "", -1
	}
}

func (h *initHarness) callLog() []string {
	h.t.Helper()
	data, err := os.ReadFile(h.calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// index returns the position of the first call containing every part, or -1.
func index(calls []string, parts ...string) int {
	for i, c := range calls {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(c, p)
		}
		if ok {
			return i
		}
	}
	return -1
}

func TestInitScriptSyntax(t *testing.T) {
	t.Parallel()
	sh := requireSh(t)
	script := renderBuilderInit(Spec{Kind: KindBuilder, Workspace: "builder", StateDir: "/state", DiskSize: 20 << 30})
	if m := regexp.MustCompile(`@[A-Z_]+@`).FindString(script); m != "" {
		t.Fatalf("rendered init.sh keeps placeholder %s", m)
	}
	if !strings.Contains(script, "DISK_SIZE=21474836480") {
		t.Errorf("rendered init.sh lacks the disk size")
	}
	path := filepath.Join(t.TempDir(), "init.sh")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil { //nolint:gosec // G204: test runs sh -n on a temp script
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	if got := renderBuilderInit(Spec{Kind: KindBuilder, StateDir: "/state"}); !strings.Contains(got, fmt.Sprintf("DISK_SIZE=%d", DefaultDiskSize)) {
		t.Errorf("zero DiskSize does not render the default disk size")
	}
}

func TestInitGrowsBeforePackageInstall(t *testing.T) {
	t.Parallel()
	t.Run("full disk, cold cache, network", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.apk()
		h.mark("full")
		out, code := h.run(`prepare_data_disk`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		calls := h.callLog()
		mount, tool, resize, nft := index(calls, "mount -t ext4 "+h.dev), index(calls, "apk", "--cache-dir "+h.stageDir(), "e2fsprogs-extra"), index(calls, "resize2fs"), index(calls, "nftables")
		if mount < 0 || tool < mount || resize < tool || nft < resize {
			t.Fatalf("want mount < resize tool install < resize2fs < nftables install, got %d %d %d %d in:\n%s", mount, tool, resize, nft, strings.Join(calls, "\n"))
		}
		for i, c := range calls[:resize] {
			if strings.HasPrefix(c, "apk") && strings.Contains(c, h.cacheDir()) && !strings.Contains(c, "--no-network") {
				t.Errorf("call %d writes the data disk cache before the resize: %s", i, c)
			}
		}
		if index(calls, "apk", "--no-network", "e2fsprogs-extra") >= 0 {
			t.Errorf("a cold cache must not be read offline:\n%s", strings.Join(calls, "\n"))
		}
		if strings.Contains(out, "failed") {
			t.Errorf("unexpected failure in output:\n%s", out)
		}
	})
	t.Run("warm cache uses no network", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.apk()
		h.mark("full")
		h.warmCache("e2fsprogs-extra", "e2fsprogs-libs", "libcom_err", "nftables", "libmnl")
		out, code := h.run(`prepare_data_disk`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		calls := h.callLog()
		for _, c := range calls {
			if strings.HasPrefix(c, "apk") && !strings.Contains(c, "--no-network") {
				t.Errorf("warm cache still used the network fallback: %s", c)
			}
		}
		if i, r := index(calls, "apk", "--no-network", "e2fsprogs-extra"), index(calls, "resize2fs"); i < 0 || r < i {
			t.Errorf("want offline tool install before resize2fs:\n%s", strings.Join(calls, "\n"))
		}
	})
	t.Run("usage logged after mount and growth", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.apk()
		out, code := h.run(`prepare_data_disk`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		for _, want := range []string{"after mount", "after growth"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		calls := h.callLog()
		resize := index(calls, "resize2fs")
		if resize < 0 {
			t.Fatalf("resize2fs did not run:\n%s", strings.Join(calls, "\n"))
		}
		for _, flag := range []string{"df -k", "df -i"} {
			if index(calls[:resize], flag) < 0 || index(calls[resize:], flag) < 0 {
				t.Errorf("want %q before and after resize2fs:\n%s", flag, strings.Join(calls, "\n"))
			}
		}
	})
	t.Run("resize failure is fatal", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.apk()
		h.mark("full")
		h.mark("resize-fails")
		out, code := h.run(`prepare_data_disk`)
		if code == 0 {
			t.Fatalf("resize failure was ignored:\n%s", out)
		}
		calls := h.callLog()
		if index(calls, "poweroff -f") < 0 || index(calls, "nftables") >= 0 {
			t.Errorf("want poweroff and no package install after a failed resize:\n%s", strings.Join(calls, "\n"))
		}
		if !strings.Contains(out, "resize") {
			t.Errorf("output does not explain the failure:\n%s", out)
		}
	})
	t.Run("resize tool unavailable is fatal", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.apk()
		h.mark("full")
		h.mark("no-network")
		out, code := h.run(`prepare_data_disk`)
		if code == 0 {
			t.Fatalf("missing resize2fs was ignored:\n%s", out)
		}
		calls := h.callLog()
		if index(calls, "resize2fs") >= 0 || index(calls, "nftables") >= 0 || index(calls, "poweroff -f") < 0 {
			t.Errorf("want poweroff before any resize or package install:\n%s", strings.Join(calls, "\n"))
		}
	})
	t.Run("fresh disk is formatted and not resized", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.apk()
		h.stub("blkid", ``)
		out, code := h.run(`prepare_data_disk`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		calls := h.callLog()
		if mkfs, mount := index(calls, "mkfs.ext4"), index(calls, "mount -t ext4"); mkfs < 0 || mount < mkfs {
			t.Errorf("want mkfs before mount:\n%s", strings.Join(calls, "\n"))
		}
		if index(calls, "resize2fs") >= 0 {
			t.Errorf("a freshly formatted disk needs no resize:\n%s", strings.Join(calls, "\n"))
		}
	})
}

// TestInitOfflineRestartAfterFirstBoot boots a builder twice with the root
// filesystem reset in between: the second boot has no network, so nftables,
// and the resize tool with its dependencies when the disk grew, must come from
// the APK cache that the first boot left on the data disk. An unchanged disk
// needs no resize tool at all.
func TestInitOfflineRestartAfterFirstBoot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		grown bool
	}{
		{name: "unchanged disk"},
		{name: "grown full disk", grown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newInitHarness(t)
			h.apk()
			h.stub("blkid", ``)
			out, code := h.run(`prepare_data_disk`)
			if code != 0 {
				t.Fatalf("first boot: exit %d, output:\n%s", code, out)
			}
			first := h.callLog()
			// Before the data disk exists the packages go to the staging
			// cache on the root filesystem, never to the data disk.
			install, mkfs, mount := index(first, "apk", "--cache-dir "+h.stageDir(), "e2fsprogs", "e2fsprogs-extra"), index(first, "mkfs.ext4"), index(first, "mount -t ext4")
			if install < 0 || mkfs < install || mount < mkfs {
				t.Fatalf("want staged e2fsprogs install < mkfs < mount, got %d %d %d in:\n%s", install, mkfs, mount, strings.Join(first, "\n"))
			}
			for i, c := range first[:mount] {
				if strings.Contains(c, h.cacheDir()) {
					t.Errorf("first boot call %d uses the data disk cache before it is mounted: %s", i, c)
				}
			}
			if index(first, "resize2fs") >= 0 {
				t.Errorf("a freshly formatted disk is not resized:\n%s", strings.Join(first, "\n"))
			}
			// Everything the resize tool needs, dependencies the mkfs
			// install already put on the root filesystem included, must
			// have reached the persistent cache.
			cached := strings.Join(h.cached(), " ")
			for _, want := range []string{"e2fsprogs-extra.apk", "e2fsprogs-libs.apk", "nftables.apk", "libmnl.apk"} {
				if !strings.Contains(cached, want) {
					t.Errorf("persistent cache lacks %s: %s", want, cached)
				}
			}

			// Second boot: new root filesystem, same data disk, no network.
			h.restartRoot()
			h.stub("blkid", `echo ext4`)
			h.mark("no-network")
			script := "prepare_data_disk"
			if tt.grown {
				// The host raised docker_disk or builder_disk by 1 GiB.
				h.mark("full")
				script = "DISK_SIZE=$((DISK_SIZE + (1 << 30)))\n" + script
			}
			out, code = h.run(script)
			if code != 0 {
				t.Fatalf("offline second boot: exit %d, output:\n%s", code, out)
			}
			calls := h.callLog()
			for _, c := range calls {
				if strings.HasPrefix(c, "apk") && !strings.Contains(c, "--no-network") {
					t.Errorf("offline boot used the network: %s", c)
				}
			}
			tool, resize, nft := index(calls, "apk", "--no-network", "e2fsprogs-extra"), index(calls, "resize2fs"), index(calls, "apk", "--no-network", "nftables")
			switch {
			case tt.grown && (tool < 0 || resize < tool || nft < resize):
				t.Errorf("want offline tool install < resize2fs < offline nftables install, got %d %d %d in:\n%s", tool, resize, nft, strings.Join(calls, "\n"))
			case !tt.grown && (tool >= 0 || resize >= 0 || nft < 0):
				t.Errorf("an unchanged disk needs no resize tool or resize2fs, only offline nftables, got %d %d %d in:\n%s", tool, resize, nft, strings.Join(calls, "\n"))
			}
			if index(calls, "poweroff") >= 0 || index(calls, "mkfs.ext4") >= 0 {
				t.Errorf("offline boot powered off or reformatted:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// TestInitHealsCacheWithoutResizeTool covers a data disk whose APK cache
// predates the resize tool (only nftables): the boot that needs the tool uses
// the network, staged on the root filesystem, and leaves the tool and its
// dependencies in the persistent cache for the offline boots after it.
func TestInitHealsCacheWithoutResizeTool(t *testing.T) {
	t.Parallel()
	h := newInitHarness(t)
	h.apk()
	h.warmCache("nftables", "libmnl")
	h.mark("full")
	out, code := h.run(`prepare_data_disk`)
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	calls := h.callLog()
	offline, stage, resize := index(calls, "apk", "--no-network", "e2fsprogs-extra"), index(calls, "apk", "--cache-dir "+h.stageDir(), "e2fsprogs-extra"), index(calls, "resize2fs")
	if offline < 0 || stage < offline || resize < stage {
		t.Fatalf("want offline attempt < staged network install < resize2fs, got %d %d %d in:\n%s", offline, stage, resize, strings.Join(calls, "\n"))
	}

	h.restartRoot()
	if err := os.Remove(filepath.Join(h.state, "full")); err != nil {
		t.Fatal(err)
	}
	h.mark("no-network")
	out, code = h.run(`prepare_data_disk`)
	if code != 0 {
		t.Fatalf("offline boot after the healing boot: exit %d, output:\n%s", code, out)
	}
}

// TestInitGrowthGuard covers the measurement that decides whether the file
// system must grow: the ext4 superblock of the device file, read by the real
// od, against DISK_SIZE, allowing for the 1 MiB tail the format leaves. Every
// boot here is offline with a complete package cache, so a needless resize
// shows as a call, and growth as a resize2fs call.
func TestInitGrowthGuard(t *testing.T) {
	t.Parallel()
	const (
		mib     = 1 << 20
		fourTiB = 4 << 40 // 1 KiB blocks, high half 1, low half 0
	)
	tests := []struct {
		name      string
		diskSize  int64  // 0: the harness default
		blockSize uint64 // 0: 4096
		fsBytes   uint64 // file system size, a multiple of the block size
		blocks    uint64 // when set, used instead of fsBytes
		incompat  uint32
		grow      bool
	}{
		{name: "file system at the 1 MiB tail", fsBytes: testDiskSize - mib},
		{name: "file system fills the disk", fsBytes: testDiskSize},
		{name: "file system larger than the disk", fsBytes: testDiskSize + 4096},
		{name: "one block more than the tail", fsBytes: testDiskSize - mib - 4096, grow: true},
		{name: "a gibibyte short", fsBytes: testDiskSize - 1<<30, grow: true},
		{name: "1 KiB blocks at the tail", blockSize: 1024, fsBytes: testDiskSize - mib},
		{name: "1 KiB blocks, one block more than the tail", blockSize: 1024, fsBytes: testDiskSize - mib - 1024, grow: true},
		{name: "2 KiB blocks at the tail", blockSize: 2048, fsBytes: testDiskSize - mib},
		{name: "2 KiB blocks, one block more than the tail", blockSize: 2048, fsBytes: testDiskSize - mib - 2048, grow: true},
		{name: "4 KiB blocks at the tail", blockSize: 4096, fsBytes: testDiskSize - mib},
		{name: "64bit feature, high half counts", diskSize: fourTiB + mib, blockSize: 1024, blocks: 1 << 32, incompat: featureIncompat64Bit},
		{name: "64bit feature, high half counts, growth", diskSize: fourTiB + 2*mib, blockSize: 1024, blocks: 1 << 32, incompat: featureIncompat64Bit, grow: true},
		{name: "high half ignored without the 64bit feature", diskSize: fourTiB + mib, blockSize: 1024, blocks: 1 << 32, grow: true},
		{name: "other incompat bits do not enable the high half", diskSize: fourTiB + mib, blockSize: 1024, blocks: 1 << 32, incompat: 0x2c7f, grow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newInitHarness(t)
			h.apk()
			h.warmCache("e2fsprogs-extra", "e2fsprogs-libs", "libcom_err", "nftables", "libmnl")
			h.mark("no-network")
			blockSize := tt.blockSize
			if blockSize == 0 {
				blockSize = 4096
			}
			logBlockSize := uint32(0)
			for 1024<<logBlockSize < blockSize {
				logBlockSize++
			}
			blocks := tt.blocks
			if blocks == 0 {
				blocks = tt.fsBytes / blockSize
			}
			h.setFS(blocks, logBlockSize, tt.incompat)
			script := "prepare_data_disk"
			if tt.diskSize != 0 {
				script = fmt.Sprintf("DISK_SIZE=%d\n%s", tt.diskSize, script)
			}
			out, code := h.run(script)
			if code != 0 {
				t.Fatalf("exit %d, output:\n%s", code, out)
			}
			calls := h.callLog()
			tool, resize := index(calls, "e2fsprogs-extra"), index(calls, "resize2fs")
			if tt.grow && (tool < 0 || resize < tool) {
				t.Errorf("want the resize tool install < resize2fs, got %d %d in:\n%s", tool, resize, strings.Join(calls, "\n"))
			}
			if !tt.grow && (tool >= 0 || resize >= 0) {
				t.Errorf("no growth needed, but the resize tool or resize2fs ran:\n%s", strings.Join(calls, "\n"))
			}
			if index(calls, "poweroff") >= 0 || index(calls, "apk", "--no-network", "nftables") < 0 {
				t.Errorf("want offline nftables install and no poweroff:\n%s", strings.Join(calls, "\n"))
			}
			for _, c := range calls {
				if strings.HasPrefix(c, "apk") && !strings.Contains(c, "--no-network") {
					t.Errorf("offline boot used the network: %s", c)
				}
			}
		})
	}
}

// TestInitLegacyCacheOfflineBoot is a data disk from before the resize tool
// was cached, restarted offline: when the file system already fills the disk
// the incomplete cache must not matter, when it has to grow the missing tool
// is fatal before anything is installed.
func TestInitLegacyCacheOfflineBoot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cache   []string
		fsBytes uint64
		grow    bool
	}{
		{name: "cache lacks the tool, unchanged", cache: []string{"nftables", "libmnl"}, fsBytes: testDiskSize - 1<<20},
		{name: "cache has half the tool, unchanged", cache: []string{"e2fsprogs-extra", "nftables", "libmnl"}, fsBytes: testDiskSize - 1<<20},
		{name: "cache lacks the tool, growth required", cache: []string{"nftables", "libmnl"}, fsBytes: testDiskSize - 1<<30, grow: true},
		{name: "cache has half the tool, growth required", cache: []string{"e2fsprogs-extra", "nftables", "libmnl"}, fsBytes: testDiskSize - 1<<30, grow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newInitHarness(t)
			h.apk()
			h.warmCache(tt.cache...)
			h.mark("no-network")
			h.setFS(tt.fsBytes/4096, 2, 0)
			out, code := h.run(`prepare_data_disk`)
			calls := h.callLog()
			if !tt.grow {
				if code != 0 || index(calls, "poweroff") >= 0 || index(calls, "resize2fs") >= 0 || index(calls, "apk", "--no-network", "nftables") < 0 {
					t.Fatalf("an unchanged legacy disk must boot offline: exit %d, output:\n%s\ncalls:\n%s", code, out, strings.Join(calls, "\n"))
				}
				return
			}
			if code == 0 || index(calls, "poweroff -f") < 0 || index(calls, "resize2fs") >= 0 || index(calls, "nftables") >= 0 {
				t.Fatalf("required growth without the resize tool must be fatal before any install: exit %d, output:\n%s\ncalls:\n%s", code, out, strings.Join(calls, "\n"))
			}
		})
	}
}

// TestInitGrowthRetriesWhileFilesystemIsSmall: nothing remembers that a boot
// already tried to grow, the file system itself says it is still too small.
func TestInitGrowthRetriesWhileFilesystemIsSmall(t *testing.T) {
	t.Parallel()
	h := newInitHarness(t)
	h.apk()
	h.warmCache("e2fsprogs-extra", "e2fsprogs-libs", "libcom_err", "nftables", "libmnl")
	h.mark("no-network")
	h.mark("resize-fails")
	if out, code := h.run(`prepare_data_disk`); code == 0 {
		t.Fatalf("failed resize was ignored:\n%s", out)
	}
	for boot := 2; boot <= 3; boot++ {
		h.restartRoot()
		if err := os.Remove(filepath.Join(h.state, "resize-fails")); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		out, code := h.run(`prepare_data_disk`)
		if code != 0 || index(h.callLog(), "resize2fs") < 0 {
			t.Fatalf("boot %d: want a retried resize2fs, exit %d, output:\n%s\ncalls:\n%s", boot, code, out, strings.Join(h.callLog(), "\n"))
		}
	}
}

// TestInitMeasurementFailureIsFatal: a superblock that cannot be read is not
// "no growth needed".
func TestInitMeasurementFailureIsFatal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		breakDev func(t *testing.T, h *initHarness)
	}{
		{name: "device missing", breakDev: func(t *testing.T, h *initHarness) {
			t.Helper()
			if err := os.Remove(h.dev); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "device empty", breakDev: func(t *testing.T, h *initHarness) {
			t.Helper()
			if err := os.WriteFile(h.dev, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "device shorter than the superblock fields", breakDev: func(t *testing.T, h *initHarness) {
			t.Helper()
			if err := os.WriteFile(h.dev, make([]byte, 1030), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newInitHarness(t)
			h.apk()
			h.warmCache("e2fsprogs-extra", "e2fsprogs-libs", "libcom_err", "nftables", "libmnl")
			tt.breakDev(t, h)
			out, code := h.run(`prepare_data_disk`)
			calls := h.callLog()
			if code == 0 || index(calls, "poweroff -f") < 0 {
				t.Fatalf("unreadable measurement was ignored: exit %d, output:\n%s", code, out)
			}
			if index(calls, "apk") >= 0 || index(calls, "resize2fs") >= 0 {
				t.Errorf("nothing may be installed or resized after a failed measurement:\n%s", strings.Join(calls, "\n"))
			}
			if !strings.Contains(out, "file system size") {
				t.Errorf("output does not explain the failure:\n%s", out)
			}
		})
	}
}

func TestInitFormatsFreshDiskLeavingTail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		diskSize int64
		blocks   string
	}{
		{name: "1 GiB", diskSize: 1 << 30, blocks: "261888"},
		{name: "2 GiB", diskSize: 2 << 30, blocks: "524032"},
		{name: "20 GiB", diskSize: 20 << 30, blocks: "5242624"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newInitHarness(t)
			h.apk()
			h.stub("blkid", ``)
			out, code := h.run(fmt.Sprintf("DISK_SIZE=%d\nprepare_data_disk", tt.diskSize))
			if code != 0 {
				t.Fatalf("exit %d, output:\n%s", code, out)
			}
			calls := h.callLog()
			mkfs := index(calls, "mkfs.ext4")
			want := "mkfs.ext4 -q -F -b 4096 -E nodiscard " + h.dev + " " + tt.blocks
			if mkfs < 0 || calls[mkfs] != want {
				t.Fatalf("mkfs call index %d, want %q in:\n%s", mkfs, want, strings.Join(calls, "\n"))
			}
			// The file system must end 1 MiB before the disk does.
			fields := strings.Fields(calls[mkfs])
			blocks, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
			if err != nil {
				t.Fatalf("mkfs block count: %v", err)
			}
			if tail := tt.diskSize - blocks*4096; tail != 1<<20 {
				t.Errorf("tail after the file system = %d bytes, want %d", tail, 1<<20)
			}
			mount, pkg := index(calls, "mount -t ext4"), index(calls, "nftables")
			if mount < mkfs || pkg < mount {
				t.Errorf("want mkfs < mount < package install, got %d %d %d in:\n%s", mkfs, mount, pkg, strings.Join(calls, "\n"))
			}
			if index(calls, "resize2fs") >= 0 {
				t.Errorf("a freshly formatted disk is not resized:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

func TestInitDaemonConfig(t *testing.T) {
	t.Parallel()
	h := newInitHarness(t)
	out, code := h.run(`write_daemon_config`)
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	data, err := os.ReadFile(filepath.Join(h.root, "run", "jailoc-dockerd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Builder struct {
			GC struct {
				Enabled              bool   `json:"enabled"`
				DefaultMaxUsedSpace  string `json:"defaultMaxUsedSpace"`
				DefaultMinFreeSpace  string `json:"defaultMinFreeSpace"`
				DefaultReservedSpace string `json:"defaultReservedSpace"`
				DefaultKeepStorage   string `json:"defaultKeepStorage"`
			} `json:"gc"`
		} `json:"builder"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("daemon config is not JSON: %v\n%s", err, data)
	}
	gc := cfg.Builder.GC
	// 20 GiB disk: GC targets keep the cache at or below 50% of the disk and
	// 20% free, as absolute bytes. They are GC targets, not hard quotas. The
	// reserved floor is explicit zero so it cannot raise the targets.
	if !gc.Enabled || gc.DefaultMaxUsedSpace != "10737418240" || gc.DefaultMinFreeSpace != "4294967296" || gc.DefaultReservedSpace != "0" || gc.DefaultKeepStorage != "" {
		t.Errorf("builder.gc = %+v from\n%s", gc, data)
	}
	if strings.Contains(string(data), "%") {
		t.Errorf("daemon config uses percentages:\n%s", data)
	}
}

func TestInitRunDockerd(t *testing.T) {
	t.Parallel()
	t.Run("unexpected exit", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.stub("dockerd", `exit 3`)
		out, code := h.run(`write_daemon_config; run_dockerd`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		if !strings.Contains(out, "dockerd exited unexpectedly with status 3") || strings.Contains(out, "shutdown requested") {
			t.Errorf("unexpected exit not reported:\n%s", out)
		}
		calls := h.callLog()
		cfg := "--config-file " + filepath.Join(h.root, "run", "jailoc-dockerd.json")
		if index(calls, "dockerd", cfg, "--data-root "+filepath.Join(h.root, "docker")) < 0 {
			t.Errorf("dockerd not started with the generated config:\n%s", strings.Join(calls, "\n"))
		}
		if index(calls, "df -k") < 0 || index(calls, "df -i") < 0 || !strings.Contains(out, "after dockerd exit") {
			t.Errorf("block and inode usage not logged after the exit:\n%s", out)
		}
	})
	t.Run("requested shutdown", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.stub("dockerd", `trap 'exit 0' TERM
touch "$CONTROL/shutdown"
while :; do sleep 0.1; done`)
		out, code := h.run(`write_daemon_config; run_dockerd`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		if !strings.Contains(out, "shutdown requested") || !strings.Contains(out, "stopped after shutdown request (exit status 0)") {
			t.Errorf("shutdown not reported:\n%s", out)
		}
		if strings.Contains(out, "unexpectedly") || index(h.callLog(), "df") >= 0 {
			t.Errorf("a user shutdown was reported as a failure:\n%s", out)
		}
	})
	t.Run("dockerd ignores SIGTERM", func(t *testing.T) {
		t.Parallel()
		h := newInitHarness(t)
		h.stub("dockerd", `trap '' TERM
touch "$CONTROL/shutdown"
while :; do sleep 0.1; done`)
		out, code := h.run(`write_daemon_config; run_dockerd`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		if !strings.Contains(out, "ignored SIGTERM") || !strings.Contains(out, "exit status 137") {
			t.Errorf("forced stop not reported:\n%s", out)
		}
	})
}
