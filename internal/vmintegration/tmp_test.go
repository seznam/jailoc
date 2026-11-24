//go:build vmintegration

package vmintegration

// Tests of the workspace /tmp contract: tmp_storage (ram|disk), tmp_size,
// tmp_ttl, the mount properties of /tmp, the "Tmp:" line of `jailoc status`,
// `jailoc-guest tmp-status`, the TMPDIR family of session variables, the
// TTL sweep and the discard of freed blocks of the host's tmp.img.
//
// They reuse TestMain, the helpers and the workspace name of vm_test.go but
// write their own configuration (writeTmpConfig) and purge the workspace
// after each test. The tests run one after another in the shared test home;
// none of them is parallel. Run only these with:
//
//	go test -tags vmintegration -v -timeout 45m -run 'TestTmp' ./internal/vmintegration/
//
// TestTmpTTLSweepAndDiscard relies on the 5-second sweep interval enabled in
// vmintegration builds of jailoc-guest.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	tmpMiB = int64(1) << 20
	tmpGiB = int64(1) << 30

	// tmpSweepWait is how long the TTL test waits for the sweep; vmintegration
	// builds of jailoc-guest run the cleaner every 5 seconds instead of 15 minutes.
	tmpSweepWait = 1 * time.Minute
	// tmpDiscardGrace is the extra time the freed blocks of tmp.img get to
	// reach the host after the sweep was seen.
	tmpDiscardGrace = 1 * time.Minute

	tmpPollInterval = 1 * time.Second
)

// tmpWorkspace is the workspace the /tmp tests boot.
type tmpWorkspace struct {
	// Memory is the VM memory, "3g" when empty.
	Memory string
	// Docker sets enable_docker; it is written here and nowhere else.
	Docker bool
	// ProtectGit sets protect_git (sudo is disabled in the guest).
	ProtectGit bool
	// Settings are further workspace keys, one TOML line each: tmp_storage,
	// tmp_size, tmp_ttl, env. They must not set enable_docker or protect_git.
	Settings string
}

