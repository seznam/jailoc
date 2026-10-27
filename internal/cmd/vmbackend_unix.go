//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guestbin"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	vmStartTimeout   = 5 * time.Minute
	vmStopTimeout    = 45 * time.Second
	vmMemoryOverhead = 1024 // MiB added to the workspace memory limit
)

// vmSuperviseCmd is the detached process that owns a workspace VM and its
// network broker. `jailoc up` starts it and `jailoc down` stops it.
var vmSuperviseCmd = &cobra.Command{
	Use:          "vm-supervise <state-dir>",
	Hidden:       true,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, err := vm.ReadSpec(args[0])
		if err != nil {
			return fmt.Errorf("read vm spec: %w", err)
		}
		spec.Log = os.Stderr
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		return vm.Run(ctx, *spec)
	},
}

func init() {
	rootCmd.AddCommand(vmSuperviseCmd)
}

func vmRootDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc-vm")
	}
	return filepath.Join(home, ".cache", "jailoc-vm")
}

func vmStateDir(name string) string { return filepath.Join(vmRootDir(), "workspaces", name) }

func setDockerHost(socket string) error {
	if err := os.Setenv("DOCKER_HOST", "unix://"+socket); err != nil {
		return fmt.Errorf("set DOCKER_HOST: %w", err)
	}
	return nil
}

