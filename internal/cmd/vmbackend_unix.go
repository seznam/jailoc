//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/agent"
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
		return vm.Run(cmd.Context(), *spec)
	},
}

func init() {
	rootCmd.AddCommand(vmSuperviseCmd)
}

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
				return nil, fmt.Errorf("start vm: %s", vmFailure(spec.StateDir, logPath))
			}
		}
	}
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
		time.Sleep(10 * time.Millisecond)
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

// defaultVMRunner and defaultVMLibDir name a VM runtime that the package
// ships next to jailoc. Packagers set them at link time, for example
// -ldflags "-X github.com/seznam/jailoc/internal/cmd.defaultVMRunner=/nix/store/...".
var (
	defaultVMRunner string
	defaultVMLibDir string
)

// vmRuntimeFiles is the runner, its library directory and the jailoc-guest
// binary: the configured runner, the runtime provided by the package
// (defaultVMRunner) or the runtime embedded in jailoc (release builds), and
// the guest binary embedded in jailoc (release builds) or next to the runner.
type vmRuntimeFiles struct {
	runner, libDir, guest string
}

func vmRuntime(cfg *config.Config) (vmRuntimeFiles, error) {
	return vmRuntimeWith(cfg, defaultVMRunner, defaultVMLibDir)
}

// selectVMRuntime picks the runtime source: the configured runner, then the
// package runner, then the embedded runtime (embedded is true). The package
// lib dir applies only together with the package runner.
func selectVMRuntime(cfg *config.Config, pkgRunner, pkgLibDir string) (runner, libDir string, embedded bool) {
	switch {
	case cfg.VM.Runner != "":
		return cfg.VM.Runner, vmLibDir(cfg.VM.LibDir), false
	case pkgRunner != "":
		return pkgRunner, pkgLibDir, false
	default:
		return "", "", true
	}
}

func vmRuntimeWith(cfg *config.Config, pkgRunner, pkgLibDir string) (vmRuntimeFiles, error) {
	var rt vmRuntimeFiles
	runner, libDir, embedded := selectVMRuntime(cfg, pkgRunner, pkgLibDir)
	if embedded {
		var err error
		runner, libDir, err = vm.EnsureRuntime(filepath.Join(vmRootDir(), "runtime"), os.Stdout)
		if err != nil {
			return rt, fmt.Errorf("prepare VM runtime: %w", err)
		}
	} else if cfg.VM.Runner == "" {
		if _, err := os.Stat(runner); err != nil {
			return rt, fmt.Errorf("VM runner %s provided by this jailoc package is missing: %w; reinstall jailoc or set runner and lib_dir in the [vm] section of the config", runner, err)
		}
	}
	rt = vmRuntimeFiles{runner: runner, libDir: libDir}
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
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: path %q: %w", ws.Name, p, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace %q: path %q is not a directory", ws.Name, p)
		}
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
		case errors.Is(err, os.ErrNotExist) && m.Mode == "ro" && !isDefaultMount(ws.Agent, host, m.Container):
			// An empty directory created here could never fill up from the guest.
			return nil, fmt.Errorf("workspace %q: mount %q: source %q does not exist; create it or fix the path, jailoc only creates missing sources of read-write mounts", ws.Name, spec, host)
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
	if limit := vm.MaxShares(ws.EnableDocker, ws.TmpStorage == config.TmpStorageDisk); len(shares) > limit {
		return nil, fmt.Errorf("workspace %q: %d shared directories (paths, mounts and profile state) exceed the limit of %d a VM on this host boots with; list a common parent directory in paths instead of its subdirectories", ws.Name, len(shares), limit)
	}
	if err := checkNestedShares(shares); err != nil {
		return nil, fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	return shares, nil
}

