//go:build linux

package vm

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stacklok/go-microvm/runner"
	"golang.org/x/sys/unix"
)

// TestConfinedRunnerAccess runs shell commands under the rules a VM runner
// gets and checks that only the declared paths are reachable. Paths through
// a share's ".." stand in for the lookups a hostile guest kernel can send to
// libkrun's virtio-fs server.
func TestConfinedRunnerAccess(t *testing.T) {
	t.Parallel()
	fd, handled, err := createRuleset()
	if err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	_ = unix.Close(fd)

	base := t.TempDir()
	dirs := map[string]string{}
	for _, name := range []string{"runtime", "state/microvm/rootfs", "rw", "ro", "home"} {
		dirs[name] = filepath.Join(base, name)
		if err := os.MkdirAll(dirs[name], 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string]string{
		filepath.Join(dirs["ro"], "file"):       "ro",
		filepath.Join(dirs["home"], "key"):      "secret",
		filepath.Join(base, "state/docker.img"): "",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
		LibDir:     dirs["runtime"],
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
	}
	env := append(os.Environ(),
		"RO="+dirs["ro"], "RW="+dirs["rw"], "ROOT="+dirs["state/microvm/rootfs"],
		"DISK="+filepath.Join(base, "state/docker.img"), "LOG="+cfg.ConsoleLog, "BASE="+base,
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
}