// writeTmpConfig writes the configuration of one /tmp test. enable_docker
// and protect_git are written exactly once, from the struct fields.
func writeTmpConfig(t *testing.T, ws tmpWorkspace) {
	t.Helper()
	for _, key := range []string{"enable_docker", "protect_git"} {
		if strings.Contains(ws.Settings, key) {
			t.Fatalf("tmpWorkspace.Settings must not contain %s; use the struct field", key)
		}
	}
	memory := ws.Memory
	if memory == "" {
		memory = "3g"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[vm]\nrunner = %q\n", os.Getenv("JAILOC_VM_RUNNER"))
	if libDir := os.Getenv("JAILOC_VM_LIBDIR"); libDir != "" {
		fmt.Fprintf(&b, "lib_dir = %q\n", libDir)
	}
	fmt.Fprintf(&b, "\n[workspaces.%s]\nbackend = \"vm\"\npaths = [%q]\ncpu = 2.0\nmemory = %q\nenable_docker = %t\nprotect_git = %t\n%s\n",
		workspaceName, project, memory, ws.Docker, ws.ProtectGit, ws.Settings)

	dir := filepath.Join(home, ".config", "jailoc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(b.String()), 0o600); err != nil { //nolint:gosec // G703: test directory
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
}

// tmpJailoc runs the jailoc binary in the test home and returns its
// combined output; unlike jailoc it leaves failures to the caller.
func tmpJailoc(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...) //nolint:gosec // G204: the jailoc binary under test
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tmpImagePath() string {
	return filepath.Join(stateDir(), "tmp.img")
}

// tmpRunnerPIDs lists the VM runner processes of the test workspace. The
// runner shows up as "jailoc-<workspace>"; the match is anchored so VMs of
// the user's own workspaces are never matched.
func tmpRunnerPIDs() ([]string, error) {
	out, err := exec.Command("pgrep", "-f", "^jailoc-"+workspaceName+"( |$)").Output() //nolint:gosec // G204: fixed arguments
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pgrep: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// startTmpWorkspace writes the configuration, boots the workspace and
// returns the test context. The cleanup runs after the context is cancelled:
// it stops the VM, requires its runner to be gone, then purges the state,
// which deletes tmp.img.
func startTmpWorkspace(t *testing.T, ws tmpWorkspace, timeout time.Duration) context.Context {
	t.Helper()
	writeTmpConfig(t, ws)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	// Cleanups run last-in first-out: cancel is registered second so the test
	// context is done before the VM is stopped and the image deleted.
	t.Cleanup(func() {
		if t.Failed() {
			dumpLogs(t)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer ccancel()
		if out, err := tmpJailoc(cctx, "down", workspaceName); err != nil {
			t.Logf("jailoc down: %v\n%s", err, tail([]byte(out), 2000))
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			pids, err := tmpRunnerPIDs()
			if err != nil {
				t.Logf("cannot check for leftover runners: %v", err)
				break
			}
			if len(pids) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("VM runner of %s still running after down: pids %v", workspaceName, pids)
				break
			}
			time.Sleep(time.Second)
		}
		if out, err := tmpJailoc(cctx, "down", "--purge", workspaceName); err != nil {
			t.Errorf("jailoc down --purge: %v\n%s", err, tail([]byte(out), 2000))
		}
		if _, err := os.Stat(tmpImagePath()); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("tmp.img after purge: %v, want it deleted", err)
		}
	})
	t.Cleanup(cancel)

	start := time.Now()
	jailoc(ctx, t, "", "up", workspaceName)
	t.Logf("up: %s", time.Since(start).Round(time.Second))
	return ctx
}

func tmpSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// tmpImageBytes returns the host bytes allocated to tmp.img (not its
// apparent size).
func tmpImageBytes(t *testing.T) int64 {
	t.Helper()
	info, err := os.Stat(tmpImagePath())
	if err != nil {
		t.Fatalf("stat tmp.img: %v", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("no stat_t for tmp.img")
	}
	return st.Blocks * 512
}

// checkTmpImageSize requires the logical size of tmp.img to be exactly size:
// a trim that truncates the image, as a libkrun EOF handling bug would, is a
// failure even when the guest looks fine.
func checkTmpImageSize(t *testing.T, size int64) {
	t.Helper()
	info, err := os.Stat(tmpImagePath())
	if err != nil {
		t.Fatalf("stat tmp.img: %v", err)
	}
	if info.Size() != size {
		t.Fatalf("tmp.img logical size = %d, want %d", info.Size(), size)
	}
}

// tmpWriteSentinel writes 64 KiB of random data to path, optionally dates it
// far into the future so that a TTL sweep keeps it, and returns its SHA-256.
func tmpWriteSentinel(ctx context.Context, t *testing.T, path string, future bool) string {
	t.Helper()
	cmd := fmt.Sprintf("head -c 65536 /dev/urandom > %[1]s && sync", path)
	if future {
		cmd += fmt.Sprintf(" && touch -d @4102444800 %s", path)
	}
	mustGuest(ctx, t, cmd)
	return mustGuest(ctx, t, fmt.Sprintf("sha256sum %s | cut -d' ' -f1", path))
}

// tmpCheckSentinel drops the guest page cache, so the data is read back from
// the disk image, and compares the checksum of path with want. It needs sudo.
func tmpCheckSentinel(ctx context.Context, t *testing.T, path, want string) {
	t.Helper()
	mustGuest(ctx, t, "sync; sudo -n sh -c 'echo 3 > /proc/sys/vm/drop_caches'")
	if got := mustGuest(ctx, t, fmt.Sprintf("sha256sum %s | cut -d' ' -f1", path)); got != want {
		t.Fatalf("checksum of %s = %q after the trim, want %q", path, got, want)
	}
}

// tmpTTLShown matches the TTL description in the "Tmp:" status line:
// "cleanup after <ttl>" or "no cleanup" when disabled.
func tmpTTLShown(ttl string) *regexp.Regexp {
	if ttl == "0" {
		return regexp.MustCompile(`\bno cleanup\b`)
	}
	return regexp.MustCompile(`\bcleanup after ` + regexp.QuoteMeta(ttl) + `\b`)
}

// tmpUsage is the JSON of `jailoc-guest tmp-status`.
type tmpUsage struct {
	CapacityBytes  *int64 `json:"capacity_bytes"`
	UsedBytes      *int64 `json:"used_bytes"`
	AvailableBytes *int64 `json:"available_bytes"`
}

// guestTmpUsage runs the guest's tmp-status command and returns capacity,
// used and available bytes of /tmp.
func guestTmpUsage(ctx context.Context, t *testing.T) (capacity, used, available int64) {
	t.Helper()
	out := mustGuest(ctx, t, "/.jailoc/jailoc-guest tmp-status")
	var u tmpUsage
	if err := json.Unmarshal([]byte(out), &u); err != nil {
		t.Fatalf("tmp-status is not JSON: %v\n%s", err, out)
	}
	if u.CapacityBytes == nil || u.UsedBytes == nil || u.AvailableBytes == nil {
		t.Fatalf("tmp-status lacks capacity_bytes, used_bytes or available_bytes: %s", out)
	}
	if *u.CapacityBytes <= 0 || *u.UsedBytes < 0 || *u.AvailableBytes < 0 {
		t.Fatalf("tmp-status has implausible values: %s", out)
	}
	if *u.UsedBytes > *u.CapacityBytes || *u.AvailableBytes > *u.CapacityBytes {
		t.Fatalf("tmp-status used or available exceed the capacity: %s", out)
	}
	return *u.CapacityBytes, *u.UsedBytes, *u.AvailableBytes
}

// tmpCapacityWithin checks the reported capacity of /tmp against the
// configured tmp_size. A RAM tmpfs reports the size itself; a disk /tmp
// loses part of it to filesystem metadata, so it is strictly below the size
// but still at least 90% of it.
func tmpCapacityWithin(t *testing.T, backend string, size, capacity int64) {
	t.Helper()
	if capacity*10 < size*9 {
		t.Fatalf("%s capacity %d is below 90%% of tmp_size %d", backend, capacity, size)
	}
	switch backend {
	case "disk":
		if capacity >= size {
			t.Fatalf("disk capacity %d, want below tmp_size %d (filesystem metadata)", capacity, size)
		}
	default:
		if capacity > size {
			t.Fatalf("ram capacity %d exceeds tmp_size %d", capacity, size)
		}
	}
}

// tmpStatusLine returns the "Tmp:" line of `jailoc status`.
func tmpStatusLine(ctx context.Context, t *testing.T) string {
	t.Helper()
	out := jailoc(ctx, t, "", "status", workspaceName)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Tmp:") {
			return line
		}
	}
	t.Fatalf("status has no Tmp: line:\n%s", out)
	return ""
}

// checkTmpStatusLine requires the backend, the used and the free amount in
// the "Tmp:" line.
func checkTmpStatusLine(ctx context.Context, t *testing.T, backend string) string {
	t.Helper()
	line := tmpStatusLine(ctx, t)
	lower := strings.ToLower(line)
	for _, want := range []string{backend, "used", "free"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("status line %q lacks %q", line, want)
		}
	}
	return line
}

