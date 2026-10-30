//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

const (
	builderCPUs      = 4
	builderMemoryMiB = 4096
)

// builderStateDir is the builder of a workspace: shared by all workspaces,
// or the workspace's own with isolated_build.
func builderStateDir(ws *workspace.Resolved) string {
	if ws.IsolatedBuild {
		return filepath.Join(vmStateDir(ws.Name), "builder")
	}
	return filepath.Join(vmRootDir(), "builder")
}

// runUpVM builds the workspace image in a builder VM and boots it as the
// workspace VM.
func runUpVM(ctx context.Context, cfg *config.Config, ws *workspace.Resolved) error {
	upStart := time.Now()
	stateDir := vmStateDir(ws.Name)
	if st, err := runningVM(stateDir); err != nil {
		return err
	} else if st != nil {
		return reloadVM(ctx, ws)
	}

	// Must run before every generated-file write below.
	if err := validateSecretSources(ws); err != nil {
		return fmt.Errorf("validate secrets for workspace %q: %w", ws.Name, err)
	}
	if err := materializeCABundle(ws); err != nil {
		return fmt.Errorf("prepare CA bundle for workspace %q: %w", ws.Name, err)
	}
	cacheDir := workspaceCacheDir(ws.Name)
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return fmt.Errorf("create cache directory %q: %w", cacheDir, err)
	}
	if err := writeAgentFiles(ws, cacheDir); err != nil {
		return err
	}
	shares, err := vmShares(ws)
	if err != nil {
		return err
	}
	if err := claimProfile(ws); err != nil {
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	internal, err := vmGuestData(ws, cacheDir, shares, true)
	if err != nil {
		return err
	}
	snapshots, err := vmSnapshots(ws)
	if err != nil {
		return err
	}
	if err := checkSnapshots(ws, snapshots, shares); err != nil {
		return err
	}

	start := time.Now()
	rt, err := vmRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	logStep("prepare VM runtime", start)
	start = time.Now()
	ref, err := buildWorkspaceImage(ctx, cfg, ws, rt)
	if err != nil {
		return err
	}
	logStep("workspace image", start, "ref", ref)

	binds, err := stageSnapshots(stateDir, snapshots, "")
	if err != nil {
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	spec, err := workspaceSpec(ctx, ws, rt, ref, shares, binds)
	if err != nil {
		return err
	}
	spec.Guest.Binds = internal
	if err := configureVMDocker(ws, &spec); err != nil {
		return err
	}
	if ws.ProtectGit {
		pins, readOnly, err := gitProtection(ws.Paths)
		if err != nil {
			return fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
		spec.Guest.Pins = pins
		spec.Guest.ReadOnly = readOnly
		spec.Guest.NoPrivilegeEscalation = true
		repos := 0
		for _, p := range readOnly {
			if filepath.Base(p) == "hooks" {
				repos++
			}
		}
		_, _ = color.New(color.FgCyan).Printf("Protecting Git hooks and config of %d repositories; sudo and Docker are disabled\n", repos)
	}
	payload, err := secretsPayload(ctx, ws, spec)
	if err != nil {
		return err
	}
	// A marker left by an earlier VM would hide an interrupted delivery.
	if err := os.Remove(secretsMarker(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reset secrets marker: %w", err)
	}
	start = time.Now()
	if _, err := startSupervisor(ctx, spec, "VM for workspace "+ws.Name); err != nil {
		return fmt.Errorf("start vm for workspace %q: %w", ws.Name, err)
	}
	logStep("start workspace VM", start)
	start = time.Now()
	if err := deliverSecrets(ctx, stateDir, payload); err != nil {
		_ = stopSupervisor(stateDir)
		return fmt.Errorf("deliver secrets to workspace %q: %w", ws.Name, err)
	}
	logStep("deliver secrets", start)
	logStep("up", upStart, "workspace", ws.Name)
	_, _ = color.New(color.FgGreen).Printf("Workspace %s started\n", ws.Name)
	return nil
}

// buildWorkspaceImage runs the image cascade in a builder VM and unpacks the
// result into the image cache. It returns the image-cache reference.
func buildWorkspaceImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, rt vmRuntimeFiles) (string, error) {
	var hostErr error
	if !ws.IsolatedBuild && cfg.VM.Builder != config.BuilderVM {
		hostErr = docker.HostArchitecture(ctx)
	}
	host, err := useHostDocker(cfg.VM.Builder, ws.IsolatedBuild, hostErr)
	if err != nil {
		return "", err
	}
	if host {
		_, _ = color.New(color.FgCyan).Printf("Building image for workspace %s with the host Docker daemon...\n", ws.Name)
		return importWorkspaceImage(ctx, cfg, ws)
	}
	if hostErr != nil {
		slog.Debug("host Docker not used for the image build", "reason", hostErr)
	}

	dir := builderStateDir(ws)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create builder state dir: %w", err)
	}
	lock, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return "", fmt.Errorf("open builder lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: file descriptors fit in int
		return "", fmt.Errorf("lock builder: %w", err)
	}

	policy := []byte(`{}`)
	label := "shared image builder VM"
	if ws.IsolatedBuild {
		if policy, err = vmPolicy(ctx, ws); err != nil {
			return "", err
		}
		label = "image builder VM for workspace " + ws.Name
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate jailoc executable: %w", err)
	}
	spec := vm.Spec{
		Kind:       vm.KindBuilder,
		Workspace:  "builder",
		StateDir:   dir,
		Image:      vm.DefaultImage,
		CPUs:       builderCPUs,
		MemoryMiB:  builderMemoryMiB,
		DiskSize:   vm.DefaultDiskSize,
		Policy:     policy,
		RunnerPath: rt.runner,
		LibDir:     rt.libDir,
		ImageCache: filepath.Join(vmRootDir(), "images"),
		Broker:     []string{exe, "netbroker"},
		// The builder restarts with each workspace's bundle, so pulls behind
		// a TLS-intercepting proxy work in the shared builder too.
		CABundle: workspaceCABundle(ws),
	}
	if ws.IsolatedBuild {
		spec.Workspace = ws.Name + "-builder"
	}
	start := time.Now()
	st, err := startSupervisor(ctx, spec, label)
	if err != nil {
		return "", fmt.Errorf("start image builder: %w", err)
	}
	logStep("start builder VM", start)
	defer func() {
		if err := stopSupervisor(dir); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: stop image builder: %v\n", err)
		}
	}()
	if err := setDockerHost(st.DockerSocket); err != nil {
		return "", err
	}
	return importWorkspaceImage(ctx, cfg, ws)
}

