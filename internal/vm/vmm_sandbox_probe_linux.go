//go:build linux && vmintegration

package vm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stacklok/go-microvm/runner"
	"golang.org/x/sys/unix"
)

// runnerProbeEnv holds a shell script that VM integration tests run inside
// the runner's Landlock domain to prove what the real runner can reach.
const runnerProbeEnv = "JAILOC_VMINTEGRATION_RUNNER_PROBE"

func init() { runnerProbe = probeRunner }

// probeRunner runs the script from runnerProbeEnv on the confined thread, so
// the forked shell inherits the runner's Landlock domain. Its output goes to
// runner-probe.log next to the VM log.
func probeRunner(cfg runner.Config, runnerPID int) {
	script := os.Getenv(runnerProbeEnv)
	if script == "" || cfg.VMLogPath == "" {
		return
	}
	logPath := filepath.Join(filepath.Dir(cfg.VMLogPath), "runner-probe.log")
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // G304: path derived from the runner's own log dir
	if err != nil {
		fmt.Fprintf(os.Stderr, "runner probe: open log: %v\n", err)
		return
	}
	defer func() { _ = out.Close() }()

	resolveUnix := "0"
	if fd, handled, err := createRuleset(); err == nil {
		_ = unix.Close(fd) // only the handled rights were needed
		if handled&unix.LANDLOCK_ACCESS_FS_RESOLVE_UNIX != 0 {
			resolveUnix = "1"
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script) //nolint:gosec // G204: test-only probe script, vmintegration builds only
	cmd.Env = append(os.Environ(),
		"RUNNER_PID="+strconv.Itoa(runnerPID),
		"SUPERVISOR_PID="+strconv.Itoa(os.Getpid()),
		"RESOLVE_UNIX="+resolveUnix,
	)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		_, _ = fmt.Fprintf(out, "probe failed: %v\n", err)
	}
}