// checkTmpMount checks the mount of /tmp: device (tmpfs for RAM), the
// nosuid and nodev options, no noexec, strict atime and mode 1777.
func checkTmpMount(ctx context.Context, t *testing.T, backend, wantSource string) {
	t.Helper()
	fields := strings.Fields(mustGuest(ctx, t, "findmnt -n -o SOURCE,FSTYPE,OPTIONS /tmp"))
	if len(fields) != 3 {
		t.Fatalf("findmnt /tmp = %q, want source, fstype and options", fields)
	}
	source, fstype := fields[0], fields[1]
	options := map[string]bool{}
	for _, o := range strings.Split(fields[2], ",") {
		options[o] = true
	}
	if backend == "ram" {
		if fstype != "tmpfs" {
			t.Fatalf("ram /tmp is %s on %s, want tmpfs", fstype, source)
		}
	} else if fstype == "tmpfs" || source != wantSource {
		t.Fatalf("disk /tmp is %s on %s, want a disk filesystem on %s", fstype, source, wantSource)
	}
	for _, want := range []string{"rw", "nosuid", "nodev"} {
		if !options[want] {
			t.Fatalf("/tmp options %q lack %s", fields[2], want)
		}
	}
	// strictatime is the absence of the other atime options.
	for _, bad := range []string{"noexec", "ro", "relatime", "noatime", "nodiratime", "lazytime"} {
		if options[bad] {
			t.Fatalf("/tmp options %q contain %s", fields[2], bad)
		}
	}
	if got := mustGuest(ctx, t, "stat -c %a /tmp"); got != "1777" {
		t.Fatalf("/tmp mode = %s, want 1777", got)
	}
	// Executable: a script runs from /tmp.
	if got := mustGuest(ctx, t, "printf '#!/bin/sh\\necho exec-ok\\n' > /tmp/exec-probe && chmod +x /tmp/exec-probe && /tmp/exec-probe; rm -f /tmp/exec-probe"); got != "exec-ok" {
		t.Fatalf("script in /tmp = %q, want exec-ok", got)
	}
	// strict atime: relatime would not update the atime on the second read,
	// the file being newer than its last access.
	if got := mustGuest(ctx, t, `python3 - <<'PY'
import os, time
p = "/tmp/atime-probe"
with open(p, "w") as f:
    f.write("x")
time.sleep(1.1)
open(p).read()
a1 = os.stat(p).st_atime_ns
time.sleep(1.1)
open(p).read()
a2 = os.stat(p).st_atime_ns
os.remove(p)
assert a2 > a1, (a1, a2)
print("strictatime-ok")
PY`); got != "strictatime-ok" {
		t.Fatalf("atime probe = %q", got)
	}
}