// useHostDocker reports whether the workspace image is built with the host
// Docker daemon. isolated_build always uses its own builder VM; "auto" uses
// the host daemon when hostErr is nil.
func useHostDocker(builder string, isolated bool, hostErr error) (bool, error) {
	switch {
	case isolated || builder == config.BuilderVM:
		return false, nil
	case builder == config.BuilderDocker && hostErr != nil:
		return false, fmt.Errorf(`[vm] builder = "docker": %w`, hostErr)
	default:
		return hostErr == nil, nil
	}
}

// importWorkspaceImage resolves and builds the workspace image with the
// Docker daemon selected by DOCKER_HOST and unpacks it into the VM image
// cache.
func importWorkspaceImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved) (string, error) {
	lock, err := lockDockerImages()
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close() }()
	_, _ = color.New(color.FgCyan).Printf("Resolving image for workspace %s...\n", ws.Name)
	finalImage, chain, err := ResolveAndLayerImage(ctx, cfg, ws, appVersion)
	if err != nil {
		return "", fmt.Errorf("resolve image for workspace %q: %w", ws.Name, err)
	}
	start := time.Now()
	if err := docker.EnsureImage(ctx, finalImage); err != nil {
		return "", err
	}
	logStep("ensure image", start, "image", finalImage)
	id, err := docker.ImageID(ctx, finalImage)
	if err != nil {
		return "", err
	}
	ref := vm.RootFSRef(id)
	start = time.Now()
	err = vm.ImportImage(ctx, filepath.Join(vmRootDir(), "images"), ref, func(w io.Writer) error {
		_, _ = color.New(color.FgCyan).Printf("Unpacking image %s for the workspace VM...\n", finalImage)
		_, err := docker.SaveImage(ctx, finalImage, w)
		return err
	})
	if err != nil {
		return "", err
	}
	logStep("import image into VM cache", start, "ref", ref)
	start = time.Now()
	pruneImageCache(ref)
	pruneDockerImages(ctx, lock, ws.Name, chain)
	logStep("prune images", start)
	return ref, nil
}

