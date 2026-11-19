package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	initFuncsBegin = "# jailoc-init:functions begin"
	initFuncsEnd   = "# jailoc-init:functions end"
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
	bin   string
	state string
	calls string
}

func newInitHarness(t *testing.T) *initHarness {
	t.Helper()
	root := t.TempDir()
	h := &initHarness{t: t, sh: requireSh(t), root: root, bin: filepath.Join(root, "bin"), state: filepath.Join(root, "state"), calls: filepath.Join(root, "calls")}
	for _, d := range []string{h.bin, h.state, filepath.Join(root, "docker"), filepath.Join(root, "run"), filepath.Join(root, "control")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// Tools the init script calls; each records its invocation.
	h.stub("blkid", `echo ext4`)
	h.stub("mount", ``)
	h.stub("mkfs.ext4", ``)
	h.stub("poweroff", ``)
	h.stub("df", `echo "Filesystem 1K-blocks Used Available Use% Mounted on"`)
	return h
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

// apk simulates the package manager. Installing e2fsprogs-extra provides a
// resize2fs; any write to a data disk marked full fails until resize2fs ran.
func (h *initHarness) apk() {
	h.t.Helper()
	h.stub("apk", `
case "$*" in
*e2fsprogs-extra*)
	case "$*" in
	*--no-network*) [ -e "$STATE/cache-warm" ] || exit 1 ;;
	*--no-cache*) [ ! -e "$STATE/no-network" ] || { echo "network unreachable" >&2; exit 1; } ;;
	esac
	printf '#!/bin/sh\necho "resize2fs $*" >> "$CALLS"\n[ ! -e "$STATE/resize-fails" ] || exit 1\ntouch "$STATE/resized"\n' > "$BIN/resize2fs"
	chmod +x "$BIN/resize2fs"
	;;
*nftables*)
	case "$*" in *--no-network*) exit 1 ;; esac
	if [ -e "$STATE/full" ] && [ ! -e "$STATE/resized" ]; then
		echo "apk: No space left on device" >&2
		exit 1
	fi
	;;
esac
`)
}

// run executes script after the init.sh functions with the harness paths.
func (h *initHarness) run(script string, env ...string) (string, int) {
	h.t.Helper()
	preamble := fmt.Sprintf(`set -u
DATA_DEV=/dev/vda
DATA_DIR=%[1]s/docker
APKCACHE=$DATA_DIR/.jailoc-apk-cache
RUN_DIR=%[1]s/run
DAEMON_CONFIG=$RUN_DIR/jailoc-dockerd.json
CONTROL=%[1]s/control
GATEWAY=192.168.127.1
GUEST=192.168.127.2
DOCKER_PORT=2375
AGENT_GID=1000
DISK_SIZE=21474836480
STOP_TICKS=3
export CONTROL
`, h.root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// G204: runs the embedded init.sh functions with a test-controlled script
	// and stub tools on PATH; no external input reaches the shell.
	cmd := exec.CommandContext(ctx, h.sh, "-c", preamble+initFunctions(h.t)+script) //nolint:gosec // G204: trusted embedded init functions, test-controlled script
	cmd.Env = append([]string{"PATH=" + h.bin + ":/usr/bin:/bin", "CALLS=" + h.calls, "STATE=" + h.state, "BIN=" + h.bin}, env...)
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
		mount, tool, resize, nft := index(calls, "mount -t ext4 /dev/vda"), index(calls, "apk", "--no-cache", "e2fsprogs-extra"), index(calls, "resize2fs"), index(calls, "nftables")
		if mount < 0 || tool < mount || resize < tool || nft < resize {
			t.Fatalf("want mount < resize tool install < resize2fs < nftables install, got %d %d %d %d in:\n%s", mount, tool, resize, nft, strings.Join(calls, "\n"))
		}
		for i, c := range calls[:resize] {
			if strings.HasPrefix(c, "apk") && strings.Contains(c, "--cache-dir") && !strings.Contains(c, "--no-network") {
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
		h.mark("cache-warm")
		if err := os.MkdirAll(filepath.Join(h.root, "docker", ".jailoc-apk-cache"), 0o750); err != nil {
			t.Fatal(err)
		}
		out, code := h.run(`prepare_data_disk`)
		if code != 0 {
			t.Fatalf("exit %d, output:\n%s", code, out)
		}
		calls := h.callLog()
		if index(calls, "--no-cache") >= 0 {
			t.Errorf("warm cache still used the network fallback:\n%s", strings.Join(calls, "\n"))
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
		if index(calls, "resize2fs") >= 0 || index(calls, "e2fsprogs-extra") >= 0 {
			t.Errorf("a freshly formatted disk needs no resize:\n%s", strings.Join(calls, "\n"))
		}
	})
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