// tmpWriteZeros writes mib MiB of zeroes into path and syncs.
func tmpWriteZeros(ctx context.Context, t *testing.T, path string, mib int64) {
	t.Helper()
	mustGuest(ctx, t, fmt.Sprintf("dd if=/dev/zero of=%s bs=1M count=%d conv=fsync status=none", path, mib))
}

// checkTmpBigFile writes more than the old fixed 1g /tmp limit (1.5g),
// checks the usage the guest reports and that removing the file gives it
// back. The file does not outlive the check.
func checkTmpBigFile(ctx context.Context, t *testing.T, path string) {
	t.Helper()
	const mib = 1536
	_, usedBefore, _ := guestTmpUsage(ctx, t)
	tmpWriteZeros(ctx, t, path, mib)
	_, usedDuring, _ := guestTmpUsage(ctx, t)
	if usedDuring-usedBefore < mib*tmpMiB {
		t.Fatalf("used grew by %d bytes after writing %d MiB", usedDuring-usedBefore, mib)
	}
	mustGuest(ctx, t, "rm -f "+path+"; sync")
	_, usedAfter, _ := guestTmpUsage(ctx, t)
	if usedAfter > usedBefore+64*tmpMiB {
		t.Fatalf("used = %d after removing the file, was %d before writing it", usedAfter, usedBefore)
	}
}

// tmpSessionEnv returns "$TMPDIR $TMP $TEMP $GOTMPDIR" of a host-started
// `jailoc exec` session and of `jailoc-guest run`.
func tmpSessionEnv(ctx context.Context, t *testing.T) (execEnv, runEnv string) {
	t.Helper()
	const show = `echo "$TMPDIR $TMP $TEMP $GOTMPDIR"`
	execEnv = strings.TrimSpace(jailoc(ctx, t, "", "exec", "--", "sh", "-c", show))
	runEnv = mustGuest(ctx, t, "/.jailoc/jailoc-guest run -- sh -c '"+show+"'")
	return execEnv, runEnv
}

// checkTmpReset requires the scratch /tmp of a restarted VM to start empty.
func checkTmpReset(ctx context.Context, t *testing.T, backend, wantSource string) {
	t.Helper()
	mustGuest(ctx, t, "echo scratch > /tmp/reset-marker && sync")
	jailoc(ctx, t, "", "restart", workspaceName)
	if out, err := guest(ctx, t, "test -e /tmp/reset-marker"); err == nil {
		t.Fatalf("/tmp content survived the restart: %s", out)
	}
	checkTmpMount(ctx, t, backend, wantSource)
}

func TestTmpConfigValidation(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		wantErr  string // substring of the error; empty for a valid configuration
	}{
		{name: "defaults"},
		{name: "ram", settings: `tmp_storage = "ram"`},
		{name: "disk", settings: `tmp_storage = "disk"`},
		{name: "size in MiB", settings: `tmp_size = "512m"`},
		{name: "size and ttl", settings: "tmp_size = \"2g\"\ntmp_ttl = \"8h\""},
		{name: "ttl zero disables", settings: `tmp_ttl = "0"`},
		{name: "ttl in minutes", settings: `tmp_ttl = "90m"`},
		{name: "ttl in seconds", settings: `tmp_ttl = "1s"`},
		{name: "disk at the minimum", settings: "tmp_storage = \"disk\"\ntmp_size = \"16m\""},
		{name: "unknown storage", settings: `tmp_storage = "nvme"`, wantErr: "tmp_storage"},
		{name: "zero size", settings: `tmp_size = "0"`, wantErr: "tmp_size"},
		{name: "size without a number", settings: `tmp_size = "abc"`, wantErr: "tmp_size"},
		{name: "negative size", settings: `tmp_size = "-1g"`, wantErr: "tmp_size"},
		{name: "disk below the minimum", settings: "tmp_storage = \"disk\"\ntmp_size = \"15m\"", wantErr: "tmp_size"},
		{name: "negative ttl", settings: `tmp_ttl = "-1h"`, wantErr: "tmp_ttl"},
		{name: "ttl without a duration", settings: `tmp_ttl = "soon"`, wantErr: "tmp_ttl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTmpConfig(t, tmpWorkspace{Settings: tt.settings})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			out, err := tmpJailoc(ctx, "status", workspaceName)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("valid configuration rejected: %v\n%s", err, tail([]byte(out), 2000))
			case tt.wantErr != "" && err == nil:
				t.Fatalf("invalid configuration accepted:\n%s", out)
			case tt.wantErr != "" && !strings.Contains(out, tt.wantErr):
				t.Fatalf("error does not name %s:\n%s", tt.wantErr, tail([]byte(out), 2000))
			}
		})
	}
}