// lockDockerImages returns the host-wide lock on the Docker images jailoc
// builds, held shared: every `jailoc up` holds it while it builds, exports
// and records its images, and pruning needs it exclusively.
func lockDockerImages() (*os.File, error) {
	if err := os.MkdirAll(vmRootDir(), 0o750); err != nil {
		return nil, fmt.Errorf("create VM state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(vmRootDir(), "docker-images.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("open Docker image lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil { //nolint:gosec // G115: file descriptors fit in int
		_ = f.Close()
		return nil, fmt.Errorf("lock Docker images: %w", err)
	}
	return f, nil
}

// dockerImagesFile records, per workspace, the IDs of the Docker images its
// last image build used.
const dockerImagesFile = "docker-images.json"

// pruneDockerImages records the images in chain as used by workspace and
// removes the images jailoc built that no workspace records, from the
// daemon that built them. lock is the shared lockDockerImages lock; pruning
// is skipped while another `jailoc up` holds it, since its images are not
// recorded yet, and left to the last one to finish. Failures only warn:
// removal frees disk space and a removed image is rebuilt on demand.
func pruneDockerImages(ctx context.Context, lock *os.File, workspace string, chain []string) {
	warn := func(err error) {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: prune Docker images: %v\n", err)
	}
	ids := make([]string, 0, len(chain))
	for _, ref := range chain {
		id, err := docker.ImageID(ctx, ref)
		if err != nil {
			warn(err)
			return
		}
		ids = append(ids, id)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		warn(fmt.Errorf("encode image IDs: %w", err))
		return
	}
	if err := os.MkdirAll(vmStateDir(workspace), 0o700); err != nil {
		warn(fmt.Errorf("create workspace state dir: %w", err))
		return
	}
	if err := os.WriteFile(filepath.Join(vmStateDir(workspace), dockerImagesFile), data, 0o600); err != nil {
		warn(fmt.Errorf("record workspace images: %w", err))
		return
	}

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: file descriptors fit in int
		slog.Debug("Docker image pruning skipped", "reason", err)
		return
	}

	keep := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(vmRootDir(), "workspaces", "*", dockerImagesFile))
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // G304: jailoc-owned state path
		if err != nil {
			warn(fmt.Errorf("read %s: %w", path, err))
			return
		}
		var recorded []string
		if err := json.Unmarshal(data, &recorded); err != nil {
			warn(fmt.Errorf("parse %s: %w", path, err))
			return
		}
		for _, id := range recorded {
			keep[id] = true
		}
	}
	// The grace period spares images built outside jailoc's lock, e.g. by
	// an older jailoc.
	removed, err := docker.PruneImages(ctx, keep, time.Hour)
	if err != nil {
		warn(err)
	}
	if removed > 0 {
		_, _ = color.New(color.FgCyan).Printf("Removed %d unused jailoc image(s) from Docker\n", removed)
	}
}

// pruneImageCache removes unpacked workspace images that no workspace or
// builder spec uses any more. Failures only warn: the cache is an
// optimisation.
func pruneImageCache(current string) {
	keep := []string{current}
	specs, _ := filepath.Glob(filepath.Join(vmRootDir(), "workspaces", "*", vm.SpecFile("")))
	more, _ := filepath.Glob(filepath.Join(vmRootDir(), "workspaces", "*", "builder", vm.SpecFile("")))
	specs = append(append(specs, more...), filepath.Join(vmRootDir(), "builder", vm.SpecFile("")))
	for _, path := range specs {
		if spec, err := vm.ReadSpec(filepath.Dir(path)); err == nil {
			keep = append(keep, spec.Image)
		}
	}
	res, err := vm.PruneImages(filepath.Join(vmRootDir(), "images"), keep)
	if err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: prune VM image cache: %v\n", err)
		return
	}
	if res.Images > 0 || res.Layers > 0 {
		_, _ = color.New(color.FgCyan).Printf("Removed %d unused workspace image(s) and %d unused layer(s) from the VM image cache\n", res.Images, res.Layers)
	}
}

