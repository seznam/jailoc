//go:build darwin || linux

package cmd

import (
	"cmp"
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatih/color"
	"golang.org/x/term"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/logging"
	"github.com/seznam/jailoc/internal/netbroker"
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
func runUpVM(ctx context.Context, cfg *config.Config, ws *workspace.Resolved) (err error) {
	upStart := time.Now()
	stateDir := vmStateDir(ws.Name)
	if st, err := runningVM(stateDir); err != nil {
		return err
	} else if st != nil {
		if err := reloadVM(ctx, ws, false); err != nil {
			return err
		}
		autoManageProxyOnUp(ctx, ws, os.Stdout, os.Stderr)
		return nil
	}
	stopUpLog, err := teeUpLog(stateDir)
	if err != nil {
		return err
	}
	defer func() {
		// The error reaches only the terminal otherwise.
		if err != nil {
			slog.Error("up failed", "workspace", ws.Name, "error", err)
		}
		stopUpLog()
	}()

	// Must run before every generated-file write below.
	if err := validateSecretSources(ws); err != nil {
		return fmt.Errorf("validate secrets for workspace %q: %w", ws.Name, err)
	}
	if err := materializeCABundle(ws); err != nil {
		return fmt.Errorf("prepare CA bundle for workspace %q: %w", ws.Name, err)
	}
	// Snapshotted once: image builds, the builder VM and the recorded
	// spec all see the same bundle.
	buildCA, err := workspaceBuildCA(ws)
	if err != nil {
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
	checkAgentStateVersion(cfg, ws, shares)
	snapshots, err := vmSnapshots(ws, shares)
	if err != nil {
		return err
	}
	if err := checkSnapshots(ws, snapshots, shares); err != nil {
		return err
	}
	warnDanglingShareLinks(ws, shares, snapshots)

	start := time.Now()
	rt, err := vmRuntime(cfg)
	if err != nil {
		return err
	}
	pruneRuntimeCache(rt)
	logStep("prepare VM runtime", start)
	start = time.Now()
	ref, err := buildWorkspaceImage(ctx, cfg, ws, rt, buildCA)
	if err != nil {
		return err
	}
	logStep("workspace image", start, "ref", ref)

	binds, err := stageSnapshots(stateDir, snapshots, "")
	if err != nil {
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	spec, err := workspaceSpec(ctx, ws, rt, ref, shares, binds, buildCA)
	if err != nil {
		return err
	}
	if buildsImage(cfg, ws) {
		spec.BuildCA = buildCAState(buildCA)
	} else {
		// jailoc built nothing, so a changed bundle changes nothing.
		spec.BuildCA = ""
	}
	spec.Guest.Binds = internal
	if err := configureVMDocker(ws, &spec); err != nil {
		return err
	}
	if ws.ProtectGit {
		pins, readOnly, err := gitProtection(shares, gitInheritedConfig())
		if err != nil {
			return fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
		spec.Guest.Pins = pins
		spec.Guest.ReadOnly = readOnly
		spec.Guest.NoPrivilegeEscalation = true
		_, _ = color.New(color.FgCyan).Println("Protecting Git hooks and config; sudo and Docker are disabled")
	}
	payload, err := secretsPayload(ctx, ws, spec, false)
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
	if err := archiveLegacyHome(stateDir, ws.Name); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: workspace %q: %v\n", ws.Name, err)
	}
	start = time.Now()
	if err := deliverSecrets(ctx, stateDir, payload); err != nil {
		_ = stopSupervisor(stateDir)
		return fmt.Errorf("deliver secrets to workspace %q: %w", ws.Name, err)
	}
	logStep("deliver secrets", start)
	recordAgentStateVersion(cfg, ws, shares)
	logStep("up", upStart, "workspace", ws.Name)
	_, _ = color.New(color.FgGreen).Printf("Workspace %s started\n", ws.Name)
	autoManageProxyOnUp(ctx, ws, os.Stdout, os.Stderr)
	return nil
}

// buildWorkspaceImage runs the image cascade in a builder VM and converts the
// result into the image cache. It returns the image-cache reference.
// With ca, builds run in the managed builder VM, which trusts it.
func buildWorkspaceImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, rt vmRuntimeFiles, ca *docker.BuildCA) (string, error) {
	var hostErr error
	if !ws.IsolatedBuild && cfg.VM.Builder != config.BuilderVM {
		hostErr = docker.HostArchitecture(ctx)
	}
	host, err := useHostDocker(cfg.VM.Builder, ws.IsolatedBuild, ca != nil, hostErr)
	if err != nil {
		return "", err
	}
	if host {
		_, _ = color.New(color.FgCyan).Printf("Building image for workspace %s with the host Docker daemon...\n", ws.Name)
		return importWorkspaceImage(ctx, cfg, ws, ca)
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
		if policy, err = vmBuilderPolicy(ctx, ws); err != nil {
			return "", err
		}
		label = "image builder VM for workspace " + ws.Name
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate jailoc executable: %w", err)
	}
	diskSize, err := builderDiskSize(cfg, ws)
	if err != nil {
		return "", err
	}
	caPath, err := writeBuilderCABundle(dir, ca)
	if err != nil {
		return "", err
	}
	spec := vm.Spec{
		Kind:       vm.KindBuilder,
		Workspace:  "builder",
		StateDir:   dir,
		Image:      vm.DefaultImage,
		CPUs:       builderCPUs,
		MemoryMiB:  builderMemoryMiB,
		DiskSize:   diskSize,
		Policy:     policy,
		RunnerPath: rt.runner,
		LibDir:     rt.libDir,
		ImageCache: filepath.Join(vmRootDir(), "images"),
		Broker:     []string{exe, "netbroker"},
		// The builder restarts with each workspace's external bundle, so
		// pulls behind a TLS-intercepting proxy work in the shared builder
		// too. The digest restarts it when the bundle changes at the same path.
		CABundle:       caPath,
		CABundleDigest: ca.Digest(),
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
	restoreHost, err := setDockerHost(st.DockerSocket)
	if err != nil {
		return "", err
	}
	defer restoreHost()
	// The builder lock is held until return, so the maintenance of the
	// builder's own daemon is serialized with every other build in this
	// HOME. The host daemon is never maintained: other tools and HOMEs share it.
	return withBuilderMaintenance(ctx, func(ctx context.Context, phase string) {
		maintainBuilderDaemon(ctx, spec.DiskSize, phase, os.Stdout, os.Stderr)
	}, func() (string, error) {
		return importWorkspaceImage(ctx, cfg, ws, ca)
	})
}

// useHostDocker reports whether the workspace image is built with the host
// Docker daemon. isolated_build always uses its own builder VM; "auto" uses
// the host daemon when hostErr is nil. With ca_bundle forwarding the build
// runs in the managed builder VM, and builder = "docker" is rejected because
// jailoc never modifies the host Docker daemon.
func useHostDocker(builder string, isolated, forwardCA bool, hostErr error) (bool, error) {
	switch {
	case forwardCA && builder == config.BuilderDocker:
		return false, fmt.Errorf("[vm] builder = \"docker\" cannot be used when ca_bundle forwarding is active: jailoc forwards ca_bundle into its builder VM and never modifies the host Docker daemon; set [vm] builder = \"auto\" or \"vm\", or set ca_bundle = false")
	case isolated || builder == config.BuilderVM:
		return false, nil
	case forwardCA:
		return false, nil
	case builder == config.BuilderDocker && hostErr != nil:
		return false, fmt.Errorf(`[vm] builder = "docker": %w`, hostErr)
	default:
		return hostErr == nil, nil
	}
}

// writeBuilderCABundle writes ca into dir for the builder VM and returns its
// path. With a nil ca it removes any bundle a previous workspace left in the
// shared builder directory and returns an empty path.
func writeBuilderCABundle(dir string, ca *docker.BuildCA) (string, error) {
	destination := filepath.Join(dir, caBundleFilename)
	if ca == nil {
		return "", removeCABundle(destination)
	}
	if err := writeCABundleAtomically(destination, ca.PEM()); err != nil {
		return "", fmt.Errorf("write builder CA bundle: %w", err)
	}
	return destination, nil
}

// importWorkspaceImage resolves and builds the workspace image with the
// Docker daemon selected by DOCKER_HOST and converts it into the VM image
// cache.
// The optional ca is the external CA bundle the builds and pulls trust.
func importWorkspaceImage(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, cas ...*docker.BuildCA) (string, error) {
	var ca *docker.BuildCA
	if len(cas) > 0 {
		ca = cas[0]
	}
	_, _ = color.New(color.FgCyan).Printf("Resolving image for workspace %s...\n", ws.Name)
	finalImage, chain, err := ResolveAndLayerImage(ctx, cfg, ws, appVersion, ca)
	if err != nil {
		return "", fmt.Errorf("resolve image for workspace %q: %w", ws.Name, err)
	}
	start := time.Now()
	if err := docker.EnsureBuildImage(ctx, finalImage, ca); err != nil {
		return "", err
	}
	logStep("ensure image", start, "image", finalImage)
	id, err := docker.ImageID(ctx, finalImage)
	if err != nil {
		return "", err
	}
	ref := vm.RootFSRef(id)
	start = time.Now()
	var phases importPhases
	progressDisplay := newImportProgressDisplay(
		os.Stderr,
		term.IsTerminal(int(os.Stderr.Fd())), //nolint:gosec // G115: file descriptors fit in int
		func() int {
			width, _, err := term.GetSize(int(os.Stderr.Fd())) //nolint:gosec // G115: file descriptors fit in int
			if err != nil {
				return 0
			}
			return width
		},
	)
	err = vm.ImportImage(ctx, filepath.Join(vmRootDir(), "images"), ref, func(w io.Writer) error {
		warnLargeImage(ctx, finalImage, dockerfileBase(chain))
		_, _ = color.New(color.FgCyan).Printf("Converting image %s for the workspace VM...\n", finalImage)
		// Not earlier: ImportImage waits for other imports before it calls save.
		phases.start = time.Now()
		progressDisplay.start()
		_, err := docker.SaveImage(ctx, finalImage, w)
		return err
	}, func(p vm.ImportProgress) {
		phases.update(p, time.Now())
		progressDisplay.update(p)
	})
	progressDisplay.stop()
	if err != nil {
		return "", err
	}
	phases.log(time.Now())
	logStep("import image into VM cache", start, "ref", ref, "cached", phases.converting.IsZero())
	start = time.Now()
	pruneImageCache(ref)
	logStep("prune images", start)
	return ref, nil
}

// Sizes of what the user adds to an image above which jailoc warns before
// converting it: the conversion reads every new layer, and each layer is one
// more mount that file lookups in the VM pass through.
const (
	largeImageLayers = 20
	largeImageBytes  = 5 << 30
)

// warnLargeImage logs the size of image and warns on stderr when the user's
// part of it is large: the layers the workspace Dockerfile adds to parent,
// or all of image when it is used as is (parent is empty). The base image and
// the layers jailoc adds do not count.
func warnLargeImage(ctx context.Context, image, parent string) {
	layers, size, err := docker.ImageSize(ctx, image)
	if err != nil {
		slog.Debug("image size unknown", "image", image, "error", err)
		return
	}
	slog.Info("workspace image", "image", image, "layers", layers, "bytes", size)
	subject := "image " + image + " has"
	if parent != "" {
		if parent == image {
			// No workspace Dockerfile.
			return
		}
		baseLayers, baseSize, err := docker.ImageSize(ctx, parent)
		if err != nil {
			slog.Debug("image size unknown", "image", parent, "error", err)
			return
		}
		layers, size = layers-baseLayers, size-baseSize
		subject = "the workspace Dockerfile adds"
		slog.Info("workspace Dockerfile layers", "image", image, "base", parent, "layers", layers, "bytes", size)
	}
	if msg := largeImageWarning(subject, layers, size); msg != "" {
		slog.Warn("large workspace image", "image", image, "layers", layers, "bytes", size)
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: %s\n", msg)
	}
}

// dockerfileBase returns the image the workspace Dockerfile was built on,
// from the chain ResolveAndLayerImage returns: the image before the final
// one, which is the final one itself without a workspace Dockerfile. An image
// used as is has none.
func dockerfileBase(chain []string) string {
	if len(chain) < 2 {
		return ""
	}
	return chain[len(chain)-2]
}

// largeImageWarning returns the warning that subject, such as "image x has",
// that many layers and bytes, or "" when they are not large.
func largeImageWarning(subject string, layers int, size int64) string {
	var what []string
	if layers > largeImageLayers {
		what = append(what, fmt.Sprintf("%d layers", layers))
	}
	if size > largeImageBytes {
		what = append(what, formatBytes(size))
	}
	if len(what) == 0 {
		return ""
	}
	return fmt.Sprintf("%s %s; converting it for the VM takes a while, and each layer slows down file lookups in the VM. "+
		"Fewer RUN steps or a multi-stage build make it faster to convert and boot", subject, strings.Join(what, " and "))
}

// importPhases times the export from Docker and the layer conversion of an
// image import from its progress reports.
type importPhases struct {
	start      time.Time
	converting time.Time // first conversion report; zero while exporting
	bytes      int64
	layers     int
}

func (p *importPhases) update(progress vm.ImportProgress, now time.Time) {
	switch progress.Phase {
	case vm.PhaseSave:
		p.bytes = progress.Bytes
	case vm.PhaseExtract:
		if p.converting.IsZero() {
			p.converting = now
		}
		p.layers = progress.Total
	}
}

// log logs the phases of an import that finished at now; an image already
// in the cache has none.
func (p *importPhases) log(now time.Time) {
	if p.converting.IsZero() {
		return
	}
	logDuration("export image from Docker", p.converting.Sub(p.start), "bytes", p.bytes)
	logDuration("convert image layers", now.Sub(p.converting), "new_layers", p.layers)
}

type importProgressDisplay struct {
	w           io.Writer
	interactive bool
	width       func() int
	now         func() time.Time

	mu       sync.Mutex
	progress vm.ImportProgress
	started  time.Time
	running  bool
	frame    int
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newImportProgressDisplay(w io.Writer, interactive bool, width func() int) *importProgressDisplay {
	return &importProgressDisplay{
		w:           w,
		interactive: interactive,
		width:       width,
		now:         time.Now,
		started:     time.Now(),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

func (d *importProgressDisplay) start() {
	if !d.interactive {
		return
	}
	d.mu.Lock()
	d.started = d.now()
	d.running = true
	d.mu.Unlock()
	go d.animate()
}

func (d *importProgressDisplay) animate() {
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()
	defer close(d.doneCh)
	for {
		select {
		case <-ticker.C:
			d.redraw()
		case <-d.stopCh:
			return
		}
	}
}

func (d *importProgressDisplay) update(progress vm.ImportProgress) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.progress = progress
}

func (d *importProgressDisplay) redraw() {
	if !d.interactive {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	frame := spinnerFrames[d.frame%len(spinnerFrames)]
	d.frame++
	seconds := int(d.now().Sub(d.started) / time.Second)
	line := truncateImportProgress(formatImportProgress(frame, d.progress, seconds), d.width())
	_, _ = fmt.Fprint(d.w, "\r\x1b[2K", line)
}

func (d *importProgressDisplay) stop() {
	if !d.interactive {
		return
	}
	d.mu.Lock()
	running := d.running
	d.mu.Unlock()
	if !running {
		return
	}
	d.stopOnce.Do(func() { close(d.stopCh) })
	<-d.doneCh
	d.erase()
}

func (d *importProgressDisplay) erase() {
	if d.interactive {
		_, _ = io.WriteString(d.w, "\r\x1b[2K")
	}
}

func formatImportProgress(frame rune, p vm.ImportProgress, seconds int) string {
	switch p.Phase {
	case vm.PhaseExtract:
		if p.Total > 0 {
			return fmt.Sprintf("  %c Converting image layers (%d/%d)… %ds", frame, p.Layers, p.Total, seconds)
		}
		return fmt.Sprintf("  %c Linking image layers… %ds", frame, seconds)
	default:
		if p.Bytes > 0 {
			return fmt.Sprintf("  %c Exporting image from Docker (%s)… %ds", frame, formatBytes(p.Bytes), seconds)
		}
		return fmt.Sprintf("  %c Exporting image from Docker… %ds", frame, seconds)
	}
}

func formatBytes(bytes int64) string {
	const (
		kib = int64(1 << 10)
		mib = int64(1 << 20)
		gib = int64(1 << 30)
	)
	switch {
	case bytes < kib:
		return "< 1 KiB"
	case bytes < mib:
		return fmt.Sprintf("%d KiB", bytes/kib)
	case bytes < gib:
		return fmt.Sprintf("%d MiB", bytes/mib)
	case bytes%gib == 0:
		return fmt.Sprintf("%d GiB", bytes/gib)
	default:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
	}
}

func truncateImportProgress(line string, width int) string {
	runes := []rune(line)
	if width <= 0 || len(runes) <= width {
		return line
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

// pruneImageCache removes workspace images that no workspace or
// builder spec uses any more. Failures only warn: the cache is an
// optimisation.
func pruneImageCache(current string) {
	keep := []string{current}
	specs, _ := filepath.Glob(filepath.Join(vmRootDir(), "workspaces", "*", vm.SpecFile("")))
	more, _ := filepath.Glob(filepath.Join(vmRootDir(), "workspaces", "*", "builder", vm.SpecFile("")))
	specs = append(append(specs, more...), filepath.Join(vmRootDir(), "builder", vm.SpecFile("")))
	for _, path := range specs {
		spec, err := vm.ReadSpec(filepath.Dir(path))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: prune VM image cache skipped: cannot read %s: %v\n", path, err)
				return
			}
			continue
		}
		keep = append(keep, spec.Image)
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

// pruneRuntimeCache removes unpacked VM runtimes and extracted guest binaries
// unused for seven days that no workspace or builder spec references.
func pruneRuntimeCache(rt vmRuntimeFiles) {
	keep := append([]string{rt.runner, rt.guest}, filepath.SplitList(rt.libDir)...)
	specs, _ := filepath.Glob(filepath.Join(vmRootDir(), "workspaces", "*", vm.SpecFile("")))
	more, _ := filepath.Glob(filepath.Join(vmRootDir(), "workspaces", "*", "builder", vm.SpecFile("")))
	specs = append(append(specs, more...), filepath.Join(vmRootDir(), "builder", vm.SpecFile("")))
	for _, path := range specs {
		spec, err := vm.ReadSpec(filepath.Dir(path))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: prune VM runtime cache skipped: cannot read %s: %v\n", path, err)
				return
			}
			continue
		}
		keep = append(append(keep, spec.RunnerPath, spec.GuestBinary), filepath.SplitList(spec.LibDir)...)
	}
	for _, sub := range []string{"runtime", "guest"} {
		if _, err := vm.PruneCacheDirs(filepath.Join(vmRootDir(), sub), keep); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: prune VM %s cache: %v\n", sub, err)
		}
	}
}

// workspaceSpec describes the workspace VM booting ref.
func workspaceSpec(ctx context.Context, ws *workspace.Resolved, rt vmRuntimeFiles, ref string, shares []vm.Share, snapshots []guest.Snapshot, ca *docker.BuildCA) (vm.Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return vm.Spec{}, fmt.Errorf("locate jailoc executable: %w", err)
	}
	memMiB, err := vmMemoryMiB(ws.Memory)
	if err != nil {
		return vm.Spec{}, err
	}
	diskSize, err := vmDockerDiskSize(ws.DockerDisk)
	if err != nil {
		return vm.Spec{}, err
	}
	stateDiskSize, err := vmStateDiskSize(ws.StateDisk)
	if err != nil {
		return vm.Spec{}, err
	}
	cacheDiskSize, err := vmCacheDiskSize(ws.CacheDisk)
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
		Tmpfs:     slices.Clone(ws.Agent.TmpfsDirs),
		Env:       append([]string{"JAILOC=1", "JAILOC_WORKSPACE=" + ws.Name}, ws.Agent.Env...),
		IPv6:      ws.IPv6,
		TimeZone:  workspaceTimeZone(ws.TimeZone),
		CacheDir:  guestUserCache,
		Tmp:       workspaceTmp(ws),
	}
	switch ws.Agent.TUIPlugin {
	case agent.OpenCode1TUI:
		g.Env = append(g.Env, ws.Agent.TUIPlugin.Env()+"="+filepath.Join(guestCacheDir, "tui-container.json"))
	case agent.OpenCode2TUI:
		content, err := cliConfigContent(ws)
		if err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: not loading the jailoc TUI plugin: %v\n", err)
		} else {
			g.Env = append(g.Env, ws.Agent.TUIPlugin.Env()+"="+content)
		}
	}
	if _, err := os.Stat(filepath.Join(config.ConfigDir(), "workspaces", ws.Name, caBundleFilename)); err == nil {
		g.CABundle = filepath.Join(guestConfigDir, caBundleFilename)
	}
	agent := vmSSHAgent(ws)
	g.SSHAgent = agent != ""
	gpgAgent := vmGPGAgent(ctx, ws)
	g.GPGAgent = gpgAgent != ""
	hostServices := vmHostServices(ws)
	for _, s := range hostServices {
		g.HostPorts = append(g.HostPorts, s.GuestPort)
	}
	// The relay and its broker forward exist in every workspace VM, so a
	// reload can add proxy routes later; without routes its allowlist is empty.
	g.ProxyRelay = &guest.ProxyRelay{Ports: proxyRelayPorts(ws.Proxy)}
	spec := vm.Spec{
		Kind:          vm.KindWorkspace,
		Workspace:     ws.Name,
		StateDir:      vmStateDir(ws.Name),
		Image:         ref,
		CPUs:          uint32(math.Ceil(ws.CPU)),
		MemoryMiB:     memMiB,
		DiskSize:      diskSize,
		Shares:        shares,
		Policy:        policy,
		RunnerPath:    rt.runner,
		LibDir:        rt.libDir,
		ImageCache:    filepath.Join(vmRootDir(), "images"),
		Broker:        []string{exe, "netbroker"},
		GuestBinary:   rt.guest,
		GuestProtocol: guest.Protocol,
		BrokerPolicy:  netbroker.PolicyVersion,
		Agent:         ws.Agent.Name,
		Guest:         g,
		Forwards:      vmForwards(ws.Ports),
		SSHAgent:      agent,
		GPGAgent:      gpgAgent,
		HostServices:  hostServices,
		BuildCA:       buildCAState(ca),
	}
	if ws.TmpStorage == config.TmpStorageDisk {
		spec.TmpDiskSize = ws.TmpSizeBytes
	}
	stateDirs, cache := workspaceStorageDisks(ws, shares)
	if len(stateDirs) > 0 {
		spec.StateDisk = true
		spec.StateDiskSize = stateDiskSize
		g.State = &guest.StateDisk{Agent: cmp.Or(ws.Agent.Builtin, ws.Agent.Name), Dirs: stateDirs, Ephemeral: ws.AgentProfile != nil && ws.AgentProfile.Ephemeral()}
	}
	if cache {
		spec.CacheDisk = true
		spec.CacheDiskSize = cacheDiskSize
		g.Cache = &guest.CacheDisk{}
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

// vmHostServices returns the host_ports relays of the workspace VM and warns
// about targets that hand the agent broad control of the host.
func vmHostServices(ws *workspace.Resolved) []vm.HostService {
	var out []vm.HostService
	for _, t := range ws.HostPorts {
		s := vm.HostService{GuestPort: uint16(t.Guest)} //nolint:gosec // G115: validated to 1-65535
		target := fmt.Sprintf("127.0.0.1:%d", t.HostPort)
		if t.Socket != "" {
			s.Path = t.Socket
			target = t.Socket
			if info, err := os.Stat(t.Socket); err != nil || info.Mode()&os.ModeSocket == 0 {
				_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: host_ports %d: %s is not a socket yet; connections fail until it exists\n", t.Guest, t.Socket)
			}
		} else {
			s.TCP = target
		}
		if why := t.Dangerous(); why != "" {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: host_ports %d relays to %s: %s\n", t.Guest, target, why)
		}
		out = append(out, s)
	}
	return out
}

// secretsPayload reads the workspace environment and secret values on the
// host. Env secrets go to the session environment only, file secrets to
// /run/secrets. Values stay in memory.
func secretsPayload(ctx context.Context, ws *workspace.Resolved, spec vm.Spec, purgeCache bool) (guest.Secrets, error) {
	if purgeCache {
		if err := purgeWorkspaceSecretCache(ctx, ws); err != nil {
			return guest.Secrets{}, err
		}
	}
	payload := guest.Secrets{Env: append([]string{}, ws.Env...), Files: map[string][]byte{}}
	if spec.SSHAgent != "" {
		payload.Env = append(payload.Env, "SSH_AUTH_SOCK="+guest.SSHAgentSocket)
	}
	if spec.GPGAgent != "" {
		payload.GPGPublicKeys = gpgPublicKeys(ctx)
	}
	if spec.Guest != nil && spec.Guest.CABundle != "" {
		payload.Env = append(payload.Env, "NODE_USE_SYSTEM_CA=1")
	}
	for _, s := range ws.Secrets {
		value, err := readSecret(ctx, ws.Name, s, purgeCache)
		if err != nil {
			return guest.Secrets{}, err
		}
		if s.Kind == workspace.SecretKindEnv {
			payload.Env = append(payload.Env, s.Name+"="+string(value))
		} else {
			payload.Files[s.Name] = value
		}
	}
	if spec.Guest != nil && spec.Guest.CABundle != "" {
		payload.Env = appendUnsetEnv(payload.Env, caBundleEnv())
	}
	return payload, nil
}

// caBundleEnv sets guest.CABundleVars to the guest's system CA bundle.
func caBundleEnv() []string {
	env := make([]string, 0, len(guest.CABundleVars))
	for _, k := range guest.CABundleVars {
		env = append(env, k+"="+guest.SystemCABundle)
	}
	return env
}

// appendUnsetEnv appends the entries of add whose keys env does not set, so
// a workspace's own env or secret wins.
func appendUnsetEnv(env, add []string) []string {
	set := make(map[string]bool, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		set[k] = true
	}
	for _, e := range add {
		if k, _, _ := strings.Cut(e, "="); !set[k] {
			env = append(env, e)
		}
	}
	return env
}

// secretsMarker exists while the running workspace VM holds the secrets of
// its latest delivery.
func secretsMarker(stateDir string) string { return filepath.Join(stateDir, "secrets-delivered") }

// deliverSecrets sends payload to the guest over SSH, which replaces the
// previous environment and secrets, and records the delivery. The supervisor
// learns the secret values first, so it redacts them from console.log before
// the guest can print them.
func deliverSecrets(ctx context.Context, stateDir string, payload guest.Secrets) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	if err := vm.SendConsoleSecrets(ctx, stateDir, guest.SecretsValues(payload)); err != nil {
		return fmt.Errorf("register secrets for console redaction: %w", err)
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
		return fmt.Errorf("%w: %s", err, escapeLines(strings.TrimSpace(stderr.String())))
	}
	// The guest writes stderr, so control characters are escaped before
	// they reach the host terminal.
	for line := range strings.Lines(strings.TrimSpace(stderr.String())) {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: %s\n", escapeVisible(strings.TrimSpace(line)))
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
	payload, err := secretsPayload(ctx, ws, *spec, false)
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
func downVM(ctx context.Context, ws *workspace.Resolved, purge bool) error {
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
		purgeErr := purgeWorkspaceSecretCache(ctx, ws)
		purgeWorkspaceAgentStateVersions(stateDir)
		if err := vm.RemoveTree(stateDir); err != nil {
			return errors.Join(purgeErr, fmt.Errorf("delete vm state: %w", err))
		}
		if purgeErr != nil {
			return purgeErr
		}
		_, _ = color.New(color.FgGreen).Printf("Workspace %s VM deleted\n", ws.Name)
		pruneBackgroundProxy()
		return nil
	}
	_, _ = color.New(color.FgGreen).Printf("Workspace %s stopped\n", ws.Name)
	pruneBackgroundProxy()
	return nil
}

// statusVM prints the agent of the workspace, whether its VM runs and, for a
// running VM, its resources, sessions and whether it runs the current
// configuration; for a stopped VM, when and why it stopped.
func statusVM(ctx context.Context, ws *workspace.Resolved) error {
	stateDir := vmStateDir(ws.Name)
	st, err := runningVM(stateDir)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir() // without a home directory paths stay absolute
	fmt.Printf("Workspace: %s\n", ws.Name)
	fmt.Printf("Backend:   vm\n")
	fmt.Printf("Agent:     %s\n", agentLabel(ws))
	if st == nil {
		last, err := vm.ReadState(stateDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Printf("State:     %s\n", stoppedLabel(last))
		if last != nil && last.Error != "" {
			fmt.Printf("Error:     %s\n", last.Error)
		}
		fmt.Printf("Data:      %s\n", shortenHome(stateDir, home))
		return nil
	}
	fmt.Printf("State:     running (supervisor pid %d)\n", st.PID)
	fmt.Printf("Started:   %s (up %s)\n", st.Updated.Local().Format(time.RFC3339), formatUptime(time.Since(st.Updated)))
	spec, err := vm.ReadSpec(stateDir)
	if err != nil {
		return err
	}
	fmt.Printf("Resources: %s\n", resourcesLabel(*spec))
	fmt.Printf("Profile:   %s\n", profileLabel(*spec))
	fmt.Printf("Tmp:       %s\n", tmpStatus(ctx, stateDir, *spec))
	if label := proxyStatusLabel(ws); label != "" {
		fmt.Printf("Proxy:     %s\n", label)
	}
	sessionsCtx, cancel := context.WithTimeout(ctx, listSessionsTimeout)
	defer cancel()
	if infos, err := vmSessions(sessionsCtx, ws); err != nil {
		fmt.Printf("Sessions:  unknown (%v)\n", err)
	} else {
		attached := 0
		for _, info := range infos {
			if info.Attached {
				attached++
			}
		}
		label := sessionsLabel(len(infos), attached)
		if len(infos) > 0 {
			label += fmt.Sprintf("; `jailoc sessions %s` lists them", ws.Name)
		}
		fmt.Printf("Sessions:  %s\n", label)
	}
	fmt.Printf("Config:    %s\n", configLabel(ws, *spec, configChangedSince(stateDir)))
	fmt.Printf("Data:      %s\n", shortenHome(stateDir, home))
	return nil
}

// stoppedLabel describes a stopped VM from its last supervisor state, nil
// when the VM never started.
func stoppedLabel(last *vm.State) string {
	switch {
	case last == nil:
		return "stopped (never started)"
	case last.Ready:
		// The supervisor clears Ready when it exits; it was killed.
		return fmt.Sprintf("stopped (supervisor pid %d exited unexpectedly)", last.PID)
	default:
		return "stopped since " + last.Updated.Local().Format(time.RFC3339)
	}
}

func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}

func resourcesLabel(spec vm.Spec) string {
	label := fmt.Sprintf("%d CPUs, %d MiB memory", spec.CPUs, spec.MemoryMiB)
	if spec.Docker {
		label += fmt.Sprintf(", %d GiB Docker disk", cmp.Or(spec.DiskSize, int64(vm.DefaultDiskSize))>>30)
	}
	return label
}

func sessionsLabel(sessions, attached int) string {
	if attached == 0 {
		return strconv.Itoa(sessions)
	}
	return fmt.Sprintf("%d (%d attached)", sessions, attached)
}

// configChangedSince reports whether the configuration file changed after
// the VM spec was last written by up, restart or reload.
func configChangedSince(stateDir string) bool {
	configInfo, err := os.Stat(config.ConfigPath())
	if err != nil {
		return false
	}
	specInfo, err := os.Stat(vm.SpecFile(stateDir))
	return err == nil && configInfo.ModTime().After(specInfo.ModTime())
}

// configLabel tells whether the running VM matches the configuration: the
// settings that need a restart, or a reload after any other change of the
// configuration file.
func configLabel(ws *workspace.Resolved, running vm.Spec, changed bool) string {
	if restart := statusRestartChanges(ws, running); len(restart) > 0 {
		return fmt.Sprintf("`jailoc restart %s` needed for %s; this ends running sessions", ws.Name, joinList(restart))
	}
	if changed {
		return fmt.Sprintf("changed since the VM started; `jailoc reload %s` applies it", ws.Name)
	}
	return "up to date"
}

// statusRestartChanges names the settings that differ between the running
// VM and the workspace and need a restart. Unlike restartOnlyChanges it
// reads nothing but the resolved workspace, so status changes nothing.
func statusRestartChanges(ws *workspace.Resolved, running vm.Spec) []string {
	var out []string
	var profile string
	var ephemeral bool
	if ws.AgentProfile != nil {
		profile, _ = ws.AgentProfile.Name()
		ephemeral = ws.AgentProfile.Ephemeral()
	}
	if running.Profile != profile || running.EphemeralProfile != ephemeral {
		out = append(out, "profile")
	}
	if running.CPUs != uint32(math.Ceil(ws.CPU)) {
		out = append(out, "cpu")
	}
	if mem, err := vmMemoryMiB(ws.Memory); err == nil && running.MemoryMiB != mem {
		out = append(out, "memory")
	}
	if size, err := vmDockerDiskSize(ws.DockerDisk); err == nil && running.Docker && ws.EnableDocker && cmp.Or(running.DiskSize, int64(vm.DefaultDiskSize)) != size {
		out = append(out, "docker_disk")
	}
	if size, err := vmStateDiskSize(ws.StateDisk); err == nil && running.StateDisk && cmp.Or(running.StateDiskSize, int64(vm.DefaultStateDiskSize)) != size {
		out = append(out, "state_disk")
	}
	if size, err := vmCacheDiskSize(ws.CacheDisk); err == nil && running.CacheDisk && cmp.Or(running.CacheDiskSize, int64(vm.DefaultCacheDiskSize)) != size {
		out = append(out, "cache_disk")
	}
	if running.Docker != ws.EnableDocker {
		out = append(out, "enable_docker")
	}
	out = append(out, tmpRestartChanges(effectiveTmp(running), workspaceTmp(ws))...)
	if running.Guest != nil && running.Guest.NoPrivilegeEscalation != ws.ProtectGit {
		out = append(out, "protect_git")
	}
	if running.Guest != nil && guestIPv6(running) != ws.IPv6 {
		out = append(out, "network.ipv6")
	}
	// A reload replaces the relay allowlist of a VM whose guest supports it.
	if running.Guest != nil && !proxyReloadable(running) {
		out = append(out, proxyChanges(ws, running.Guest)...)
	}
	// Specs written before jailoc ran other agents than OpenCode name none.
	agentChanged := cmp.Or(running.Agent, agent.Default) != ws.Agent.Name
	if agentChanged {
		out = append(out, "agent")
	}
	var runningTmpfs []string
	if running.Guest != nil {
		runningTmpfs = running.Guest.Tmpfs
	}
	if !agentChanged && !slices.Equal(runningTmpfs, ws.Agent.TmpfsDirs) {
		out = append(out, "tmpfs mounts")
	}
	if !guestCompatible(running) {
		out = append(out, "the upgraded jailoc guest")
	}
	return out
}

// workspaceStatus reports whether the workspace VM runs and, for a running
// VM, how many agent sessions it has and how many of them have a terminal
// attached.
func workspaceStatus(ctx context.Context, ws *workspace.Resolved) (running bool, sessions, attached int, err error) {
	st, err := runningVM(vmStateDir(ws.Name))
	if err != nil || st == nil {
		return false, 0, 0, err
	}
	infos, err := vmSessions(ctx, ws)
	if err != nil {
		return true, 0, 0, err
	}
	for _, info := range infos {
		if info.Attached {
			attached++
		}
	}
	return true, len(infos), attached, nil
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

// upLogPath is the log of the last `jailoc up` of a workspace: its steps with
// their durations, including the image builds.
func upLogPath(stateDir string) string { return filepath.Join(stateDir, "up.log") }

// teeUpLog writes the log records of this `jailoc up` to the workspace's
// up.log too, until the returned function is called.
func teeUpLog(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create vm state dir: %w", err)
	}
	f, err := os.OpenFile(upLogPath(stateDir), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("open up log: %w", err)
	}
	restore := logging.Tee(f)
	return func() {
		restore()
		_ = f.Close()
	}, nil
}

// logsVM prints the log of the last `jailoc up`, the supervisor log and the
// guest console log. follow keeps printing what is added to all three, so
// that it shows a running `jailoc up` from its image build to the VM boot,
// and the next start after it.
func logsVM(ctx context.Context, ws *workspace.Resolved, follow bool, w io.Writer) error {
	stateDir := vmStateDir(ws.Name)
	consolePaths := vm.ConsoleLogPaths(stateDir)
	hasConsole := false
	for _, p := range consolePaths {
		if fileExists(p) {
			hasConsole = true
			break
		}
	}
	if !hasConsole && !fileExists(upLogPath(stateDir)) {
		return fmt.Errorf("workspace %q has no VM logs; start it with 'jailoc up'", ws.Name)
	}
	upTail := &logTail{path: upLogPath(stateDir)}
	supTail := &logTail{path: filepath.Join(stateDir, "supervisor.log")}
	conTail := &logTail{path: consolePaths[len(consolePaths)-1], backups: consolePaths[:len(consolePaths)-1]}
	tails := []*logTail{upTail, supTail, conTail}
	defer func() {
		for _, t := range tails {
			t.close()
		}
	}()
	for {
		for _, t := range tails {
			if err := t.copy(w); err != nil {
				return err
			}
		}
		if !follow || !waitLogs(ctx) {
			return nil
		}
	}
}

// logTail prints what is added to a log file. It starts from the beginning
// again when the file is replaced or truncated, as the next `jailoc up` and
// VM start do with theirs. backups lists the files path rotates into, oldest
// first: they are printed before the first read of path, and the ones newer
// than the open file after each rotation.
type logTail struct {
	path    string
	backups []string
	started bool
	f       *os.File
	off     int64
}

// copy prints what was added to the file since the last copy; a missing
// file has nothing to print yet.
func (t *logTail) copy(w io.Writer) error {
	if !t.started {
		t.started = true
		if err := printFiles(w, t.backups); err != nil {
			return err
		}
	}
	if t.f != nil {
		// Drain anything appended to the open descriptor before checking
		// whether t.path was rotated to a new file.
		n, err := io.Copy(w, t.f)
		t.off += n
		if err != nil {
			return fmt.Errorf("read %s: %w", t.path, err)
		}
	}
	fi, err := os.Stat(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", t.path, err)
	}
	if t.f != nil {
		cur, err := t.f.Stat()
		switch {
		case err != nil, os.SameFile(cur, fi) && fi.Size() < t.off:
			t.close()
		case !os.SameFile(cur, fi):
			// The open file was rotated away: print the backups newer than
			// it, which a single poll misses when the log rotates twice.
			t.close()
			if err := printFiles(w, t.backups[t.backupIndex(cur)+1:]); err != nil {
				return err
			}
		}
	}
	if t.f == nil {
		f, err := os.Open(t.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("open %s: %w", t.path, err)
		}
		t.f, t.off = f, 0
	}
	n, err := io.Copy(w, t.f)
	t.off += n
	if err != nil {
		return fmt.Errorf("read %s: %w", t.path, err)
	}
	return nil
}

func (t *logTail) close() {
	if t.f != nil {
		_ = t.f.Close()
		t.f = nil
	}
}

// backupIndex returns the index in t.backups of the file fi, or -1 when it is
// not one of them (it was rotated out, or removed at the next VM start).
func (t *logTail) backupIndex(fi os.FileInfo) int {
	for i, p := range t.backups {
		if bi, err := os.Stat(p); err == nil && os.SameFile(bi, fi) {
			return i
		}
	}
	return -1
}

// printFiles prints the files in paths in order, skipping missing ones.
func printFiles(w io.Writer, paths []string) error {
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // G304: jailoc-owned state path
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// waitLogs waits for followed logs to grow and reports false once ctx is
// done.
func waitLogs(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(500 * time.Millisecond):
		return true
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// restartVMIfRunning restarts a running workspace VM so configuration
// changes (e.g. new paths) take effect.
func restartVMIfRunning(ctx context.Context, ws *workspace.Resolved) error {
	if st, err := runningVM(vmStateDir(ws.Name)); err != nil || st == nil {
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
	// Fail before stopping, so an invalid secret source does not leave the
	// workspace down.
	if err := validateSecretSources(ws2); err != nil {
		return fmt.Errorf("validate secrets for workspace %q: %w", ws2.Name, err)
	}
	if err := downVM(ctx, ws, false); err != nil {
		return err
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

// proxyRelayPorts returns the unique guest ports of routes in ascending order:
// the allowlist of the guest's proxy relay.
func proxyRelayPorts(routes []workspace.ProxyRoute) []uint16 {
	var ports []uint16
	for _, r := range routes {
		ports = append(ports, uint16(r.Port)) //nolint:gosec // G115: validated to 1-65535
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}

// guestProxyPorts returns the proxy relay allowlist of g, in ascending order.
func guestProxyPorts(g *guest.Config) []uint16 {
	if g == nil || g.ProxyRelay == nil {
		return nil
	}
	return slices.Compact(slices.Sorted(slices.Values(g.ProxyRelay.Ports)))
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