// TestTmpDefaultRAM boots with no tmp_* keys: a 1g RAM /tmp that lives in the
// session environment of every command.
func TestTmpDefaultRAM(t *testing.T) {
	ctx := startTmpWorkspace(t, tmpWorkspace{}, 20*time.Minute)

	t.Run("mount", func(t *testing.T) {
		checkTmpMount(ctx, t, "ram", "")
	})

	t.Run("capacity defaults to 1g", func(t *testing.T) {
		capacity, _, _ := guestTmpUsage(ctx, t)
		tmpCapacityWithin(t, "ram", tmpGiB, capacity)
		checkTmpStatusLine(ctx, t, "ram")
	})

	t.Run("status warns at 90% full", func(t *testing.T) {
		if out := jailoc(ctx, t, "", "status", workspaceName); strings.Contains(out, "WARNING") {
			t.Fatalf("status warns on an empty /tmp:\n%s", out)
		}
		tmpWriteZeros(ctx, t, "/tmp/fill", 950) // 92.8% of 1g
		t.Cleanup(func() { _, _ = guest(ctx, t, "rm -f /tmp/fill") })
		if out := jailoc(ctx, t, "", "status", workspaceName); !strings.Contains(out, "WARNING") {
			t.Fatalf("status does not warn with /tmp 92%% full:\n%s", out)
		}
		mustGuest(ctx, t, "rm -f /tmp/fill")
		if out := jailoc(ctx, t, "", "status", workspaceName); strings.Contains(out, "WARNING") {
			t.Fatalf("status still warns after /tmp was emptied:\n%s", out)
		}
	})

	t.Run("a write past the size fails with no space left", func(t *testing.T) {
		out := mustGuest(ctx, t, "dd if=/dev/zero of=/tmp/over bs=1M count=1100 2>&1; echo rc=$?")
		_, _, available := guestTmpUsage(ctx, t)
		mustGuest(ctx, t, "rm -f /tmp/over")
		if !strings.Contains(out, "No space left on device") || strings.HasSuffix(out, "rc=0") {
			t.Fatalf("1100 MiB write into the default 1g /tmp: %s", out)
		}
		if available > 4*tmpMiB {
			t.Fatalf("available = %d bytes after the write failed with no space left", available)
		}
	})

	t.Run("session TMPDIR variables default to /tmp", func(t *testing.T) {
		execEnv, runEnv := tmpSessionEnv(ctx, t)
		for name, got := range map[string]string{"jailoc exec": execEnv, "jailoc-guest run": runEnv} {
			if got != "/tmp /tmp /tmp /tmp" {
				t.Fatalf("%s: TMPDIR TMP TEMP GOTMPDIR = %q, want all /tmp", name, got)
			}
		}
	})

	t.Run("explicit TMPDIR and GOTMPDIR are kept", func(t *testing.T) {
		writeTmpConfig(t, tmpWorkspace{Settings: `env = ["TMPDIR=/home/agent/custom-tmp", "GOTMPDIR=/home/agent/go-tmp"]`})
		if out := jailoc(ctx, t, "", "reload", workspaceName); strings.Contains(out, "jailoc restart") {
			t.Fatalf("reload asks for a restart:\n%s", out)
		}
		const want = "/home/agent/custom-tmp /tmp /tmp /home/agent/go-tmp"
		execEnv, runEnv := tmpSessionEnv(ctx, t)
		for name, got := range map[string]string{"jailoc exec": execEnv, "jailoc-guest run": runEnv} {
			if got != want {
				t.Fatalf("%s: TMPDIR TMP TEMP GOTMPDIR = %q, want %q", name, got, want)
			}
		}
	})
}