// workspaceSpec describes the workspace VM booting ref.
func workspaceSpec(ctx context.Context, ws *workspace.Resolved, rt vmRuntimeFiles, ref string, shares []vm.Share, snapshots []guest.Snapshot) (vm.Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return vm.Spec{}, fmt.Errorf("locate jailoc executable: %w", err)
	}
	memMiB, err := vmMemoryMiB(ws.Memory)
	if err != nil {
		return vm.Spec{}, err
	}
	policy, err := vmPolicy(ctx, ws)
	if err != nil {
		return vm.Spec{}, err
	}
	workDir := "/home/agent"
	if len(ws.Paths) > 0 {
		workDir = ws.Paths[0]
	}
	g := &guest.Config{
		Hostname:  ws.Name,
		User:      "agent",
		UID:       defaultAgentID,
		GID:       defaultAgentID,
		IDMap:     hostIDMap(),
		Home:      "/home/agent",
		WorkDir:   workDir,
		Snapshots: snapshots,
		Env:       append([]string{"JAILOC=1", "JAILOC_WORKSPACE=" + ws.Name}, ws.Agent.Env...),
	}
	if ws.Agent.TUIConfigEnv != "" {
		g.Env = append(g.Env, ws.Agent.TUIConfigEnv+"="+filepath.Join(guestCacheDir, "tui-container.json"))
	}
	if _, err := os.Stat(filepath.Join(config.ConfigDir(), "workspaces", ws.Name, caBundleFilename)); err == nil {
		g.CABundle = filepath.Join(guestConfigDir, caBundleFilename)
	}
	agent := vmSSHAgent(ws)
	g.SSHAgent = agent != ""
	spec := vm.Spec{
		Kind:        vm.KindWorkspace,
		Workspace:   ws.Name,
		StateDir:    vmStateDir(ws.Name),
		Image:       ref,
		CPUs:        uint32(math.Ceil(ws.CPU)),
		MemoryMiB:   memMiB,
		Shares:      shares,
		Policy:      policy,
		RunnerPath:  rt.runner,
		LibDir:      rt.libDir,
		ImageCache:  filepath.Join(vmRootDir(), "images"),
		Broker:      []string{exe, "netbroker"},
		GuestBinary: rt.guest,
		Agent:       ws.Agent.Name,
		Guest:       g,
		Forwards:    vmForwards(ws.Ports),
		SSHAgent:    agent,
	}
	if ws.AgentProfile != nil {
		spec.Profile, _ = ws.AgentProfile.Name()
		spec.EphemeralProfile = ws.AgentProfile.Ephemeral()
	}
	return spec, nil
}

