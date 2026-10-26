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
	"github.com/seznam/jailoc/internal/compose"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	vmRunnerBinary   = "go-microvm-runner"
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

// connectBackend points the Docker clients of this process at the workspace's
// VM when the workspace uses the vm backend. It reports whether the backend is
// reachable: always for docker, and for vm only while the VM is running.
func connectBackend(ws *workspace.Resolved) (bool, error) {
	if ws.Backend != config.BackendVM {
		return true, nil
	}
	st, err := runningVM(vmStateDir(ws.Name))
	if err != nil || st == nil {
		return false, err
	}
	return true, setDockerHost(st.DockerSocket)
}

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

// startVM makes sure the workspace VM runs with the current configuration and
// points the Docker clients at it.
func startVM(ctx context.Context, cfg *config.Config, ws *workspace.Resolved) error {
	spec, err := buildVMSpec(ctx, cfg, ws)
	if err != nil {
		return err
	}
	st, err := runningVM(spec.StateDir)
	if err != nil {
		return err
	}
	if st != nil {
		if old, err := vm.ReadSpec(spec.StateDir); err == nil && sameVMSpec(*old, spec) {
			return setDockerHost(st.DockerSocket)
		}
		_, _ = color.New(color.FgYellow).Printf("Workspace %s VM configuration changed, restarting the VM...\n", ws.Name)
		if err := stopVM(ws.Name, false); err != nil {
			return err
		}
	}
	if err := vm.WriteSpec(spec); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate jailoc executable: %w", err)
	}
	logPath := filepath.Join(spec.StateDir, "supervisor.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open vm log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	_ = os.Remove(vm.StateFile(spec.StateDir))

	_, _ = color.New(color.FgCyan).Printf("Starting VM for workspace %s...\n", ws.Name)
	sup := exec.Command(exe, "vm-supervise", spec.StateDir) //nolint:gosec // G204: re-executes jailoc itself
	sup.Stdout, sup.Stderr = logFile, logFile
	sup.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := sup.Start(); err != nil {
		return fmt.Errorf("start vm supervisor: %w", err)
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
			return fmt.Errorf("start vm: %w", ctx.Err())
		case err := <-exited:
			return fmt.Errorf("vm supervisor exited (%v): %s", err, vmFailure(spec.StateDir, logPath))
		case <-deadline.C:
			_ = sup.Process.Signal(syscall.SIGTERM)
			return fmt.Errorf("vm not ready after %s: %s", vmStartTimeout, vmFailure(spec.StateDir, logPath))
		case <-tick.C:
			st, err := vm.ReadState(spec.StateDir)
			if err != nil || st.PID != sup.Process.Pid {
				continue
			}
			if st.Ready {
				return setDockerHost(st.DockerSocket)
			}
			if st.Error != "" {
				return fmt.Errorf("start vm: %s", st.Error)
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

// stopVM stops the workspace VM supervisor and, with purge, deletes the VM
// state including the Docker data disk.
func stopVM(name string, purge bool) error {
	stateDir := vmStateDir(name)
	if st, err := vm.ReadState(stateDir); err == nil && processAlive(st.PID) {
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
	}
	if purge {
		if err := os.RemoveAll(stateDir); err != nil {
			return fmt.Errorf("delete vm state: %w", err)
		}
	}
	return nil
}

func sameVMSpec(a, b vm.Spec) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// buildVMSpec derives the VM of a workspace from its configuration.
func buildVMSpec(ctx context.Context, cfg *config.Config, ws *workspace.Resolved) (vm.Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return vm.Spec{}, fmt.Errorf("locate jailoc executable: %w", err)
	}
	runner, err := vmRunnerPath(cfg.VM.Runner, exe)
	if err != nil {
		return vm.Spec{}, err
	}
	shares, err := vmShares(ws)
	if err != nil {
		return vm.Spec{}, err
	}
	memMiB, err := vmMemoryMiB(ws.Memory)
	if err != nil {
		return vm.Spec{}, err
	}
	policy, err := vmPolicy(ctx, ws)
	if err != nil {
		return vm.Spec{}, err
	}
	spec := vm.Spec{
		Workspace:  ws.Name,
		StateDir:   vmStateDir(ws.Name),
		Image:      vm.DefaultImage,
		CPUs:       uint32(math.Ceil(ws.CPU)) + 1,
		MemoryMiB:  memMiB,
		DiskSize:   vm.DefaultDiskSize,
		Shares:     shares,
		Policy:     policy,
		RunnerPath: runner,
		LibDir:     vmLibDir(cfg.VM.LibDir),
		ImageCache: filepath.Join(vmRootDir(), "images"),
		Broker:     []string{exe, "netbroker"},
	}
	if ws.ExposePort {
		spec.Forwards = []vm.Forward{{HostPort: ws.Port, GuestPort: ws.Port}}
	}
	return spec, nil
}

func vmRunnerPath(configured, exe string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	next := filepath.Join(filepath.Dir(exe), vmRunnerBinary)
	if _, err := os.Stat(next); err == nil {
		return next, nil
	}
	if p, err := exec.LookPath(vmRunnerBinary); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found next to jailoc or on PATH; set runner in the [vm] section of the config", vmRunnerBinary)
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

// vmShares lists the host directories the guest needs at their host paths:
// workspace paths read-write, mount sources in their mode, and the
// workspace's own jailoc config and cache directories read-only.
func vmShares(ws *workspace.Resolved) ([]vm.Share, error) {
	readOnly := map[string]bool{}
	add := func(path string, ro bool) {
		if prev, ok := readOnly[path]; ok {
			readOnly[path] = prev && ro
			return
		}
		readOnly[path] = ro
	}
	for _, p := range ws.Paths {
		add(p, false)
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
			return nil, fmt.Errorf("workspace %q: mount %q: backend \"vm\" shares directories only", ws.Name, spec)
		}
		add(host, m.Mode == "ro")
	}
	add(filepath.Clean(filepath.Join(config.ConfigDir(), "workspaces", ws.Name)), true)
	add(filepath.Clean(ComposeCacheDir(ws.Name)), true)

	paths := make([]string, 0, len(readOnly))
	for p := range readOnly {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	shares := make([]vm.Share, 0, len(paths))
	for _, p := range paths {
		shares = append(shares, vm.Share{HostPath: p, ReadOnly: readOnly[p]})
	}
	return shares, nil
}

// vmMemoryMiB sizes the VM: the container memory limit plus room for the
// guest kernel and dockerd.
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

// vmComposeParams adapts compose parameters to a workspace VM: the port is
// published on the guest NIC for the broker forward, files the guest needs
// come from the workspace cache directory, and Docker is the VM's dockerd.
func vmComposeParams(p *compose.ComposeParams, cacheDir string) error {
	p.PublishAddr = vm.GuestIP
	p.TUIDir = filepath.Clean(cacheDir)
	p.VMBackend = true
	if p.GitConfig != "" {
		copied, err := copyForVM(p.GitConfig, cacheDir, "gitconfig")
		if err != nil {
			return err
		}
		p.GitConfig = copied
	}
	secrets, err := vmSecretEnv(p.Secrets)
	if err != nil {
		return err
	}
	p.Secrets = secrets
	return nil
}

// copyForVM copies a host file into the workspace cache directory, which the
// guest sees, and returns the copy's path.
func copyForVM(src, cacheDir, name string) (string, error) {
	data, err := os.ReadFile(src) //nolint:gosec // G304: user-configured host file
	if err != nil {
		return "", fmt.Errorf("read %s: %w", src, err)
	}
	dst := filepath.Join(cacheDir, name)
	if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec // G306: read by the agent user in the guest
		return "", fmt.Errorf("write %s: %w", dst, err)
	}
	return dst, nil
}

// vmSecretEnv turns file secrets into environment-sourced compose secrets:
// the guest daemon cannot read host files outside its shares.
func vmSecretEnv(specs []compose.SecretSpec) ([]compose.SecretSpec, error) {
	out := make([]compose.SecretSpec, 0, len(specs))
	for i, s := range specs {
		if s.File == "" {
			out = append(out, s)
			continue
		}
		data, err := os.ReadFile(s.File)
		if err != nil {
			return nil, fmt.Errorf("read secret %q: %w", s.Name, err)
		}
		envName := "JAILOC_VM_SECRET_" + strconv.Itoa(i)
		if err := os.Setenv(envName, string(data)); err != nil {
			return nil, fmt.Errorf("export secret %q: %w", s.Name, err)
		}
		out = append(out, compose.SecretSpec{Name: s.Name, Environment: envName})
	}
	return out, nil
}
