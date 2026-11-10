//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	vmStartTimeout = 5 * time.Minute
	vmStopTimeout  = 45 * time.Second
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

func setDockerHost(socket string) (func(), error) {
	orig, set := os.LookupEnv("DOCKER_HOST")
	if err := os.Setenv("DOCKER_HOST", "unix://"+socket); err != nil {
		return nil, fmt.Errorf("set DOCKER_HOST: %w", err)
	}
	restore := func() {
		if set {
			_ = os.Setenv("DOCKER_HOST", orig)
		} else {
			_ = os.Unsetenv("DOCKER_HOST")
		}
	}
	return restore, nil
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

// workspaceRunning reports whether the VM of ws runs.
func workspaceRunning(ws *workspace.Resolved) (bool, error) {
	st, err := runningVM(vmStateDir(ws.Name))
	return st != nil, err
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
	tick := time.NewTicker(50 * time.Millisecond) // state.json is tiny; a short poll returns as soon as the VM is ready
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
	if strings.Contains(string(data), "IrqsExhausted") {
		return "the VM has more devices than libkrun can attach on this host (on x86_64 about 11 shared directories besides jailoc's own); remove some paths or mounts"
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
// binary: the configured runner or the runtime embedded in jailoc (release
// builds), and the guest binary embedded in jailoc (release builds) or next
// to the runner.
type vmRuntimeFiles struct {
	runner, libDir, guest string
}

func vmRuntime(cfg *config.Config) (vmRuntimeFiles, error) {
	var rt vmRuntimeFiles
	if cfg.VM.Runner != "" {
		rt = vmRuntimeFiles{runner: cfg.VM.Runner, libDir: vmLibDir(cfg.VM.LibDir)}
	} else {
		runner, libDir, err := vm.EnsureRuntime(filepath.Join(vmRootDir(), "runtime"), os.Stdout)
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
	guestConfigDir = "/etc/jailoc"
	guestCacheDir  = "/etc/jailoc-cache"
	guestUserCache = "/home/agent/.cache"
)

// vmShares lists the host directories the workspace VM mounts: workspace
// paths read-write at their host paths and mount sources at their container
// paths. jailoc's own directories reach the guest through the internal
// share (see vmGuestData).
func vmShares(ws *workspace.Resolved) ([]vm.Share, error) {
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
			return nil, fmt.Errorf("workspace %q: mount %q: backend \"vm\" shares directories only; use snapshots for a read-only copy of a file", ws.Name, spec)
		}
		add(host, m.Container, m.Mode == "ro")
	}
	// A named profile shares the agent's state directories that no mount
	// covers; vmGuestData keeps the rest per workspace.
	if name := workspaceProfile(ws); name != "" {
		for _, d := range ws.Agent.State {
			if _, ok := byGuest[filepath.Clean(d.Path)]; ok {
				continue
			}
			host := filepath.Join(profilesDir(), name, d.Name)
			if err := os.MkdirAll(host, 0o700); err != nil {
				return nil, fmt.Errorf("workspace %q: create profile directory %q: %w", ws.Name, host, err)
			}
			if _, _, ok := ws.Agent.TUIPlugin.SettingsFile(); ok {
				root, err := os.OpenRoot(host)
				if err != nil {
					return nil, fmt.Errorf("workspace %q: open profile directory %q: %w", ws.Name, host, err)
				}
				addStatusLine(root, ".", ws.Agent)
				_ = root.Close()
			}
			add(host, d.Path, false)
		}
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
	if limit := vm.MaxShares(ws.EnableDocker); len(shares) > limit {
		return nil, fmt.Errorf("workspace %q: %d shared directories (paths, mounts and profile state) exceed the limit of %d a VM on this host boots with; list a common parent directory in paths instead of its subdirectories", ws.Name, len(shares), limit)
	}
	if err := checkNestedShares(shares); err != nil {
		return nil, fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	return shares, nil
}

// checkNestedShares rejects a share whose host path runs through a symbolic
// link inside a read-write share. The workspace can replace such a link,
// which would make the next start share a different host directory. The
// share path is followed link by link, so a path that reaches a read-write
// share through an alias of its directory is checked too.
func checkNestedShares(shares []vm.Share) error {
	var roots, names []string
	for _, w := range shares {
		if w.ReadOnly {
			continue
		}
		if real, err := filepath.EvalSymlinks(w.HostPath); err == nil {
			roots = append(roots, real)
			names = append(names, w.HostPath)
		}
	}
	for _, s := range shares {
		link, i, err := linkInside(s.HostPath, roots)
		if err != nil {
			return fmt.Errorf("share %s: %w", s.HostPath, err)
		}
		if link != "" {
			return fmt.Errorf("share %s: %s is a symbolic link inside the read-write share %s; use the path it points to", s.HostPath, link, names[i])
		}
	}
	return nil
}

// maxLinkHops bounds the symbolic links linkInside follows.
const maxLinkHops = 255

// linkInside follows path from the file system root, resolving symbolic
// links one at a time, and returns the first link it passes through that
// lies strictly inside one of roots, which must be free of symbolic links,
// with the index of that root. It returns "" and -1 when there is none or
// the rest of the path does not exist.
func linkInside(path string, roots []string) (string, int, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", -1, fmt.Errorf("resolve %s: %w", path, err)
	}
	sep := string(filepath.Separator)
	cur, rest, hops := sep, strings.Split(path, sep), 0
	for len(rest) > 0 {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			return "", -1, nil
		}
		if err != nil {
			return "", -1, fmt.Errorf("resolve %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		for i, r := range roots {
			if rel, err := filepath.Rel(r, next); err == nil && rel != "." && filepath.IsLocal(rel) {
				return next, i, nil
			}
		}
		if hops++; hops > maxLinkHops {
			return "", -1, fmt.Errorf("resolve %s: too many levels of symbolic links", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", -1, fmt.Errorf("resolve %s: %w", path, err)
		}
		if filepath.IsAbs(target) {
			cur = sep
		}
		rest = append(strings.Split(target, sep), rest...)
	}
	return "", -1, nil
}

// vmMinMemoryMiB is the smallest workspace VM that boots reliably.
const vmMinMemoryMiB = 512

// vmMemoryMiB sizes the workspace VM: the whole VM, including the guest
// kernel and its Docker daemon, gets the workspace memory limit.
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
	if mib < vmMinMemoryMiB {
		return 0, fmt.Errorf("memory limit %q is below the %d MiB a workspace VM needs", limit, vmMinMemoryMiB)
	}
	return uint32(mib), nil //nolint:gosec // G115: bounded by the check above
}

// vmMinDockerDiskSize is the smallest Docker data disk a workspace accepts.
const vmMinDockerDiskSize = 1 << 30

// vmDockerDiskSize parses the docker_disk setting into the size of the sparse Docker
// data disk in bytes, rounded up to whole MiB.
func vmDockerDiskSize(size string) (int64, error) {
	if size == "" {
		return 0, errors.New("empty docker_disk")
	}
	mult := int64(1)
	switch unit := strings.ToLower(size[len(size)-1:]); unit {
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	}
	digits := strings.TrimRight(size, "kmgKMG")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid docker_disk %q", size)
	}
	if n > (math.MaxInt64-(1<<20-1))/mult {
		return 0, fmt.Errorf("docker_disk %q is too large", size)
	}
	bytes := (n*mult + (1<<20 - 1)) &^ (1<<20 - 1)
	if bytes < vmMinDockerDiskSize {
		return 0, fmt.Errorf("docker_disk %q is below the %d GiB minimum", size, vmMinDockerDiskSize>>30)
	}
	return bytes, nil
}

type vmRule struct {
	Name  string   `json:"name,omitempty"`
	Names []string `json:"names,omitempty"`
	CIDRs []string `json:"cidrs,omitempty"`
	Proto string   `json:"proto,omitempty"`
	Ports []string `json:"ports,omitempty"`
}

// vmPolicy builds the broker policy JSON from the workspace's resolved network
// rules and DNS settings. @list references are expanded on the host.
func vmPolicy(_ context.Context, ws *workspace.Resolved) ([]byte, error) {
	allow := []vmRule{}
	deny := []vmRule{}

	rules := ws.NetworkRules
	if len(rules) == 0 && (len(ws.AllowedNetworks) > 0 || len(ws.AllowedHosts) > 0 || len(ws.DNSBlockedZones) > 0) {
		if len(ws.AllowedHosts) > 0 {
			rules = append(rules, config.NetworkRule{Name: "legacy", Allow: ws.AllowedHosts, Proto: config.RuleProtoAny})
		}
		if len(ws.AllowedNetworks) > 0 {
			rules = append(rules, config.NetworkRule{Name: "legacy", Allow: ws.AllowedNetworks, Proto: config.RuleProtoAny})
		}
		if len(ws.DNSBlockedZones) > 0 {
			var blocked []string
			for _, z := range ws.DNSBlockedZones {
				z = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(z)), ".")
				if z != "" {
					blocked = append(blocked, z, "*."+z)
				}
			}
			if len(blocked) > 0 {
				rules = append(rules, config.NetworkRule{Name: "legacy", Deny: blocked, Proto: config.RuleProtoAny})
			}
		}
	}

	for _, r := range rules {
		isAllow := len(r.Allow) > 0
		targets := r.Allow
		if !isAllow {
			targets = r.Deny
		}
		br, err := compileVMRule(r.Name, r.Proto, r.Ports, targets)
		if err != nil {
			return nil, err
		}
		if isAllow {
			allow = append(allow, br)
		} else {
			deny = append(deny, br)
		}
	}

	policy := struct {
		DNS   string   `json:"dns,omitempty"`
		Deny  []vmRule `json:"deny"`
		Allow []vmRule `json:"allow"`
		IPv6  bool     `json:"ipv6,omitempty"`
	}{
		DNS:   ws.DNSUpstream,
		Deny:  deny,
		Allow: allow,
		IPv6:  ws.IPv6,
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("encode broker policy: %w", err)
	}
	return data, nil
}

func compileVMRule(name, proto string, ports []config.RulePort, targets []string) (vmRule, error) {
	if proto == "" {
		proto = config.RuleProtoTCP
	}
	var portStrs []string
	for _, p := range ports {
		portStrs = append(portStrs, string(p))
	}
	out := vmRule{Name: name, Proto: proto, Ports: portStrs}
	seenNames := map[string]bool{}
	seenCIDRs := map[string]bool{}
	for _, t := range targets {
		kind, norm, err := config.ParseRuleTarget(t)
		if err != nil {
			return vmRule{}, err
		}
		switch kind {
		case "cidr":
			if !seenCIDRs[norm] {
				seenCIDRs[norm] = true
				out.CIDRs = append(out.CIDRs, norm)
			}
		case "wildcard", "name":
			if !seenNames[norm] {
				seenNames[norm] = true
				out.Names = append(out.Names, norm)
			}
		}
	}
	return out, nil
}