// vmSSHAgent returns the host SSH agent socket to relay into the workspace
// VM, or "" when ssh_auth_sock is off or no agent is running.
func vmSSHAgent(ws *workspace.Resolved) string {
	if !ws.SSHAuthSock {
		return ""
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if info, err := os.Stat(sock); sock == "" || err != nil || info.Mode()&os.ModeSocket == 0 { //nolint:gosec // G703: the user's own SSH_AUTH_SOCK
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: ssh_auth_sock is enabled but SSH_AUTH_SOCK does not name a running agent; the workspace starts without one\n")
		return ""
	}
	return sock
}

// workspaceCABundle is the materialized CA bundle of the workspace, or "".
func workspaceCABundle(ws *workspace.Resolved) string {
	path := filepath.Join(config.ConfigDir(), "workspaces", ws.Name, caBundleFilename)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// secretsPayload reads the workspace environment and secret values on the
// host. Env secrets go to the session environment only, file secrets to
// /run/secrets. Values stay in memory.
func secretsPayload(ctx context.Context, ws *workspace.Resolved, spec vm.Spec) (guest.Secrets, error) {
	payload := guest.Secrets{Env: append([]string{}, ws.Env...), Files: map[string][]byte{}}
	if spec.SSHAgent != "" {
		payload.Env = append(payload.Env, "SSH_AUTH_SOCK="+guest.SSHAgentSocket)
	}
	if spec.Guest != nil && spec.Guest.CABundle != "" {
		payload.Env = append(payload.Env, "NODE_USE_SYSTEM_CA=1")
	}
	for _, s := range ws.Secrets {
		value, err := readSecret(ctx, s)
		if err != nil {
			return guest.Secrets{}, err
		}
		if s.Kind == workspace.SecretKindEnv {
			payload.Env = append(payload.Env, s.Name+"="+string(value))
		} else {
			payload.Files[s.Name] = value
		}
	}
	return payload, nil
}

// secretsMarker exists while the running workspace VM holds the secrets of
// its latest delivery.
func secretsMarker(stateDir string) string { return filepath.Join(stateDir, "secrets-delivered") }

// deliverSecrets sends payload to the guest over SSH, which replaces the
// previous environment and secrets, and records the delivery.
func deliverSecrets(ctx context.Context, stateDir string, payload guest.Secrets) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	client, err := vm.DialSSH(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	sess.Stdin = strings.NewReader(string(data))
	var stderr strings.Builder
	sess.Stderr = &stderr
	if err := sess.Run(guest.BinaryPath + " secrets"); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.WriteFile(secretsMarker(stateDir), nil, 0o600); err != nil {
		return fmt.Errorf("record secrets delivery: %w", err)
	}
	return nil
}

// ensureSecrets delivers the secrets to a running workspace VM that never
// received them, for example when jailoc was interrupted between booting the
// VM and delivering them.
func ensureSecrets(ctx context.Context, ws *workspace.Resolved) error {
	stateDir := vmStateDir(ws.Name)
	if _, err := os.Stat(secretsMarker(stateDir)); err == nil {
		return nil
	}
	if err := validateSecretSources(ws); err != nil {
		return fmt.Errorf("validate secrets for workspace %q: %w", ws.Name, err)
	}
	spec, err := vm.ReadSpec(stateDir)
	if err != nil {
		return err
	}
	payload, err := secretsPayload(ctx, ws, *spec)
	if err != nil {
		return err
	}
	if err := deliverSecrets(ctx, stateDir, payload); err != nil {
		return fmt.Errorf("deliver secrets to workspace %q: %w", ws.Name, err)
	}
	return nil
}

// shellJoin quotes args for the guest's /bin/sh -c.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// downVM stops the workspace VM; purge also deletes its state, persistent
// home directories and isolated builder.
func downVM(ws *workspace.Resolved, purge bool) error {
	stateDir := vmStateDir(ws.Name)
	st, err := runningVM(stateDir)
	if err != nil {
		return err
	}
	if st == nil && !purge {
		_, _ = color.New(color.FgYellow).Printf("Workspace %s is not running\n", ws.Name)
		return nil
	}
	if st != nil {
		_, _ = color.New(color.FgCyan).Printf("Stopping workspace %s...\n", ws.Name)
	}
	if err := stopSupervisor(stateDir); err != nil {
		return err
	}
	if spec, err := vm.ReadSpec(stateDir); err == nil && spec.Profile != "" {
		if err := releaseProfile(ws.Name, spec.Profile); err != nil {
			return fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
	}
	if err := stopSupervisor(filepath.Join(stateDir, "builder")); err != nil {
		return err
	}
	if purge {
		if err := os.RemoveAll(stateDir); err != nil {
			return fmt.Errorf("delete vm state: %w", err)
		}
		_, _ = color.New(color.FgGreen).Printf("Workspace %s VM deleted\n", ws.Name)
		return nil
	}
	_, _ = color.New(color.FgGreen).Printf("Workspace %s stopped\n", ws.Name)
	return nil
}

// statusVM prints whether the workspace VM runs.
func statusVM(ws *workspace.Resolved) error {
	st, err := runningVM(vmStateDir(ws.Name))
	if err != nil {
		return err
	}
	fmt.Printf("Workspace: %s\n", ws.Name)
	fmt.Printf("Backend:   vm\n")
	if st == nil {
		fmt.Printf("State:     stopped\n")
		return nil
	}
	fmt.Printf("State:     running (supervisor pid %d)\n", st.PID)
	fmt.Printf("Started:   %s\n", st.Updated.Local().Format(time.RFC3339))
	if spec, err := vm.ReadSpec(vmStateDir(ws.Name)); err == nil {
		fmt.Printf("Profile:   %s\n", profileLabel(*spec))
	}
	return nil
}

// profileLabel describes where the agent of a workspace VM keeps its state.
func profileLabel(spec vm.Spec) string {
	switch {
	case spec.Profile != "":
		return spec.Profile
	case spec.EphemeralProfile:
		return "none (agent state is emptied on every start)"
	default:
		return "workspace"
	}
}

// logsVM prints the supervisor and guest console logs; follow keeps
// printing the console log as it grows.
func logsVM(ctx context.Context, ws *workspace.Resolved, follow bool, w io.Writer) error {
	stateDir := vmStateDir(ws.Name)
	console := filepath.Join(stateDir, "microvm", "console.log")
	if _, err := os.Stat(console); err != nil {
		return fmt.Errorf("workspace %q has no VM logs; start it with 'jailoc up'", ws.Name)
	}
	if data, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil { //nolint:gosec // G304: jailoc-owned state path
		_, _ = w.Write(data)
	}
	f, err := os.Open(console) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return fmt.Errorf("open console log: %w", err)
	}
	defer func() { _ = f.Close() }()
	for {
		if _, err := io.Copy(w, f); err != nil {
			return fmt.Errorf("read console log: %w", err)
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// restartVMIfRunning restarts a running workspace VM so configuration
// changes (e.g. new paths) take effect.
func restartVMIfRunning(ctx context.Context, ws *workspace.Resolved) error {
	if st, err := runningVM(vmStateDir(ws.Name)); err != nil || st == nil {
		return err
	}
	if err := downVM(ws, false); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload config for restart: %w", err)
	}
	ws2, err := workspace.Resolve(cfg, ws.Name)
	if err != nil {
		return fmt.Errorf("re-resolve workspace for restart: %w", err)
	}
	return runUpVM(ctx, cfg, ws2)
}

// dockerdPaths are where images install dockerd.
var dockerdPaths = []string{"usr/local/bin/dockerd", "usr/bin/dockerd", "usr/sbin/dockerd", "bin/dockerd"}

// configureVMDocker enables the Docker daemon of the workspace VM when
// enable_docker is on; the image must ship dockerd.
func configureVMDocker(ws *workspace.Resolved, spec *vm.Spec) error {
	if !ws.EnableDocker {
		return nil
	}
	has, err := vm.ImageHasFile(spec.ImageCache, spec.Image, dockerdPaths...)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	if !has {
		return fmt.Errorf("workspace %q: enable_docker is on, but the workspace image has no dockerd; install Docker in the image or set enable_docker = false", ws.Name)
	}
	spec.Docker = true
	spec.Guest.Docker = &guest.Docker{Disk: guest.DockerDisk}
	return nil
}

func vmForwards(ports []config.PortForward) []vm.Forward {
	out := make([]vm.Forward, 0, len(ports))
	for _, p := range ports {
		out = append(out, vm.Forward{HostPort: p.Host, GuestPort: p.Guest})
	}
	return out
}

// defaultAgentID is the agent's UID and GID inside workspace VMs.
const defaultAgentID = 1000

// hostIDMap returns the host user to map inside the workspace VM. On Linux,
// libkrun's virtio-fs shows the real host owner of every shared file and of
// the VM root filesystem, so the guest mounts them idmapped: the host user
// appears as root in the root filesystem and as the agent in shared
// directories. macOS presents files as owned by whoever accesses them and
// needs no mapping; a root host needs none either.
func hostIDMap() *guest.IDMap {
	if runtime.GOOS != "linux" || os.Getuid() == 0 || os.Getgid() == 0 {
		return nil
	}
	return &guest.IDMap{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} //nolint:gosec // G115: Unix IDs are non-negative and fit in uint32
}