// runningVM returns the state of a ready VM whose supervisor is alive.
func runningVM(stateDir string) (*vm.State, error) {
	st, err := vm.ReadState(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Ready || !processAlive(st.PID) {
		return nil, nil
	}
	return st, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// startSupervisor makes sure a VM runs with spec: an unchanged running VM is
// reused, a changed one is restarted. It returns once the VM is ready.
func startSupervisor(ctx context.Context, spec vm.Spec, label string) (*vm.State, error) {
	st, err := runningVM(spec.StateDir)
	if err != nil {
		return nil, err
	}
	if st != nil {
		if old, err := vm.ReadSpec(spec.StateDir); err == nil && sameVMSpec(*old, spec) {
			return st, nil
		}
		_, _ = color.New(color.FgYellow).Printf("%s configuration changed, restarting it...\n", label)
		if err := stopSupervisor(spec.StateDir); err != nil {
			return nil, err
		}
	}
	if err := vm.WriteSpec(spec); err != nil {
		return nil, err
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate jailoc executable: %w", err)
	}
	logPath := filepath.Join(spec.StateDir, "supervisor.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("open vm log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	_ = os.Remove(vm.StateFile(spec.StateDir))

	_, _ = color.New(color.FgCyan).Printf("Starting %s...\n", label)
	sup := exec.Command(exe, "vm-supervise", spec.StateDir) //nolint:gosec // G204: re-executes jailoc itself
	sup.Stdout, sup.Stderr = logFile, logFile
	sup.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := sup.Start(); err != nil {
		return nil, fmt.Errorf("start vm supervisor: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- sup.Wait() }()

	deadline := time.NewTimer(vmStartTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = sup.Process.Signal(syscall.SIGTERM)
			return nil, fmt.Errorf("start vm: %w", ctx.Err())
		case err := <-exited:
			return nil, fmt.Errorf("vm supervisor exited (%v): %s", err, vmFailure(spec.StateDir, logPath))
		case <-deadline.C:
			_ = sup.Process.Signal(syscall.SIGTERM)
			return nil, fmt.Errorf("vm not ready after %s: %s", vmStartTimeout, vmFailure(spec.StateDir, logPath))
		case <-tick.C:
			st, err := vm.ReadState(spec.StateDir)
			if err != nil || st.PID != sup.Process.Pid {
				continue
			}
			if st.Ready {
				return st, nil
			}
			if st.Error != "" {
				return nil, fmt.Errorf("start vm: %s", st.Error)
			}
		}
	}
}

// vmFailure explains why a VM did not come up, from its state or log.
func vmFailure(stateDir, logPath string) string {
	if st, err := vm.ReadState(stateDir); err == nil && st.Error != "" {
		return st.Error
	}
	data, err := os.ReadFile(logPath) //nolint:gosec // G304: jailoc-owned state path
	if err != nil || len(data) == 0 {
		return "see " + logPath
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return strings.Join(lines, "; ")
}

// stopSupervisor stops the VM supervisor of stateDir and waits for it.
func stopSupervisor(stateDir string) error {
	st, err := vm.ReadState(stateDir)
	if err != nil || !processAlive(st.PID) {
		return nil
	}
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop vm supervisor: %w", err)
	}
	deadline := time.Now().Add(vmStopTimeout)
	for processAlive(st.PID) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if processAlive(st.PID) {
		_ = syscall.Kill(st.PID, syscall.SIGKILL)
	}
	return nil
}

func sameVMSpec(a, b vm.Spec) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// vmRuntime is the runner, its library directory and the jailoc-guest
// binary: the configured runner or the pinned runtime jailoc downloads on
// first use, and the guest binary embedded in jailoc (release builds) or
// next to the runner.
type vmRuntimeFiles struct {
	runner, libDir, guest string
}

func vmRuntime(ctx context.Context, cfg *config.Config) (vmRuntimeFiles, error) {
	var rt vmRuntimeFiles
	if cfg.VM.Runner != "" {
		rt = vmRuntimeFiles{runner: cfg.VM.Runner, libDir: vmLibDir(cfg.VM.LibDir)}
	} else {
		runner, libDir, err := vm.EnsureRuntime(ctx, filepath.Join(vmRootDir(), "runtime"), os.Stdout)
		if err != nil {
			return rt, fmt.Errorf("prepare VM runtime: %w", err)
		}
		rt = vmRuntimeFiles{runner: runner, libDir: libDir}
	}
	guest, err := guestbin.Path(filepath.Join(vmRootDir(), "guest"))
	switch {
	case err == nil:
		rt.guest = guest
	case errors.Is(err, guestbin.ErrNotEmbedded):
		rt.guest = filepath.Join(filepath.Dir(rt.runner), guestbin.FileName)
		if _, statErr := os.Stat(rt.guest); statErr != nil {
			return rt, fmt.Errorf("%w and %s is not next to %s; run `go generate ./internal/guestbin` before building jailoc", err, guestbin.FileName, rt.runner)
		}
	default:
		return rt, fmt.Errorf("prepare guest binary: %w", err)
	}
	return rt, nil
}

func vmLibDir(configured string) string {
	if configured != "" {
		return configured
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/opt/homebrew/lib"); err == nil {
			return "/opt/homebrew/lib"
		}
	}
	return ""
}

// Guest paths of the jailoc directories shared with a workspace VM.
const (
	guestConfigDir   = "/etc/jailoc"
	guestCacheDir    = "/etc/jailoc-cache"
	guestOpenCodeDir = "/home/agent/.local/share/opencode"
	guestUserCache   = "/home/agent/.cache"
)

// vmShares lists the host directories the workspace VM mounts: workspace
// paths read-write at their host paths, mount sources at their container
// paths, the workspace's jailoc config and cache directories read-only,
// and persistent OpenCode data and cache directories unless a mount covers
// them.
func vmShares(ws *workspace.Resolved, cacheDir string) ([]vm.Share, error) {
	byGuest := map[string]vm.Share{}
	add := func(host, guestPath string, ro bool) {
		guestPath = filepath.Clean(guestPath)
		if prev, ok := byGuest[guestPath]; ok {
			ro = prev.ReadOnly && ro
		}
		byGuest[guestPath] = vm.Share{HostPath: filepath.Clean(host), GuestPath: guestPath, ReadOnly: ro}
	}
	for _, p := range ws.Paths {
		add(p, p, false)
	}
	for _, spec := range ws.Mounts {
		m, err := config.ParseMount(spec)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
		if m.Host == "" {
			continue
		}
		host, err := config.ExpandPath(m.Host)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: expand mount %q: %w", ws.Name, spec, err)
		}
		info, err := os.Stat(host)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.MkdirAll(host, 0o750); err != nil {
				return nil, fmt.Errorf("workspace %q: create mount source %q: %w", ws.Name, host, err)
			}
		case err != nil:
			return nil, fmt.Errorf("workspace %q: mount source %q: %w", ws.Name, host, err)
		case !info.IsDir():
			return nil, fmt.Errorf("workspace %q: mount %q: backend \"vm\" shares directories only; use copies for a read-only snapshot of a file", ws.Name, spec)
		}
		add(host, m.Container, m.Mode == "ro")
	}
	add(filepath.Join(config.ConfigDir(), "workspaces", ws.Name), guestConfigDir, true)
	add(cacheDir, guestCacheDir, true)
	add(filepath.Join(cacheDir, "tui-plugin"), tuiPluginContainerDir, true)
	for _, dir := range []struct{ name, guestPath string }{{"opencode-data", guestOpenCodeDir}, {"cache", guestUserCache}} {
		if _, covered := byGuest[dir.guestPath]; covered {
			continue
		}
		host := filepath.Join(vmStateDir(ws.Name), "home", dir.name)
		if err := os.MkdirAll(host, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", host, err)
		}
		add(host, dir.guestPath, false)
	}

	guestPaths := make([]string, 0, len(byGuest))
	for p := range byGuest {
		guestPaths = append(guestPaths, p)
	}
	sort.Strings(guestPaths)
	shares := make([]vm.Share, 0, len(guestPaths))
	for _, p := range guestPaths {
		shares = append(shares, byGuest[p])
	}
	return shares, nil
}

// vmMemoryMiB sizes the workspace VM: the workspace memory limit plus room
// for the guest kernel.
func vmMemoryMiB(limit string) (uint32, error) {
	if limit == "" {
		return 0, errors.New("empty memory limit")
	}
	mult := int64(1)
	switch unit := strings.ToLower(limit[len(limit)-1:]); unit {
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	}
	digits := strings.TrimRight(limit, "kmgKMG")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory limit %q", limit)
	}
	if n > (int64(math.MaxUint32)<<20)/mult {
		return 0, fmt.Errorf("memory limit %q is too large", limit)
	}
	mib := (n*mult + (1<<20 - 1)) >> 20
	total := mib + vmMemoryOverhead
	if total > math.MaxUint32 {
		return 0, fmt.Errorf("memory limit %q is too large", limit)
	}
	return uint32(total), nil //nolint:gosec // G115: bounded by the check above
}