// isDefaultMount reports whether host:container is one of the harness's
// built-in mounts (such as ~/.agents), whose missing source vmShares
// creates even when read-only, so a host without it still boots.
func isDefaultMount(h agent.Harness, host, container string) bool {
	for _, spec := range h.DefaultMounts() {
		// Built-in mounts are constants that parse and expand; one that
		// doesn't is simply not a match.
		m, err := config.ParseMount(spec)
		if err != nil {
			continue
		}
		expanded, err := config.ExpandPath(m.Host)
		if err != nil {
			continue
		}
		if filepath.Clean(expanded) == filepath.Clean(host) && filepath.Clean(m.Container) == filepath.Clean(container) {
			return true
		}
	}
	return false
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

// warnDanglingShareLinks warns about the symbolic links danglingShareLinks
// finds in the top level of the workspace shares, counting the snapshot
// destinations as available in the VM.
func warnDanglingShareLinks(ws *workspace.Resolved, shares []vm.Share, snapshots []snapshot) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	covered := make([]string, 0, len(shares)+len(snapshots))
	for _, s := range shares {
		covered = append(covered, s.GuestPath)
	}
	for _, s := range snapshots {
		covered = append(covered, filepath.Clean(s.Container))
	}
	for _, l := range danglingShareLinks(shares, covered, home) {
		_, _ = color.New(color.FgYellow).Fprintln(os.Stderr, danglingLinkWarning(ws.Name, l))
	}
}

// danglingLinkWarning describes l of workspace name. The paths are quoted:
// the workspace chooses the link names and targets, so they must not reach
// the host terminal as raw control characters.
func danglingLinkWarning(name string, l shareLink) string {
	fix := fmt.Sprintf("mount or snapshot its target at %q", l.guest)
	if !l.dir {
		// Mounts share directories only.
		fix = fmt.Sprintf("snapshot its target at %q, or mount the directory that contains it at %q", l.guest, filepath.Dir(l.guest))
	}
	return fmt.Sprintf("WARNING: workspace %q: %q links to %q, which no share, mount or snapshot covers, so the link is dangling in the VM; %s", name, l.link, l.target, fix)
}

// shareLink is a symbolic link in a share whose target the VM cannot see.
// dir reports whether the target is a directory.
type shareLink struct {
	link, target, guest string
	dir                 bool
}

// danglingShareLinks returns the symbolic links directly inside a share
// that resolve on the host to a path under home but point, as seen from the
// guest, outside every covered guest path, such as a dotfiles link to an
// instructions file. Links into the image, links that are dangling on the
// host too and deeper directories are not checked, which keeps the scan
// cheap.
func danglingShareLinks(shares []vm.Share, covered []string, home string) []shareLink {
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil
	}
	var links []shareLink
	for _, s := range shares {
		// A share that can't be listed fails the VM start on its own.
		entries, err := os.ReadDir(s.HostPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			link := filepath.Join(s.HostPath, e.Name())
			target, err := os.Readlink(link)
			if err != nil {
				continue
			}
			guest := target
			if !filepath.IsAbs(guest) {
				guest = filepath.Join(s.GuestPath, guest)
			}
			if guestCovered(filepath.Clean(guest), covered) {
				continue
			}
			real, err := filepath.EvalSymlinks(link)
			if err != nil || !pathWithin(real, home) {
				continue
			}
			info, err := os.Stat(real)
			if err != nil {
				continue
			}
			links = append(links, shareLink{link: link, target: real, guest: filepath.Clean(guest), dir: info.IsDir()})
		}
	}
	return links
}

// guestCovered reports whether the guest path p lies in one of covered.
func guestCovered(p string, covered []string) bool {
	return slices.ContainsFunc(covered, func(dir string) bool { return pathWithin(p, dir) })
}

// pathWithin reports whether p is dir or lies under it.
func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && filepath.IsLocal(rel)
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
	return vmDiskSize("docker_disk", size)
}

type vmRule struct {
	Name  string   `json:"name,omitempty"`
	Names []string `json:"names,omitempty"`
	CIDRs []string `json:"cidrs,omitempty"`
	Proto string   `json:"proto,omitempty"`
	Ports []string `json:"ports,omitempty"`
	HTTP  *vmHTTP  `json:"http,omitempty"`
	// Quiet marks the events the rule decides (log = false); the broker
	// still records them.
	Quiet bool `json:"quiet,omitempty"`
}

