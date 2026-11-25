//go:build darwin

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/go-microvm/runner"
)

// libkrun's virtio-fs server can be made to open files outside the exported
// directory by a guest that controls its kernel, so the runner starts under a
// Seatbelt profile that only grants the runtime, the VM's root file system,
// the declared shares and disks, the logs and the broker socket
// (seatbelt_profile.go). The profile is applied by a short-lived shim, the
// jailoc binary running its hidden "vm-seatbelt" command, which confines
// itself and execs the runner; the runner inherits the profile.

const (
	runnerBinaryName    = "go-microvm-runner"
	seatbeltStopTimeout = 30 * time.Second
	seatbeltKillTimeout = 5 * time.Second
)

// seatbeltShimCommand returns the executable and leading arguments of the
// shim; the spawner appends <runner-path> <process-name> <config-json>.
// Tests replace it.
var seatbeltShimCommand = func() (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("locate jailoc executable: %w", err)
	}
	return exe, []string{"vm-seatbelt"}, nil
}

// vmmSpawner starts the VM runner confined by Seatbelt.
func vmmSpawner(redactor *consoleRedactor) runner.Spawner {
	return confinedSpawner{redactor: redactor}
}

type confinedSpawner struct {
	redactor *consoleRedactor
}

func (s confinedSpawner) Spawn(_ context.Context, cfg runner.Config) (runner.ProcessHandle, error) {
	// The runner is long-lived and outlives the calling context; its
	// lifecycle is managed through Stop, so the context is not used.
	runnerPath, err := resolveSeatbeltRunner(cfg.RunnerPath)
	if err != nil {
		return nil, err
	}
	pipePath, waitConsole, err := startConsolePipe(cfg.ConsoleLog, s.redactor)
	if err != nil {
		return nil, fmt.Errorf("start confined VM runner: %w", err)
	}
	if pipePath != "" {
		cfg.ConsoleLog = pipePath
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		waitConsole()
		return nil, fmt.Errorf("marshal runner config: %w", err)
	}
	profile, err := seatbeltProfile(cfg, runnerPath)
	if err != nil {
		waitConsole()
		return nil, fmt.Errorf("build VM runner sandbox profile: %w", err)
	}
	cmd, err := seatbeltShim(runnerPath, cfg.ProcessName, configJSON)
	if err != nil {
		waitConsole()
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cfg.LibDir != "" {
		cmd.Env = append(os.Environ(), "DYLD_LIBRARY_PATH="+cfg.LibDir)
	}
	if cfg.VMLogPath != "" {
		logFile, err := os.OpenFile(cfg.VMLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: jailoc-owned state path
		if err != nil {
			waitConsole()
			return nil, fmt.Errorf("open vm log file: %w", err)
		}
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		// The child inherited the descriptor by the time launchSeatbelt returns.
		defer func() { _ = logFile.Close() }()
	}

	if err := launchSeatbelt(cmd, profile); err != nil {
		waitConsole()
		return nil, fmt.Errorf("start confined VM runner: %w", err)
	}
	proc := &seatbeltProcess{pid: cmd.Process.Pid, done: make(chan struct{})}
	// Reap the runner when it exits so it does not stay a zombie. The pipe
	// writer stays open until then, so the reader cannot see EOF before
	// libkrun opens the pipe. The exit is reported first: consoleProcessHandle
	// waits for the drain with a bound, so a stuck drain cannot block Stop.
	go func() {
		_ = cmd.Wait() // the exit status is not needed: IsAlive reports the exit
		close(proc.done)
		waitConsole()
	}()
	return consoleProcessHandle{ProcessHandle: proc, wait: waitConsole}, nil
}

// resolveSeatbeltRunner returns the absolute path of the runner binary.
func resolveSeatbeltRunner(configured string) (string, error) {
	path := configured
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("runner binary not found: %w", err)
		}
	} else {
		found, err := exec.LookPath(runnerBinaryName)
		if err != nil {
			return "", fmt.Errorf("runner binary not found: %w", err)
		}
		path = found
	}
	canon, err := canonicalPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve runner binary: %w", err)
	}
	return canon, nil
}

// seatbeltShim builds the shim command line.
func seatbeltShim(runnerPath, processName string, configJSON []byte) (*exec.Cmd, error) {
	exe, lead, err := seatbeltShimCommand()
	if err != nil {
		return nil, err
	}
	args := append(append([]string{}, lead...), runnerPath, processName, string(configJSON))
	return exec.Command(exe, args...), nil //nolint:gosec // G204: re-executes jailoc with arguments built by the spawner
}

// launchSeatbelt starts the shim cmd with profile on fd 3 and an error pipe
// on fd 4, and returns once the shim has execed the runner. When the shim
// reports an error (the profile did not compile or apply, or the exec
// failed) it waits for the shim and fails: the runner never runs unconfined.
// On success the caller owns cmd and must wait for it.
func launchSeatbelt(cmd *exec.Cmd, profile string) error {
	profR, profW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create profile pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = profR.Close()
		_ = profW.Close()
		return fmt.Errorf("create error pipe: %w", err)
	}
	cmd.ExtraFiles = []*os.File{profR, errW} // fd 3 and fd 4 in the shim
	startErr := cmd.Start()
	// The shim has its own copies; closing ours lets errR see EOF once the
	// shim execs (fd 4 is close-on-exec there).
	_ = profR.Close()
	_ = errW.Close()
	if startErr != nil {
		_ = profW.Close()
		_ = errR.Close()
		return fmt.Errorf("start seatbelt shim: %w", startErr)
	}
	// Write from a goroutine so a profile larger than the pipe buffer cannot
	// block while the shim is not reading yet.
	go func() {
		_, _ = io.WriteString(profW, profile) // a failure shows up as a shim error
		_ = profW.Close()
	}()

	msg, readErr := io.ReadAll(errR)
	_ = errR.Close()
	if readErr != nil || len(msg) > 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // reap the shim; its exit status adds nothing to the message
		if readErr != nil {
			return fmt.Errorf("read seatbelt shim status: %w", readErr)
		}
		return fmt.Errorf("apply Seatbelt profile: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}

// seatbeltProcess is the confined runner process.
type seatbeltProcess struct {
	pid  int
	done chan struct{} // closed when the process has exited and been reaped
}

var _ runner.ProcessHandle = (*seatbeltProcess)(nil)

func (p *seatbeltProcess) PID() int { return p.pid }

func (p *seatbeltProcess) IsAlive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Stop sends SIGTERM to the runner's process group and waits for it to exit;
// after 30 seconds or when ctx ends it sends SIGKILL. The runner started with
// Setsid, so its process group ID is its PID.
func (p *seatbeltProcess) Stop(ctx context.Context) error {
	if !p.IsAlive() {
		return nil
	}
	if p.pid <= 1 {
		return fmt.Errorf("refusing to signal unsafe pid %d", p.pid)
	}
	if err := syscall.Kill(-p.pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("send SIGTERM to pid %d: %w", p.pid, err)
	}
	timer := time.NewTimer(seatbeltStopTimeout)
	defer timer.Stop()
	var stopErr error
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
		stopErr = ctx.Err()
	}

	if err := syscall.Kill(-p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("send SIGKILL to pid %d: %w", p.pid, err)
	}
	killTimer := time.NewTimer(seatbeltKillTimeout)
	defer killTimer.Stop()
	select {
	case <-p.done:
		return stopErr
	case <-killTimer.C:
		return fmt.Errorf("pid %d did not exit after SIGKILL", p.pid)
	}
}
