//go:build linux

package vm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"unsafe"

	"github.com/stacklok/go-microvm/runner"
	"golang.org/x/sys/unix"
)

// libkrun's Linux virtio-fs server resolves names from the guest relative to
// the exported directory without confining them to it (a lookup of ".." or
// of a name with "/" reaches the rest of the host), so a guest that controls
// its kernel could make the VM process open any file the user can. The
// runner therefore starts under a Landlock domain that only grants the
// runtime, the VM's root file system, data directory and disks, and the
// declared shares (read-only shares read-only).

const (
	llRead     uint64 = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
	llReadExec        = llRead | unix.LANDLOCK_ACCESS_FS_EXECUTE
	llWrite           = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE
	llAll             = ^uint64(0)
	llFile            = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE |
		unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	// llBase are the rights of Landlock ABI 1 (EXECUTE through MAKE_SYM).
	llBase = 1<<13 - 1
)

// landlockRule grants access beneath path. Optional rules are skipped when
// the path does not exist.
type landlockRule struct {
	path     string
	access   uint64
	optional bool
}

// vmmSpawner starts the VM runner confined by Landlock.
func vmmSpawner() runner.Spawner { return confinedSpawner{} }

type confinedSpawner struct{}

func (confinedSpawner) Spawn(ctx context.Context, cfg runner.Config) (runner.ProcessHandle, error) {
	var proc *runner.Process
	err := startConfined(vmmRules(cfg), func() error {
		var err error
		proc, err = runner.SpawnProcess(ctx, cfg)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("start confined VM runner: %w", err)
	}
	return proc, nil
}

// vmmRules lists what the runner of cfg may access.
func vmmRules(cfg runner.Config) []landlockRule {
	var rules []landlockRule
	// Dynamic loader, system libraries, CPU and memory information.
	for _, p := range []string{"/usr", "/lib", "/lib64", "/lib32", "/libx32", "/bin", "/sbin", "/etc", "/nix/store"} {
		rules = append(rules, landlockRule{path: p, access: llReadExec, optional: true})
	}
	for _, p := range []string{"/proc", "/sys"} {
		rules = append(rules, landlockRule{path: p, access: llRead, optional: true})
	}
	// /dev/kvm, /dev/null and the random devices.
	rules = append(rules, landlockRule{path: "/dev", access: llRead | llWrite | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV, optional: true})

	if cfg.RunnerPath != "" {
		rules = append(rules, landlockRule{path: filepath.Dir(cfg.RunnerPath), access: llReadExec})
	}
	for _, d := range filepath.SplitList(cfg.LibDir) {
		if d != "" {
			rules = append(rules, landlockRule{path: d, access: llReadExec})
		}
	}
	if cfg.RootPath != "" {
		rules = append(rules, landlockRule{path: cfg.RootPath, access: llAll})
	}
	for _, m := range cfg.VirtioFS {
		access := llAll
		if m.ReadOnly {
			access = llRead
		}
		rules = append(rules, landlockRule{path: m.HostPath, access: access})
	}
	for _, d := range cfg.Disks {
		access := llRead | llWrite
		if d.ReadOnly {
			access = llRead
		}
		rules = append(rules, landlockRule{path: d.HostPath, access: access})
	}
	// Runner and console logs.
	for _, p := range []string{cfg.VMLogPath, cfg.ConsoleLog} {
		if p != "" {
			rules = append(rules, landlockRule{path: filepath.Dir(p), access: llRead | llWrite | unix.LANDLOCK_ACCESS_FS_MAKE_REG})
		}
	}
	// The broker's NIC socket; libkrun binds vsock sockets itself.
	if cfg.NetSocket != "" {
		rules = append(rules, landlockRule{path: filepath.Dir(cfg.NetSocket), access: unix.LANDLOCK_ACCESS_FS_RESOLVE_UNIX})
	}
	for _, v := range cfg.VsockPorts {
		rules = append(rules, landlockRule{path: filepath.Dir(v.SocketPath), access: unix.LANDLOCK_ACCESS_FS_RESOLVE_UNIX |
			unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE})
	}
	return rules
}

// startConfined runs start on a fresh OS thread restricted by rules, so the
// processes start creates inherit the restriction. The thread never returns
// to the Go scheduler: it stays locked and exits with its goroutine.
func startConfined(rules []landlockRule, start func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := restrictThread(rules); err != nil {
			done <- err
			return
		}
		done <- start()
	}()
	return <-done
}

// restrictThread applies a Landlock domain allowing only rules to the
// calling thread.
func restrictThread(rules []landlockRule) error {
	ruleset, handled, err := createRuleset()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(ruleset) }()
	for _, r := range rules {
		if err := addRule(ruleset, handled, r); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("apply Landlock ruleset: %w", errno)
	}
	return nil
}

// createRuleset creates a ruleset handling every file system right the
// kernel supports.
func createRuleset() (fd int, handled uint64, err error) {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return -1, 0, fmt.Errorf("confining the VM process needs Landlock (Linux 5.19 or later with Landlock enabled): %w", errno)
	}
	// ABI 1 refuses every rename or link across directories.
	if abi < 2 {
		return -1, 0, fmt.Errorf("confining the VM process needs Landlock ABI 2 (Linux 5.19 or later), the kernel has ABI %d", abi)
	}
	handled = llBase | unix.LANDLOCK_ACCESS_FS_REFER
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		handled |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	// Restricting Unix socket connections is newer; use it when available.
	for _, h := range []uint64{handled | unix.LANDLOCK_ACCESS_FS_RESOLVE_UNIX, handled} {
		attr := unix.LandlockRulesetAttr{Handled_access_fs: h}
		r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0) //nolint:gosec // G103: Landlock has no x/sys wrapper
		if errno == 0 {
			return int(r), h, nil
		}
		if !errors.Is(errno, unix.EINVAL) {
			return -1, 0, fmt.Errorf("create Landlock ruleset: %w", errno)
		}
	}
	return -1, 0, errors.New("create Landlock ruleset: kernel rejected the handled rights")
}

func addRule(ruleset int, handled uint64, r landlockRule) error {
	fd, err := unix.Open(r.path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if r.optional && errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("open %s for the Landlock ruleset: %w", r.path, err)
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("stat %s for the Landlock ruleset: %w", r.path, err)
	}
	access := r.access & handled
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= llFile
	}
	if access == 0 {
		return nil
	}
	attr := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)} //nolint:gosec // G115: file descriptors fit in int32
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0); errno != 0 { //nolint:gosec // G103: Landlock has no x/sys wrapper
		return fmt.Errorf("add Landlock rule for %s: %w", r.path, errno)
	}
	return nil
}