// vmHTTP narrows a vmRule to HTTP requests. Empty methods or paths match any.
type vmHTTP struct {
	Methods []string `json:"methods,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

// vmInspect locates the CA the broker signs inspected TLS connections with.
// The key stays on the host; upstream_ca is the explicit ca_bundle appended to
// the host trust roots for upstream verification.
type vmInspect struct {
	CACert     string `json:"ca_cert"`
	CAKey      string `json:"ca_key"`
	UpstreamCA string `json:"upstream_ca,omitempty"`
}

// legacyResolver looks up legacy allowed_hosts names on the host.
type legacyResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
}

// hostResolver is the legacyResolver of up and reload; tests replace it.
var hostResolver legacyResolver = net.DefaultResolver

// legacyAliasWarnings reports legacy allowed_hosts names that are CNAME
// aliases of a name outside zones and resolve to a private address.
// allowed_hosts opened such a host, but now only the alias is an internal
// zone, so the broker drops the private answer of its canonical name and the
// host does not resolve in the VM. The user decides which zone to trust.
func legacyAliasWarnings(ctx context.Context, r legacyResolver, hosts, zones []string) []string {
	trusted, _ := config.LegacyHostZones(hosts)
	for _, z := range zones {
		trusted = append(trusted, config.NormalizeInternalZone(z))
	}
	var out []string
	for _, h := range hosts {
		if strings.HasPrefix(h, "*.") || net.ParseIP(h) != nil || strings.Contains(h, "/") || config.ValidateInternalZone(h) != nil {
			continue
		}
		canonical, err := r.LookupCNAME(ctx, h)
		if err != nil {
			continue // an unresolvable host is reported where it is resolved
		}
		c := config.NormalizeInternalZone(canonical)
		if c == "" || c == config.NormalizeInternalZone(h) || inZones(c, trusted) || !resolvesPrivate(ctx, r, h) {
			continue
		}
		out = append(out, fmt.Sprintf("allowed host %q is an alias of %q, outside internal_zones: its private address does not resolve in the VM; add the zone of %q to internal_zones", h, c, c))
	}
	return out
}

func inZones(name string, zones []string) bool {
	return slices.ContainsFunc(zones, func(z string) bool { return name == z || strings.HasSuffix(name, "."+z) })
}

func resolvesPrivate(ctx context.Context, r legacyResolver, host string) bool {
	ips, err := r.LookupIP(ctx, "ip", host)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(ips, func(ip net.IP) bool {
		addr, ok := netip.AddrFromSlice(ip)
		return ok && !isPublicLegacyAddr(addr.Unmap())
	})
}

// vmPolicy builds the broker policy JSON from the workspace's resolved network
// rules and DNS settings. @list references are expanded on the host.
func vmPolicy(ctx context.Context, ws *workspace.Resolved) ([]byte, error) {
	return buildVMPolicy(ctx, ws, false)
}

// vmBuilderPolicy builds the broker policy of the isolated_build builder VM.
// Only the builder VM trusts the inspection CA, not the containers of the
// image build's RUN steps, so the builder does not inspect: http allow rules
// open their targets like plain ones, http deny rules (which only deny single
// requests) are left out, and there is no inspect section.
func vmBuilderPolicy(ctx context.Context, ws *workspace.Resolved) ([]byte, error) {
	return buildVMPolicy(ctx, ws, true)
}

func buildVMPolicy(ctx context.Context, ws *workspace.Resolved, builder bool) ([]byte, error) {
	allow := []vmRule{}
	deny := []vmRule{}

	rules := slices.Clone(ws.NetworkRules)
	if len(rules) == 0 && (len(ws.AllowedNetworks) > 0 || len(ws.AllowedHosts) > 0 || len(ws.DNSBlockedZones) > 0) {
		if len(ws.AllowedHosts) > 0 {
			rules = append(rules, config.NetworkRule{Name: "legacy", Allow: slices.Clone(ws.AllowedHosts), Proto: config.RuleProtoAny})
		}
		if len(ws.AllowedNetworks) > 0 {
			rules = append(rules, config.NetworkRule{Name: "legacy", Allow: slices.Clone(ws.AllowedNetworks), Proto: config.RuleProtoAny})
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

	// Legacy allowed_hosts names open private addresses through the broker's
	// DNS: Resolve adds them to InternalZones, and their legacy allow rule
	// matches the cached names. They are not resolved into CIDRs here, which
	// would open an address an alias outside the internal zones points to.
	for _, w := range legacyAliasWarnings(ctx, hostResolver, ws.AllowedHosts, ws.InternalZones) {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: %s\n", w)
	}
	hasHTTP := slices.ContainsFunc(rules, func(r config.NetworkRule) bool { return r.HTTP != nil })
	if builder && hasHTTP {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: %s\n", "the isolated image build does not inspect HTTPS, so the http method and path limits of the network rules do not apply during the build; the rules still limit hosts and ports")
	}
	for _, r := range rules {
		isAllow := len(r.Allow) > 0
		targets := r.Allow
		if !isAllow {
			targets = r.Deny
		}
		httpMatch := r.HTTP
		if builder && httpMatch != nil {
			if !isAllow {
				continue
			}
			httpMatch = nil
		}
		br, err := compileVMRule(r.Name, r.Proto, r.Ports, targets, httpMatch)
		if err != nil {
			return nil, err
		}
		br.Quiet = r.Quiet()
		if isAllow {
			allow = append(allow, br)
		} else {
			deny = append(deny, br)
		}
	}

	var inspect *vmInspect
	if hasHTTP && !builder {
		var err error
		if inspect, err = vmInspectPolicy(ws); err != nil {
			return nil, err
		}
	}

	policy := struct {
		DNS           string     `json:"dns,omitempty"`
		Deny          []vmRule   `json:"deny"`
		Allow         []vmRule   `json:"allow"`
		InternalZones []string   `json:"internal_zones,omitempty"`
		IPv6          bool       `json:"ipv6,omitempty"`
		Inspect       *vmInspect `json:"inspect,omitempty"`
	}{
		DNS:           ws.DNSUpstream,
		Deny:          deny,
		Allow:         allow,
		InternalZones: ws.InternalZones,
		IPv6:          ws.IPv6,
		Inspect:       inspect,
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("encode broker policy: %w", err)
	}
	return data, nil
}

// vmInspectPolicy creates the workspace inspection CA if needed and describes
// it for the broker. upstream_ca is the expanded explicit ca_bundle path; the
// automatically forwarded bundle is already part of the host trust roots.
func vmInspectPolicy(ws *workspace.Resolved) (*vmInspect, error) {
	certPath, keyPath, _, err := ensureInspectCA(ws.Name)
	if err != nil {
		return nil, fmt.Errorf("prepare inspection CA for workspace %q: %w", ws.Name, err)
	}
	inspect := &vmInspect{CACert: certPath, CAKey: keyPath}
	if path, custom := ws.CABundle.Path(); custom {
		if inspect.UpstreamCA, err = expandCABundlePath(path); err != nil {
			return nil, fmt.Errorf("expand ca_bundle path: %w", err)
		}
	}
	return inspect, nil
}

var legacyPrivateV4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func isPublicLegacyAddr(addr netip.Addr) bool {
	if addr.Is4() {
		for _, pfx := range legacyPrivateV4 {
			if pfx.Contains(addr) {
				return false
			}
		}
		return true
	}
	return !addr.IsPrivate() && netip.MustParsePrefix("2000::/3").Contains(addr)
}

func compileVMRule(name, proto string, ports []config.RulePort, targets []string, httpMatch *config.HTTPMatch) (vmRule, error) {
	if proto == "" {
		proto = config.RuleProtoTCP
	}
	var portStrs []string
	for _, p := range ports {
		canon, err := config.CanonicalRulePort(p)
		if err != nil {
			return vmRule{}, err
		}
		portStrs = append(portStrs, canon)
	}
	out := vmRule{Name: name, Proto: proto, Ports: portStrs}
	if httpMatch != nil {
		h, err := compileVMHTTP(httpMatch)
		if err != nil {
			return vmRule{}, err
		}
		out.HTTP = h
	}
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

// compileVMHTTP validates an http table and normalizes its methods to upper
// case without duplicates; "*" anywhere makes the list the single entry "*".
func compileVMHTTP(m *config.HTTPMatch) (*vmHTTP, error) {
	out := &vmHTTP{}
	for _, method := range m.Methods {
		norm, err := config.NormalizeHTTPMethod(method)
		if err != nil {
			return nil, err
		}
		if norm == "*" {
			out.Methods = []string{"*"}
			break
		}
		if !slices.Contains(out.Methods, norm) {
			out.Methods = append(out.Methods, norm)
		}
	}
	for _, p := range m.Paths {
		if err := config.ValidateHTTPPathPattern(p); err != nil {
			return nil, err
		}
		if !slices.Contains(out.Paths, p) {
			out.Paths = append(out.Paths, p)
		}
	}
	return out, nil
}
