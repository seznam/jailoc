package guest

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	dockerDataDir  = "/var/lib/docker"
	dockerLog      = "/var/log/jailoc-dockerd.log"
	dockerStopWait = 15 * time.Second
)

// dockerDaemon is a running dockerd. Its exit status is collected by the
// reaper, so it is tracked by PID only.
type dockerDaemon struct {
	pid int
}

// startDocker mounts the Docker data disk, formatting it on first use, and
// starts dockerd with the nftables firewall backend; the guest kernel has
// no iptables NAT. The socket is group-owned by the agent's group, so the
// agent uses Docker without sudo.
func startDocker(logger *slog.Logger, d *Docker, acct account) (*dockerDaemon, error) {
	dockerd, err := exec.LookPath("dockerd")
	if err != nil {
		return nil, fmt.Errorf("docker: dockerd not found in the image: %w", err)
	}
	formatted, err := hasExtFS(d.Disk)
	if err != nil {
		return nil, fmt.Errorf("docker: inspect %s: %w", d.Disk, err)
	}
	if !formatted {
		logger.Info("formatting docker data disk", "device", d.Disk)
		out, err := exec.Command("mkfs.ext4", "-q", "-F", "-E", "nodiscard", d.Disk).CombinedOutput() //nolint:gosec // G204: fixed command, device from the guest config
		if err != nil {
			return nil, fmt.Errorf("docker: format %s: %w: %s", d.Disk, err, out)
		}
	}
	if err := os.MkdirAll(dockerDataDir, 0o711); err != nil { //nolint:gosec // G301: dockerd's default data-root mode
		return nil, fmt.Errorf("docker: create %s: %w", dockerDataDir, err)
	}
	if err := syscall.Mount(d.Disk, dockerDataDir, "ext4", 0, ""); err != nil {
		return nil, fmt.Errorf("docker: mount %s: %w", d.Disk, err)
	}
	if err := os.MkdirAll(filepath.Dir(dockerLog), 0o755); err != nil { //nolint:gosec // G301: standard log directory
		return nil, fmt.Errorf("docker: create log dir: %w", err)
	}
	logFile, err := os.OpenFile(dockerLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640) //nolint:gosec // G304: fixed path
	if err != nil {
		return nil, fmt.Errorf("docker: open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command(dockerd, //nolint:gosec // G204: dockerd from the image PATH, fixed arguments
		"--data-root", dockerDataDir,
		"--firewall-backend=nftables",
		"-H", "unix:///var/run/docker.sock",
		"-G", strconv.FormatUint(uint64(acct.gid), 10),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("docker: start dockerd: %w", err)
	}
	logger.Info("dockerd started", "pid", cmd.Process.Pid, "log", dockerLog)
	return &dockerDaemon{pid: cmd.Process.Pid}, nil
}

// stop asks dockerd to stop its containers and exit, then unmounts the
// data disk so it is consistent before the VM powers off.
func (d *dockerDaemon) stop(logger *slog.Logger) {
	if d == nil {
		return
	}
	_ = syscall.Kill(d.pid, syscall.SIGTERM)
	deadline := time.Now().Add(dockerStopWait)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(d.pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	syscall.Sync()
	if err := syscall.Unmount(dockerDataDir, 0); err != nil {
		logger.Warn("unmount docker data disk", "error", err)
	}
}

// hasExtFS reports whether dev carries an ext2/3/4 superblock.
func hasExtFS(dev string) (bool, error) {
	f, err := os.Open(dev) //nolint:gosec // G304: device from the guest config
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	return extMagicAt(f)
}
