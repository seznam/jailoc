//go:build darwin || linux

package cmd

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/guestbin"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

const reloadTimeout = 15 * time.Second

// reloadVM applies the current configuration to a running workspace VM
// without restarting it: network policy (allowed_hosts, allowed_networks,
// filtered_dns), published ports, the SSH agent, the CA bundle, snapshots,
// environment and secrets. Settings that need a new VM are reported, not
// applied.
func reloadVM(ctx context.Context, ws *workspace.Resolved) error {
	stateDir := vmStateDir(ws.Name)
	st, err := runningVM(stateDir)
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("workspace %q is not running; start it with `jailoc up %s`", ws.Name, ws.Name)
	}
	runningSpec, err := vm.ReadSpec(stateDir)
	if err != nil {
		return err
	}
	running := *runningSpec
	if running.Guest == nil {
		return fmt.Errorf("workspace %q: running VM has no guest configuration; run `jailoc restart %s`", ws.Name, ws.Name)
	}
	want, snapshots, err := reloadSpec(ctx, ws, running)
	if err != nil {
		return err
	}
	payload, err := secretsPayload(ctx, ws, want)
	if err != nil {
		return err
	}
	binds := running.Guest.Snapshots
	if snapshotsReloadable(running) {
		if binds, err = stageSnapshots(stateDir, snapshots, snapshotGeneration(running.Guest.Snapshots)); err != nil {
			return fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
	}

	next := running
	next.Policy = want.Policy
	next.Forwards = want.Forwards
	next.SSHAgent = want.SSHAgent
	g := *running.Guest
	g.CABundle = want.Guest.CABundle
	g.SSHAgent = want.Guest.SSHAgent
	g.Snapshots = binds
	next.Guest = &g

	if err := reloadBroker(next, st.BrokerPID); err != nil {
		return fmt.Errorf("reload network broker of workspace %q: %w", ws.Name, err)
	}
	if err := reloadGuest(stateDir, g); err != nil {
		return fmt.Errorf("reload workspace %q: %w", ws.Name, err)
	}
	if err := vm.WriteSpec(next); err != nil {
		return err
	}
	if err := deliverSecrets(ctx, stateDir, payload); err != nil {
		return fmt.Errorf("deliver secrets to workspace %q: %w", ws.Name, err)
	}
	_, _ = color.New(color.FgGreen).Printf("Workspace %s reloaded\n", ws.Name)
	if changed := restartOnlyChanges(running, want); len(changed) > 0 {
		_, _ = color.New(color.FgYellow).Printf("Changes to %s apply after `jailoc restart %s` (this ends running sessions)\n", joinList(changed), ws.Name)
	}
	return nil
}

// reloadSpec computes the workspace spec for the current configuration
// without building an image, and the snapshots to stage.
func reloadSpec(ctx context.Context, ws *workspace.Resolved, running vm.Spec) (vm.Spec, []config.Mount, error) {
	if err := validateSecretSources(ws); err != nil {
		return vm.Spec{}, nil, fmt.Errorf("validate secrets for workspace %q: %w", ws.Name, err)
	}
	if err := materializeCABundle(ws); err != nil {
		return vm.Spec{}, nil, fmt.Errorf("prepare CA bundle for workspace %q: %w", ws.Name, err)
	}
	cacheDir := workspaceCacheDir(ws.Name)
	if err := writeAgentFiles(ws, cacheDir); err != nil {
		return vm.Spec{}, nil, err
	}
	shares, err := vmShares(ws)
	if err != nil {
		return vm.Spec{}, nil, err
	}
	// Refreshes the configuration and cache copies the guest reads.
	internal, err := vmGuestData(ws, cacheDir, shares)
	if err != nil {
		return vm.Spec{}, nil, err
	}
	snapshots, err := vmSnapshots(ws)
	if err != nil {
		return vm.Spec{}, nil, err
	}
	if err := checkSnapshots(ws, snapshots, shares); err != nil {
		return vm.Spec{}, nil, err
	}
	rt := vmRuntimeFiles{runner: running.RunnerPath, libDir: running.LibDir, guest: running.GuestBinary}
	spec, err := workspaceSpec(ctx, ws, rt, running.Image, shares, nil)
	if err != nil {
		return vm.Spec{}, nil, err
	}
	spec.Guest.Binds = internal
	spec.Docker = ws.EnableDocker
	if spec.Docker {
		spec.Guest.Docker = &guest.Docker{Disk: guest.DockerDisk}
	}
	spec.Guest.NoPrivilegeEscalation = ws.ProtectGit
	return spec, snapshots, nil
}

// snapshotsReloadable reports whether the running VM can rebind snapshots:
// it runs the current jailoc guest, which has the snapshots in its internal
// share. Otherwise restartOnlyChanges reports the guest difference.
func snapshotsReloadable(running vm.Spec) bool {
	current, err := guestbin.Path(filepath.Join(vmRootDir(), "guest"))
	return err == nil && filepath.Clean(current) == filepath.Clean(running.GuestBinary)
}