// TestTmpRAM2g checks a 2g RAM /tmp takes a file larger than the old 1g.
func TestTmpRAM2g(t *testing.T) {
	ctx := startTmpWorkspace(t, tmpWorkspace{Memory: "4g", Settings: "tmp_storage = \"ram\"\ntmp_size = \"2g\""}, 25*time.Minute)

	checkTmpMount(ctx, t, "ram", "")
	capacity, _, _ := guestTmpUsage(ctx, t)
	tmpCapacityWithin(t, "ram", 2*tmpGiB, capacity)
	checkTmpStatusLine(ctx, t, "ram")

	t.Run("write past the old 1g and remove", func(t *testing.T) {
		checkTmpBigFile(ctx, t, "/tmp/big")
	})

	t.Run("restart resets /tmp", func(t *testing.T) {
		checkTmpReset(ctx, t, "ram", "")
		capacity, _, _ := guestTmpUsage(ctx, t)
		tmpCapacityWithin(t, "ram", 2*tmpGiB, capacity)
	})
}

// TestTmpDiskProtectGit puts /tmp on the only disk (/dev/vda, no Docker) in
// a workspace whose guest has no sudo: the mount must not need root there.
// The freed blocks of tmp.img are not trimmed by hand, as there is no root
// for fstrim; the discard of them is TestTmpTTLSweepAndDiscard's.
func TestTmpDiskProtectGit(t *testing.T) {
	ctx := startTmpWorkspace(t, tmpWorkspace{ProtectGit: true, Settings: "tmp_storage = \"disk\"\ntmp_size = \"2g\""}, 25*time.Minute)

	if out, err := guest(ctx, t, "sudo -n true"); err == nil {
		t.Fatalf("sudo works in a protect_git guest: %s", out)
	}
	checkTmpMount(ctx, t, "disk", "/dev/vda")
	capacity, _, _ := guestTmpUsage(ctx, t)
	tmpCapacityWithin(t, "disk", 2*tmpGiB, capacity)
	checkTmpStatusLine(ctx, t, "disk")

	t.Run("write past the old 1g reaches the host image", func(t *testing.T) {
		const mib = 1536
		tmpWriteZeros(ctx, t, "/tmp/big", mib)
		if got := tmpImageBytes(t); got < tmpGiB {
			t.Fatalf("tmp.img has %d bytes allocated after writing %d MiB, want at least 1 GiB", got, mib)
		}
		checkTmpImageSize(t, 2*tmpGiB)
		mustGuest(ctx, t, "rm -f /tmp/big; sync")
		_, used, _ := guestTmpUsage(ctx, t)
		if used > 256*tmpMiB {
			t.Fatalf("used = %d after removing the file", used)
		}
	})

	t.Run("restart resets /tmp", func(t *testing.T) {
		checkTmpReset(ctx, t, "disk", "/dev/vda")
		if out, err := guest(ctx, t, "sudo -n true"); err == nil {
			t.Fatalf("sudo works in a protect_git guest after the restart: %s", out)
		}
	})
}

// TestTmpDiskWithDocker puts the Docker data on /dev/vda and /tmp on the
// scratch disk /dev/vdb. The guest has root, so freed blocks are trimmed by
// hand here.
func TestTmpDiskWithDocker(t *testing.T) {
	ctx := startTmpWorkspace(t, tmpWorkspace{Docker: true, Settings: "tmp_storage = \"disk\"\ntmp_size = \"2g\""}, 30*time.Minute)

	checkDisks := func(t *testing.T) {
		t.Helper()
		if got := mustGuest(ctx, t, "findmnt -n -o SOURCE /var/lib/docker"); got != "/dev/vda" {
			t.Fatalf("/var/lib/docker is on %q, want /dev/vda", got)
		}
		checkTmpMount(ctx, t, "disk", "/dev/vdb")
	}

	t.Run("disks", func(t *testing.T) {
		checkDisks(t)
		capacity, _, _ := guestTmpUsage(ctx, t)
		tmpCapacityWithin(t, "disk", 2*tmpGiB, capacity)
		checkTmpStatusLine(ctx, t, "disk")
	})

	t.Run("write, remove and FITRIM keep the image intact", func(t *testing.T) {
		const mib = 1536
		checkTmpImageSize(t, 2*tmpGiB)
		sentinel := tmpWriteSentinel(ctx, t, "/tmp/sentinel", false)
		tmpWriteZeros(ctx, t, "/tmp/big", mib)
		peak := tmpImageBytes(t)
		if peak < tmpGiB {
			t.Fatalf("tmp.img has %d bytes allocated after writing %d MiB, want at least 1 GiB", peak, mib)
		}
		mustGuest(ctx, t, "rm -f /tmp/big; sync; sudo -n fstrim /tmp")
		deadline := time.Now().Add(time.Minute)
		for tmpImageBytes(t) > peak-tmpGiB {
			if time.Now().After(deadline) {
				t.Fatalf("tmp.img still has %d bytes allocated, was %d at the peak, after fstrim", tmpImageBytes(t), peak)
			}
			if err := tmpSleep(ctx, 2*time.Second); err != nil {
				t.Fatal(err)
			}
		}
		checkTmpImageSize(t, 2*tmpGiB)
		tmpCheckSentinel(ctx, t, "/tmp/sentinel", sentinel)
	})

	t.Run("restart keeps Docker data and resets /tmp", func(t *testing.T) {
		mustGuest(ctx, t, "sudo -n sh -c 'echo docker-data > /var/lib/docker/.vmtest-tmp-marker && sync'")
		checkTmpReset(ctx, t, "disk", "/dev/vdb")
		checkDisks(t)
		if got := mustGuest(ctx, t, "sudo -n cat /var/lib/docker/.vmtest-tmp-marker"); got != "docker-data" {
			t.Fatalf("Docker data marker = %q after the restart", got)
		}
	})
}