type vmException struct {
	CIDR  string `json:"cidr"`
	Proto string `json:"proto"`
}

// vmPolicy turns allowed_networks and allowed_hosts into broker exceptions.
// Host names are resolved on the host once, when the VM starts.
func vmPolicy(ctx context.Context, ws *workspace.Resolved) ([]byte, error) {
	exceptions := []vmException{}
	seen := map[string]bool{}
	add := func(cidr string) {
		if !seen[cidr] {
			seen[cidr] = true
			exceptions = append(exceptions, vmException{CIDR: cidr, Proto: "any"})
		}
	}
	for _, n := range ws.AllowedNetworks {
		_, ipnet, err := net.ParseCIDR(n)
		if err != nil {
			return nil, fmt.Errorf("allowed network %q: %w", n, err)
		}
		if ipnet.IP.To4() != nil {
			add(ipnet.String())
		}
	}
	for _, h := range ws.AllowedHosts {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", h)
		if err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: cannot resolve allowed host %q: %v\n", h, err)
			continue
		}
		for _, ip := range ips {
			add(ip.String() + "/32")
		}
	}
	data, err := json.Marshal(struct {
		Exceptions []vmException `json:"exceptions"`
	}{exceptions})
	if err != nil {
		return nil, fmt.Errorf("encode broker policy: %w", err)
	}
	return data, nil
}