// restartOnlyChanges names the settings that differ between the running VM
// and the configuration but cannot change while the VM runs.
func restartOnlyChanges(running, want vm.Spec) []string {
	var out []string
	if !slices.Equal(sortedShares(running.Shares), sortedShares(want.Shares)) || !slices.Equal(running.Guest.Binds, want.Guest.Binds) {
		out = append(out, "paths and mounts")
	}
	if running.CPUs != want.CPUs {
		out = append(out, "cpu")
	}
	if running.MemoryMiB != want.MemoryMiB {
		out = append(out, "memory")
	}
	if running.Docker != want.Docker {
		out = append(out, "enable_docker")
	}
	if running.Guest.NoPrivilegeEscalation != want.Guest.NoPrivilegeEscalation {
		out = append(out, "protect_git")
	}
	// Specs written before jailoc ran other agents than OpenCode name none.
	agentChanged := cmp.Or(running.Agent, agent.Default) != want.Agent
	if agentChanged {
		out = append(out, "agent")
	}
	if !agentChanged && (!slices.Equal(running.Guest.Env, want.Guest.Env) || running.Guest.WorkDir != want.Guest.WorkDir) {
		out = append(out, "the working directory")
	}
	if current, err := guestbin.Path(filepath.Join(vmRootDir(), "guest")); err == nil && filepath.Clean(current) != filepath.Clean(running.GuestBinary) {
		out = append(out, "the jailoc guest (jailoc was upgraded)")
	}
	return out
}

func sortedShares(shares []vm.Share) []vm.Share {
	out := slices.Clone(shares)
	slices.SortFunc(out, func(a, b vm.Share) int {
		switch {
		case a.GuestPath < b.GuestPath:
			return -1
		case a.GuestPath > b.GuestPath:
			return 1
		}
		return 0
	})
	return out
}

func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	out := ""
	for i, s := range items {
		switch {
		case i == 0:
			out = s
		case i == len(items)-1:
			out += " and " + s
		default:
			out += ", " + s
		}
	}
	return out
}

// reloadBroker writes the policy and runtime files of spec, signals the
// broker and waits until it reports the reload in its log.
func reloadBroker(spec vm.Spec, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("broker pid unknown")
	}
	logPath := filepath.Join(spec.StateDir, "broker.jsonl")
	var offset int64
	if info, err := os.Stat(logPath); err == nil {
		offset = info.Size()
	}
	if err := vm.WritePolicy(spec); err != nil {
		return err
	}
	if err := vm.WriteBrokerRuntime(spec); err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		return fmt.Errorf("signal broker: %w", err)
	}
	deadline := time.Now().Add(reloadTimeout)
	for time.Now().Before(deadline) {
		if ev, ok := brokerReloadEvent(logPath, offset); ok {
			if ev.PolicyError != "" {
				return fmt.Errorf("invalid policy, the broker now denies all new connections: %s", ev.PolicyError)
			}
			if len(ev.RuntimeErrors) > 0 {
				return fmt.Errorf("published ports: %s", joinList(ev.RuntimeErrors))
			}
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("broker did not confirm the reload within %s", reloadTimeout)
}

type brokerReload struct {
	Event         string   `json:"event"`
	PolicyError   string   `json:"policy_error"`
	RuntimeErrors []string `json:"runtime_errors"`
}

// brokerReloadEvent returns the first reload event after offset in the log.
func brokerReloadEvent(path string, offset int64) (brokerReload, bool) {
	f, err := os.Open(path) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return brokerReload{}, false
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, 0); err != nil {
		return brokerReload{}, false
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev brokerReload
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Event == "reload" {
			return ev, true
		}
	}
	return brokerReload{}, false
}

// reloadGuest asks the guest init to apply the CA bundle, SSH agent and snapshots of g
// and waits for its answer.
func reloadGuest(stateDir string, g guest.Config) error {
	req := guest.Reload{Seq: time.Now().UnixNano(), CABundle: g.CABundle, SSHAgent: g.SSHAgent, Snapshots: g.Snapshots}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode reload request: %w", err)
	}
	if err := vm.WriteControl(stateDir, guest.ReloadFile, data); err != nil {
		return fmt.Errorf("write reload request: %w", err)
	}
	deadline := time.Now().Add(reloadTimeout)
	for time.Now().Before(deadline) {
		var done guest.ReloadDone
		if b, err := vm.ReadControl(stateDir, guest.ReloadDoneFile); err == nil && json.Unmarshal(b, &done) == nil && done.Seq == req.Seq {
			if done.Error != "" {
				return fmt.Errorf("guest: %s", done.Error)
			}
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the guest did not confirm the reload within %s; it may run a jailoc-guest from another jailoc version (`jailoc restart` updates it)", reloadTimeout)
}
