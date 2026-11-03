//go:build linux

package vm

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/stacklok/go-microvm/runner"
	"golang.org/x/sys/unix"
)

// TestConfinedRunnerAccess runs shell commands under the rules a VM runner
// gets and checks that only the declared paths are reachable. Paths through
// a share's ".." stand in for guest-originated lookups handled by libkrun's
// virtio-fs server.
func TestConfinedRunnerAccess(t *testing.T) {
	t.Parallel()
	fd, handled, err := createRuleset()
	if err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	_ = unix.Close(fd)

	base := t.TempDir()
	dirs := map[string]string{}
	for _, name := range []string{"runtime", "lib", "state/microvm/rootfs", "rw", "ro", "home"} {
		dirs[name] = filepath.Join(base, name)
		if err := os.MkdirAll(dirs[name], 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string]string{
		filepath.Join(dirs["ro"], "file"):                   "ro",
		filepath.Join(dirs["home"], "key"):                  "secret",
		filepath.Join(dirs["runtime"], "go-microvm-runner"): "runner",
		filepath.Join(dirs["runtime"], "other-file"):        "other",
		filepath.Join(base, "state/docker.img"):             "",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	devFile, err := os.CreateTemp("/dev/shm", "jailoc-landlock-")
	if err != nil {
		t.Skipf("create non-listed device file: %v", err)
	}
	devFilePath := devFile.Name()
	if err := devFile.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(devFilePath) })
	if err := os.MkdirAll(filepath.Join(dirs["rw"], "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	outsideSock := filepath.Join(dirs["home"], "agent.sock")
	l, err := net.Listen("unix", outsideSock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	cfg := runner.Config{
		RootPath:   dirs["state/microvm/rootfs"],
		RunnerPath: filepath.Join(dirs["runtime"], "go-microvm-runner"),
		LibDir:     dirs["lib"],
		NetSocket:  filepath.Join(base, "state", "nic.sock"),
		VirtioFS: []runner.VirtioFSMount{
			{Tag: "rw", HostPath: dirs["rw"]},
			{Tag: "ro", HostPath: dirs["ro"], ReadOnly: true},
		},
		Disks:      []runner.Disk{{ID: "docker", HostPath: filepath.Join(base, "state/docker.img")}},
		ConsoleLog: filepath.Join(base, "state/microvm/console.log"),
		VMLogPath:  filepath.Join(base, "state/microvm/vm.log"),
	}
	rules := vmmRules(cfg)

	tests := []struct {
		name   string
		script string
		allow  bool
	}{
		{"read ro share", `cat "$RO/file"`, true},
		{"write ro share", `echo x > "$RO/file"`, false},
		{"create in ro share", `touch "$RO/new"`, false},
		{"write rw share", `echo x > "$RW/a/file"`, true},
		{"rename across rw share dirs", `mkdir -p "$RW/b" && echo y > "$RW/a/mv" && mv "$RW/a/mv" "$RW/b/mv"`, true},
		{"write root fs", `mkdir -p "$ROOT/etc" && echo x > "$ROOT/etc/x"`, true},
		{"write disk", `echo x >> "$DISK"`, true},
		{"write console log", `echo x > "$LOG"`, true},
		{"read outside through share", `cat "$RW/../home/key"`, false},
		{"list outside through share", `ls "$RO/../home"`, false},
		{"create outside through share", `touch "$RW/../home/planted"`, false},
		{"delete outside through root fs", `rm "$ROOT/../../../home/key"`, false},
		{"write state dir", `echo x > "$BASE/state/spec.json"`, false},
		{"read runner", `cat "$RUNNER"`, true},
		{"read other runner directory file", `cat "$RUNNER_OTHER"`, false},
		{"read dynamic loader cache", `cat /etc/ld.so.cache >/dev/null`, true},
		{"read other etc file", `cat /etc/passwd`, false},
		{"write null device", `echo x > /dev/null`, true},
		{"read non-listed device file", `cat "$DEV_FILE"`, false},
	}
	env := append(os.Environ(),
		"RO="+dirs["ro"], "RW="+dirs["rw"], "ROOT="+dirs["state/microvm/rootfs"],
		"DISK="+filepath.Join(base, "state/docker.img"), "LOG="+cfg.ConsoleLog, "BASE="+base,
		"RUNNER="+cfg.RunnerPath, "RUNNER_OTHER="+filepath.Join(dirs["runtime"], "other-file"),
		"DEV_FILE="+devFilePath,
	)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out []byte
			err := startConfined(rules, func() error {
				cmd := exec.Command("/bin/sh", "-c", tt.script) //nolint:gosec // G204: fixed test scripts
				cmd.Env = env
				var err error
				out, err = cmd.CombinedOutput()
				return err
			})
			if tt.allow && err != nil {
				t.Fatalf("denied: %v\n%s", err, out)
			}
			if !tt.allow && err == nil {
				t.Fatalf("allowed:\n%s", out)
			}
		})
	}

	t.Run("connect to socket outside state dir", func(t *testing.T) {
		if handled&unix.LANDLOCK_ACCESS_FS_RESOLVE_UNIX == 0 {
			t.Skip("kernel cannot restrict Unix socket connections")
		}
		err := startConfined(rules, func() error {
			c, err := net.Dial("unix", outsideSock)
			if err == nil {
				_ = c.Close()
			}
			return err
		})
		if err == nil {
			t.Fatal("connected to a socket outside the state directory")
		}
	})

	t.Run("proc access follows process domain", func(t *testing.T) {
		outside := exec.Command("/bin/sh", "-c", "sleep 30")
		outside.Env = append(os.Environ(), "JAILOC_PROC_SCOPE=outside")
		if err := outside.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = outside.Process.Kill()
			_ = outside.Wait()
		})

		err := startConfined(rules, func() error {
			cmd := exec.Command("/bin/sh", "-c", `cat /proc/self/status >/dev/null && ! cat "$OUTSIDE_ENV" >/dev/null 2>&1`)
			cmd.Env = append(slices.Clone(env), "OUTSIDE_ENV=/proc/"+strconv.Itoa(outside.Process.Pid)+"/environ")
			return cmd.Run()
		})
		if err != nil {
			t.Fatalf("proc access did not match process domain: %v", err)
		}
	})
}