// TestTmpTTLSweepAndDiscard waits for a TTL sweep (5-second interval in
// vmintegration builds) of a 1s TTL and checks what it removed and what it
// must leave alone, then that the freed blocks of tmp.img came back to the
// host without a manual fstrim, and finally that reload with tmp_ttl = "0"
// turns the sweep off.
func TestTmpTTLSweepAndDiscard(t *testing.T) {
	settings := func(ttl string) string {
		return fmt.Sprintf("tmp_storage = \"disk\"\ntmp_size = \"2g\"\ntmp_ttl = %q", ttl)
	}
	ctx := startTmpWorkspace(t, tmpWorkspace{Settings: settings("1s")}, 10*time.Minute)

	// A big file written and deleted before the sweep: its blocks stay
	// allocated on the host until the guest discards them.
	const bigMiB = 1536
	checkTmpImageSize(t, 2*tmpGiB)
	// Dated far into the future, the sentinel outlives the sweep; its
	// checksum must survive the trim of the freed blocks around it.
	sentinel := tmpWriteSentinel(ctx, t, "/tmp/ttl-sentinel", true)
	tmpWriteZeros(ctx, t, "/tmp/ttl-big", bigMiB)
	peak := tmpImageBytes(t)
	if peak < tmpGiB {
		t.Fatalf("tmp.img has %d bytes allocated after writing %d MiB, want at least 1 GiB", peak, bigMiB)
	}
	mustGuest(ctx, t, "rm -f /tmp/ttl-big; sync")

	// Old files and directories the sweep removes, and things it must keep.
	// Times are set last: a directory's mtime changes with its children.
	mustGuest(ctx, t, `set -e
old=946684800
future=4102444800
mkdir -p ~/outside-dir /tmp/ttl-test
echo outside > ~/outside-target
echo outside > ~/outside-dir/old.txt
touch -d @$old ~/outside-target ~/outside-dir/old.txt
cd /tmp/ttl-test
echo x > old.txt
mkdir old-dir empty-old-dir keep-dir bound
echo x > old-dir/child.txt
echo x > future.txt
echo x > fresh-atime.txt
echo x > keep-dir/future-child.txt
echo x > bound/inside-old.txt
ln -s ~/outside-target link-out
ln -s ~/outside-dir link-dir
mkfifo fifo
touch -d @$old old.txt old-dir/child.txt bound/inside-old.txt
touch -d @$future future.txt keep-dir/future-child.txt
touch -m -d @$old fresh-atime.txt
touch -a -d @$future fresh-atime.txt
touch -c -h -d @$old link-out link-dir fifo
touch -d @$old old-dir empty-old-dir keep-dir bound
# A bind mount of the same filesystem: its content is only reachable by
# crossing a mount point, which the sweep must not do.
sudo -n mount --bind /tmp/ttl-test/bound /tmp/ttl-test/bound`)

	// A live unix socket and a flock-held file, both old, owned by a process
	// that keeps running through the sweep.
	mustGuest(ctx, t, `cat > ~/ttl-holder.py <<'PY'
import fcntl, os, socket, time
old = 946684800
base = "/tmp/ttl-test"
lock = open(base + "/locked.dat", "w")
fcntl.flock(lock, fcntl.LOCK_EX)
os.utime(base + "/locked.dat", (old, old))
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(base + "/live.sock")
s.listen(1)
os.utime(base + "/live.sock", (old, old))
with open(os.path.expanduser("~/holder.pid"), "w") as f:
    f.write(str(os.getpid()))
open(os.path.expanduser("~/holder-ready"), "w").close()
time.sleep(7200)
PY
rm -f ~/holder-ready
setsid -f python3 ~/ttl-holder.py </dev/null >~/holder.log 2>&1
for i in $(seq 1 100); do test -e ~/holder-ready && break; sleep 0.1; done
test -e ~/holder-ready || { cat ~/holder.log; exit 1; }`)

	t.Logf("waiting up to %s for the TTL sweep", tmpSweepWait)
	sweepDeadline := time.Now().Add(tmpSweepWait)
	swept := false
	for {
		if !swept {
			_, err := guest(ctx, t, "test ! -e /tmp/ttl-test/old.txt")
			swept = err == nil
		}
		trimmed := tmpImageBytes(t) <= peak-tmpGiB
		if swept && trimmed {
			break
		}
		now := time.Now()
		if !swept && now.After(sweepDeadline) {
			t.Fatalf("old.txt still exists %s after the TTL was set; the sweep did not run", tmpSweepWait)
		}
		if swept && now.After(sweepDeadline.Add(tmpDiscardGrace)) {
			t.Fatalf("tmp.img still has %d bytes allocated, was %d at the peak, after the sweep and %s", tmpImageBytes(t), peak, tmpSweepWait+tmpDiscardGrace)
		}
		if err := tmpSleep(ctx, tmpPollInterval); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("sweep removes old files and directories and keeps the rest", func(t *testing.T) {
		out, err := guest(ctx, t, `cd /tmp/ttl-test || exit 1
fail=0
gone() { for p in "$@"; do if [ -e "$p" ] || [ -L "$p" ]; then echo "not swept: $p"; fail=1; fi; done; }
kept() { for p in "$@"; do if ! { [ -e "$p" ] || [ -L "$p" ]; }; then echo "wrongly swept: $p"; fail=1; fi; done; }
# A file goes when its atime, mtime and ctime are all older than the TTL. The ctime
# of old.txt is that of the setup, which has aged by the wait for the sweep.
gone old.txt old-dir/child.txt empty-old-dir
# A directory goes by its atime and mtime alone, but only once empty. old-dir may be
# removed in this pass or the next; it must not hold its old file any more.
[ ! -d old-dir ] || [ -z "$(ls -A old-dir)" ] || { echo "old-dir not emptied"; fail=1; }
# A newer atime or mtime, or a kept child, keeps a file or a directory.
kept future.txt fresh-atime.txt keep-dir keep-dir/future-child.txt
# Sockets, symlinks and FIFOs are skipped even when old; symlink targets are not followed.
kept link-out link-dir fifo live.sock ~/outside-target ~/outside-dir/old.txt
# Held by a live process: a flock-ed file and a bound socket.
kept locked.dat live.sock
# Another mount point inside /tmp, same filesystem, is not crossed.
kept bound bound/inside-old.txt
kill -0 "$(cat ~/holder.pid)" || { echo "holder process died"; fail=1; }
exit $fail`)
		if err != nil {
			t.Fatalf("sweep result: %v\n%s", err, out)
		}
	})

	t.Run("freed blocks reached the host without fstrim and nothing was corrupted", func(t *testing.T) {
		// The guest has sudo here, but the discard must come from the guest's
		// own periodic trim, which the wait above observed.
		if got := tmpImageBytes(t); got > peak-tmpGiB {
			t.Fatalf("tmp.img has %d bytes allocated, was %d at the peak", got, peak)
		}
		checkTmpImageSize(t, 2*tmpGiB)
		tmpCheckSentinel(ctx, t, "/tmp/ttl-sentinel", sentinel)
	})

	t.Run("status shows /tmp and the TTL", func(t *testing.T) {
		line := checkTmpStatusLine(ctx, t, "disk")
		if !tmpTTLShown("1s").MatchString(line) {
			t.Fatalf("status line %q does not show the 1s TTL", line)
		}
	})

	t.Run("reload with tmp_ttl 0 disables the sweep", func(t *testing.T) {
		writeTmpConfig(t, tmpWorkspace{Settings: settings("0")})
		if out := jailoc(ctx, t, "", "reload", workspaceName); strings.Contains(out, "jailoc restart") {
			t.Fatalf("reload asks for a restart:\n%s", out)
		}
		if line := tmpStatusLine(ctx, t); !tmpTTLShown("0").MatchString(line) {
			t.Fatalf("status line %q does not show the sweep disabled after reload", line)
		}
		// A sweep in flight when the TTL changes is stopped by the guest; the
		// guest unit tests cover that. No second 15 minute wait is made.
		mustGuest(ctx, t, "test -e /tmp/ttl-test/future.txt")
		checkTmpMount(ctx, t, "disk", "/dev/vda")
	})
}
