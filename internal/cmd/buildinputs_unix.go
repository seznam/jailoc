//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// Kinds of build input, as error messages name them.
const (
	inputBaseDockerfile = "base Dockerfile"
	inputDockerfile     = "Dockerfile"
	inputContext        = "build context"
)

// buildInput is a local host path that the build of a workspace image reads.
// dir marks a build context, whose whole tree the build reads.
type buildInput struct {
	kind string
	path string
	dir  bool
}

// effectiveBuildInputs returns the local files and directories that
// ResolveAndLayerImage reads to build the image of ws: the [base] Dockerfile
// of the base image cascade, the workspace Dockerfile and its build context.
// Images used directly, Dockerfiles fetched from a URL and the empty context
// of a URL Dockerfile read nothing local. The paths are as the build receives
// them, not cleaned: a lexical clean would drop the effect of a symbolic link
// followed by "..".
func effectiveBuildInputs(cfg *config.Config, ws *workspace.Resolved) ([]buildInput, error) {
	var inputs []buildInput
	switch strategy, _ := resolveImageStrategy(ws.Image, cfg.Defaults.Image, ws.Dockerfile); strategy {
	case strategyDirectImage, strategyDefaultsDirect:
		return nil, nil
	case strategyCascade:
		if base := strings.TrimSpace(cfg.Base.Dockerfile); base != "" {
			local, err := docker.IsLocalDockerfile(base)
			if err != nil {
				return nil, fmt.Errorf("base dockerfile: %w", err)
			}
			if local {
				inputs = append(inputs, buildInput{kind: inputBaseDockerfile, path: base})
			}
		}
	}
	// The overlay is built only from a Dockerfile; without one the build
	// context is unused. The build uses the workspace paths as they are,
	// trimming them only to tell whether they are set, and so do the inputs.
	dockerfile := ws.Dockerfile
	if strings.TrimSpace(dockerfile) == "" {
		return inputs, nil
	}
	local, err := docker.IsLocalDockerfile(dockerfile)
	if err != nil {
		return nil, fmt.Errorf("workspace %q dockerfile: %w", ws.Name, err)
	}
	if local {
		inputs = append(inputs, buildInput{kind: inputDockerfile, path: dockerfile})
	}
	switch {
	case strings.TrimSpace(ws.BuildContext) != "":
		inputs = append(inputs, buildInput{kind: inputContext, path: ws.BuildContext, dir: true})
	case local:
		inputs = append(inputs, buildInput{kind: inputContext, path: filepath.Dir(dockerfile), dir: true})
	}
	return inputs, nil
}

// workspaceShare is a share of the VM of a workspace.
type workspaceShare struct {
	workspace string
	share     vm.Share
}

// vmExports returns what the VM of workspace name in stateDir exports to its
// guest: shares, the guest data directory and the root file system it boots
// from, which the guest can change.
func vmExports(name, stateDir string, shares []vm.Share) []workspaceShare {
	return append(ownShares(name, shares),
		workspaceShare{workspace: name, share: vm.Share{HostPath: vm.GuestDataDir(stateDir)}},
		workspaceShare{workspace: name, share: vm.Share{HostPath: vm.BootRootDir(stateDir)}})
}

// ownShares are shares as the shares of workspace name.
func ownShares(name string, shares []vm.Share) []workspaceShare {
	owned := make([]workspaceShare, 0, len(shares)+1)
	for _, s := range shares {
		owned = append(owned, workspaceShare{workspace: name, share: s})
	}
	return owned
}

// pathTrace is a path as the kernel resolves it.
type pathTrace struct {
	// real is the path without symbolic links, ending in the components
	// that do not exist yet.
	real string
	// links are the symbolic links on the way, at their real paths.
	links []string
	// detours are the directories on the way that real does not run
	// through, which a later ".." left.
	detours []string
}

// tracePath resolves the absolute path one component at a time, as the
// kernel does: a symbolic link is followed before a later ".." goes up from
// its target, and a dangling link leads to the path its target would have.
// A missing component followed by ".." cannot be told apart from a link the
// guest creates later, so it fails, like a file followed by any component,
// also "/", "." and "..".
func tracePath(path string) (pathTrace, error) {
	var t pathTrace
	if !filepath.IsAbs(path) {
		return t, fmt.Errorf("resolve %s: not an absolute path", path)
	}
	var dirs []string
	done := func(real string) (pathTrace, error) {
		t.real = real
		for _, d := range dirs {
			if !pathWithin(real, d) && !slices.Contains(t.detours, d) {
				t.detours = append(t.detours, d)
			}
		}
		return t, nil
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
		if errors.Is(err, fs.ErrNotExist) {
			if slices.Contains(rest, "..") {
				return t, fmt.Errorf("resolve %s: %s does not exist and the path goes up from it", path, next)
			}
			return done(filepath.Join(append([]string{next}, rest...)...))
		}
		if err != nil {
			return t, fmt.Errorf("resolve %s: %w", path, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			if !info.IsDir() && len(rest) > 0 {
				// Even "/", "." and ".." after a file fail, which the dot
				// components above would skip.
				return t, fmt.Errorf("resolve %s: %s: %w", path, next, syscall.ENOTDIR)
			}
			cur = next
			dirs = append(dirs, next)
			continue
		}
		if hops++; hops > maxLinkHops {
			return t, fmt.Errorf("resolve %s: more than %d symbolic links", path, maxLinkHops)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return t, fmt.Errorf("resolve %s: %w", path, err)
		}
		t.links = append(t.links, next)
		if filepath.IsAbs(target) {
			cur = sep
		}
		rest = append(strings.Split(target, sep), rest...)
	}
	return done(cur)
}

// inDir reports whether the path p, free of symbolic links, lies in the
// directory dir, also free of them. Besides the names it compares the
// identity info of dir with the existing ancestors of p, which catches the
// other spellings of a directory on a case-insensitive file system and its
// bind mounts.
func inDir(p, dir string, info fs.FileInfo) bool {
	if pathWithin(p, dir) {
		return true
	}
	if info == nil {
		return false
	}
	for c := p; ; c = filepath.Dir(c) {
		if ci, err := os.Stat(c); err == nil && os.SameFile(ci, info) {
			return true
		}
		if c == filepath.Dir(c) {
			return false
		}
	}
}

// existingAncestor returns the file info of p or of its closest existing
// ancestor.
func existingAncestor(p string) fs.FileInfo {
	for c := p; ; c = filepath.Dir(c) {
		if info, err := os.Stat(c); err == nil {
			return info
		}
		if c == filepath.Dir(c) {
			return nil
		}
	}
}

// fileDevice returns the device that holds the file of info.
func fileDevice(info fs.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:gosec,unconvert // G115: device numbers are not negative; Dev is int32 on darwin
}

// writableRoot is a read-write export with its real path.
type writableRoot struct {
	export workspaceShare
	real   string
	info   fs.FileInfo // nil while the directory does not exist
	dev    uint64
	hasDev bool
}

// maxContextScanEntries bounds the entries checkBuildInputs walks in one
// build context, like maxGitScanDirs bounds the Git scan.
const maxContextScanEntries = 500_000

// checkBuildInputs rejects the build inputs of workspace owner that a
// read-write export in exports lets a guest change: an input that lies in
// the export, a build context that contains it, an input whose path runs
// through a symbolic link inside it, and a Dockerfile or a file in a build
// context with other hard links on the device of an export, whose other
// names it may hold. Read-only shares are allowed. Paths are resolved as the
// kernel resolves them when the build opens them, and a missing input by the
// path it would have, so the guest cannot create it later. An input or
// export that cannot be resolved fails the check. The error lists every
// conflict.
func checkBuildInputs(owner string, inputs []buildInput, exports []workspaceShare) error {
	roots, err := writableRoots(exports)
	if err != nil || len(roots) == 0 {
		return err
	}
	// A hard link names a file on one device only.
	exposedLink := func(info fs.FileInfo) bool {
		if !hardLinked(info) {
			return false
		}
		dev, ok := fileDevice(info)
		return !ok || slices.ContainsFunc(roots, func(r writableRoot) bool { return !r.hasDev || r.dev == dev })
	}
	var errs []error
	for _, in := range inputs {
		name := fmt.Sprintf("%s %s of workspace %q", in.kind, in.path, owner)
		t, err := tracePath(in.path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		var info fs.FileInfo
		if i, err := os.Stat(t.real); err == nil {
			info = i
		}
		conflicts := len(errs)
		for _, r := range roots {
			switch {
			case inDir(t.real, r.real, r.info):
				errs = append(errs, fmt.Errorf("%s lies in the read-write share %s of workspace %q", name, r.export.share.HostPath, r.export.workspace))
			case in.dir && inDir(r.real, t.real, info):
				errs = append(errs, fmt.Errorf("%s contains the read-write share %s of workspace %q", name, r.export.share.HostPath, r.export.workspace))
			}
		}
		if len(errs) == conflicts {
			if err := throughShare(name, t, roots); err != nil {
				errs = append(errs, err)
			}
		}
		switch {
		case len(errs) > conflicts:
			// Walking a context that is shared already would only repeat it.
		case in.dir:
			linked, err := contextHardLink(t.real, maxContextScanEntries, exposedLink)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			case linked != "":
				errs = append(errs, fmt.Errorf("%s: %q has other hard links, which a read-write share can expose; give it a file of its own", name, linked))
			}
		case info != nil && info.Mode().IsRegular() && exposedLink(info):
			errs = append(errs, fmt.Errorf("%s has other hard links, which a read-write share can expose; give it a file of its own", name))
		}
	}
	return errors.Join(errs...)
}

// writableRoots resolves the read-write exports among exports.
func writableRoots(exports []workspaceShare) ([]writableRoot, error) {
	var roots []writableRoot
	for _, e := range exports {
		if e.share.ReadOnly {
			continue
		}
		t, err := tracePath(e.share.HostPath)
		if err != nil {
			return nil, fmt.Errorf("resolve the read-write share %s of workspace %q: %w", e.share.HostPath, e.workspace, err)
		}
		r := writableRoot{export: e, real: t.real}
		if info, err := os.Stat(t.real); err == nil {
			r.info = info
		}
		if info := existingAncestor(t.real); info != nil {
			r.dev, r.hasDev = fileDevice(info)
		}
		roots = append(roots, r)
	}
	return roots, nil
}

// throughShare rejects the path t of name when it runs through a symbolic
// link or a detour in a directory one of roots lets a guest write: the guest
// can repoint the link or replace the directory with one, and the path then
// leads elsewhere.
func throughShare(name string, t pathTrace, roots []writableRoot) error {
	in := func(p string) (writableRoot, bool) {
		i := slices.IndexFunc(roots, func(r writableRoot) bool { return inDir(filepath.Dir(p), r.real, r.info) })
		if i < 0 {
			return writableRoot{}, false
		}
		return roots[i], true
	}
	for _, link := range t.links {
		if r, ok := in(link); ok {
			return fmt.Errorf("%s runs through the symbolic link %q inside the read-write share %s of workspace %q", name, link, r.export.share.HostPath, r.export.workspace)
		}
	}
	for _, d := range t.detours {
		if r, ok := in(d); ok {
			return fmt.Errorf("%s runs through %q inside the read-write share %s of workspace %q, which the guest can replace with a symbolic link", name, d, r.export.share.HostPath, r.export.workspace)
		}
	}
	return nil
}

// checkExportPaths rejects a read-write export in exports whose path runs
// through a symbolic link or a detour that a read-write export of another
// workspace in others lets its guest change. The runner keeps what it
// exported at boot, but the checks resolve the path again, so they would
// look at the wrong directory. checkNestedShares covers the shares of one
// workspace.
func checkExportPaths(exports, others []workspaceShare) error {
	var errs []error
	for _, e := range exports {
		if e.share.ReadOnly {
			continue
		}
		t, err := tracePath(e.share.HostPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve the read-write share %s of workspace %q: %w", e.share.HostPath, e.workspace, err))
			continue
		}
		foreign := slices.DeleteFunc(slices.Clone(others), func(o workspaceShare) bool { return o.workspace == e.workspace })
		roots, err := writableRoots(foreign)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("the read-write share %s of workspace %q", e.share.HostPath, e.workspace)
		if err := throughShare(name, t, roots); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// contextHardLink returns the first regular file under dir for which linked
// reports other hard links that a read-write share could expose. Their
// other names may lie anywhere on the device, so a file the build sends can
// change through a share that does not contain it. A missing dir has no
// files; walking more than limit entries fails, so the check does not pass
// on a tree it did not finish.
func contextHardLink(dir string, limit int, linked func(fs.FileInfo) bool) (string, error) {
	var found string
	entries := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if p == dir {
			return nil
		}
		if entries++; entries > limit {
			return fmt.Errorf("more than %d entries to check for hard links; use a smaller build_context", limit)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if linked(info) {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("check %s for hard links: %w", dir, err)
	}
	return found, nil
}

// buildInputsRemedy follows a build input conflict.
const buildInputsRemedy = "keep build inputs out of read-write shares: share directories without them, mount them read-only, or use a prebuilt image"

// admitVM checks under the admission lock that the VM of ws may start with
// shares (checkVMAdmission) and records its shares and build inputs, so the
// checks of other workspaces see them while it builds, boots and runs. It
// holds the start lock of ws, which marks the record as current, until
// release; a second start of ws fails.
func admitVM(ctx context.Context, cfg *config.Config, ws *workspace.Resolved, shares []vm.Share) (release func(), err error) {
	stateDir := vmStateDir(ws.Name)
	err = withAdmissionLock(ctx, func() error {
		unlock, err := holdStartLock(stateDir, ws.Name)
		if err != nil {
			return err
		}
		if err := checkVMAdmission(cfg, ws, shares, vmRootDir()); err != nil {
			unlock()
			return err
		}
		inputs, err := effectiveBuildInputs(cfg, ws)
		if err == nil {
			err = writeAdmission(stateDir, shares, inputs)
		}
		if err != nil {
			unlock()
			return err
		}
		release = unlock
		return nil
	})
	return release, err
}

// checkVMAdmission rejects starting the VM of ws with shares under root when
// a read-write export exposes build inputs: those of ws, to its exports or
// to those of a workspace VM running or starting under root
// (checkBuildAdmission), or those of another configured or starting
// workspace to its exports (checkShareExposure).
func checkVMAdmission(cfg *config.Config, ws *workspace.Resolved, shares []vm.Share, root string) error {
	live, err := liveWorkspaces(root, ws.Name)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	stateDir := filepath.Join(root, "workspaces", ws.Name)
	earlier, err := earlierVM(ws.Name, stateDir)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	var liveExports []workspaceShare
	for _, l := range live {
		liveExports = append(liveExports, l.exports...)
	}
	exports := vmExports(ws.Name, stateDir, shares)
	err = errors.Join(
		checkBuildAdmission(cfg, ws, exports, append(live, earlier...)),
		checkShareExposure(cfg, exports, ws.Name, live),
		checkExportPaths(exports, liveExports),
		checkExportPaths(liveExports, exports),
	)
	if err != nil {
		return fmt.Errorf("workspace %q: %w\n%s", ws.Name, err, buildInputsRemedy)
	}
	return nil
}

// warnBuildInputExposure warns on w when the read-write exports of the
// running VM of workspace name under root expose the build inputs of a
// configured or starting workspace, itself included. Shares change only on
// restart, so a reload can only report it; checkVMAdmission refuses the
// builds and starts.
func warnBuildInputExposure(w io.Writer, cfg *config.Config, name string, shares []vm.Share, root string) {
	if cfg == nil {
		return
	}
	warn := color.New(color.FgYellow)
	live, err := liveWorkspaces(root, name)
	if err != nil {
		_, _ = warn.Fprintf(w, "WARNING: workspace %q: check the build inputs its shares expose: %v\n", name, err)
		return
	}
	exports := vmExports(name, filepath.Join(root, "workspaces", name), shares)
	if err := checkShareExposure(cfg, exports, "", live); err != nil {
		_, _ = warn.Fprintf(w, "WARNING: workspace %q: %v\nthe running VM keeps these shares, and the builds that read them are refused, until you change its paths or mounts and run `jailoc restart %s`; check the exposed files before the next build\n", name, err, name)
	}
}

// checkBuildAdmission rejects building the image of ws when a read-write
// export exposes its build inputs: one of exports, which the VM of ws will
// have, or an export of another workspace in live. A build reads its inputs
// while it runs, so they must be out of reach of every guest.
func checkBuildAdmission(cfg *config.Config, ws *workspace.Resolved, exports []workspaceShare, live []liveWorkspace) error {
	inputs, err := effectiveBuildInputs(cfg, ws)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return nil
	}
	all := slices.Clone(exports)
	for _, l := range live {
		all = append(all, l.exports...)
	}
	err = checkBuildInputs(ws.Name, inputs, all)
	if err != nil && len(live) > 0 {
		return fmt.Errorf("%w\nstop a running workspace that shares them with `jailoc down <workspace>`", err)
	}
	return err
}

// checkShareExposure rejects the read-write exports that expose the build
// inputs of a workspace other than skip: one configured in cfg or one in
// live, which may come from another configuration. A guest could change them
// while its VM runs, and the next build of that workspace would read the
// change.
func checkShareExposure(cfg *config.Config, exports []workspaceShare, skip string, live []liveWorkspace) error {
	owners := map[string][]buildInput{}
	for name := range cfg.Workspaces {
		if name == skip {
			continue
		}
		inputs, err := configuredBuildInputs(cfg, name)
		if err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
		owners[name] = inputs
	}
	for _, l := range live {
		if l.name == skip {
			continue
		}
		for _, in := range l.inputs {
			if !slices.Contains(owners[l.name], in) {
				owners[l.name] = append(owners[l.name], in)
			}
		}
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(owners)) {
		if err := checkBuildInputs(name, owners[name], exports); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// configuredBuildInputs is effectiveBuildInputs for workspace name as the
// configuration sets it, without resolving the rest of the workspace.
func configuredBuildInputs(cfg *config.Config, name string) ([]buildInput, error) {
	c := cfg.Workspaces[name]
	ws := workspace.Resolved{Name: name, Image: c.Image, Dockerfile: c.Dockerfile}
	if c.BuildContext != "" {
		expanded, err := config.ExpandPath(c.BuildContext)
		if err != nil {
			return nil, fmt.Errorf("expand build_context %q: %w", c.BuildContext, err)
		}
		if ws.BuildContext, err = filepath.Abs(expanded); err != nil {
			return nil, fmt.Errorf("resolve build_context %q: %w", expanded, err)
		}
	}
	return effectiveBuildInputs(cfg, &ws)
}

// liveWorkspace is a workspace whose VM runs or is being started, with what
// it exports and the build inputs its start recorded.
type liveWorkspace struct {
	name    string
	exports []workspaceShare
	inputs  []buildInput
}

// liveWorkspaces returns the workspaces under root, except workspace except,
// whose VM runs (its supervisor or its runner is alive) or that a jailoc
// process is starting (it holds the start lock). A state, spec or record
// that cannot be read fails the call: that VM could share anything.
func liveWorkspaces(root, except string) ([]liveWorkspace, error) {
	dir := filepath.Join(root, "workspaces")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list workspace VMs: %w", err)
	}
	var live []liveWorkspace
	for _, e := range entries {
		if !e.IsDir() || e.Name() == except {
			continue
		}
		name, stateDir := e.Name(), filepath.Join(dir, e.Name())
		running, err := vmAlive(stateDir)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", name, err)
		}
		starting, err := startLockHeld(stateDir)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", name, err)
		}
		if !running && !starting {
			continue
		}
		l := liveWorkspace{name: name}
		if running {
			spec, err := vm.ReadSpec(stateDir)
			if err != nil {
				return nil, fmt.Errorf("read VM spec of running workspace %q: %w", name, err)
			}
			l.exports = vmExports(name, stateDir, spec.Shares)
		}
		// A start that has not reached the admission lock yet has no
		// record; it checks the workspaces here once it does.
		rec, err := readAdmission(stateDir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("workspace %q: %w", name, err)
		default:
			if !running {
				l.exports = vmExports(name, stateDir, rec.Shares)
			}
			for _, in := range rec.Inputs {
				l.inputs = append(l.inputs, buildInput{kind: in.Kind, path: in.Path, dir: in.Dir})
			}
		}
		live = append(live, l)
	}
	return live, nil
}

// earlierVM returns the VM of workspace name in stateDir left from an
// earlier start, whose runner outlived its supervisor: it keeps its old
// shares until the new start stops it when it boots, after the build.
func earlierVM(name, stateDir string) ([]liveWorkspace, error) {
	alive, err := vmAlive(stateDir)
	if err != nil || !alive {
		return nil, err
	}
	spec, err := vm.ReadSpec(stateDir)
	if err != nil {
		return nil, fmt.Errorf("read VM spec of the earlier VM: %w", err)
	}
	return []liveWorkspace{{name: name, exports: vmExports(name, stateDir, spec.Shares)}}, nil
}

// lockPurge takes the start lock of workspace name before its state in
// stateDir is deleted, so a start that builds, boots or runs keeps the
// record the checks of other workspaces read.
func lockPurge(stateDir, name string) (release func(), err error) {
	release, err = holdStartLock(stateDir, name)
	if err != nil {
		return nil, fmt.Errorf("purge workspace %q: %w", name, err)
	}
	return release, nil
}

// checkVMGone fails while the supervisor or the runner of the VM of
// workspace name in stateDir is alive: a runner serves its shares, and
// purging the state would hide them from the checks of other workspaces.
func checkVMGone(stateDir, name string) error {
	alive, err := vmAlive(stateDir)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", name, err)
	}
	if alive {
		return fmt.Errorf("the VM of workspace %q still runs; stop its processes before purging", name)
	}
	return nil
}

// vmAlive reports whether the supervisor or the runner of the VM in stateDir
// is alive. A runner can outlive its supervisor and keeps the shares.
func vmAlive(stateDir string) (bool, error) {
	st, err := vm.ReadState(stateDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, fmt.Errorf("read VM state: %w", err)
	case processAlive(st.PID):
		return true, nil
	}
	pid, err := vm.RunnerPID(stateDir)
	if err != nil {
		return false, err
	}
	return processAlive(pid), nil
}

// admissionFile in a VM state directory records the shares and build inputs
// that admitted its start.
const admissionFile = "admission.json"

type admissionRecord struct {
	Shares []vm.Share       `json:"shares"`
	Inputs []admissionInput `json:"inputs"`
}

type admissionInput struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
}

// writeAdmission replaces the admission record in stateDir.
func writeAdmission(stateDir string, shares []vm.Share, inputs []buildInput) error {
	rec := admissionRecord{Shares: shares, Inputs: make([]admissionInput, 0, len(inputs))}
	for _, in := range inputs {
		rec.Inputs = append(rec.Inputs, admissionInput{Kind: in.kind, Path: in.path, Dir: in.dir})
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode admission record: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create VM state directory: %w", err)
	}
	tmp := filepath.Join(stateDir, admissionFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write admission record: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(stateDir, admissionFile)); err != nil {
		return fmt.Errorf("write admission record: %w", err)
	}
	return nil
}

// readAdmission reads the admission record in stateDir.
func readAdmission(stateDir string) (admissionRecord, error) {
	var rec admissionRecord
	data, err := os.ReadFile(filepath.Join(stateDir, admissionFile)) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return rec, fmt.Errorf("read admission record: %w", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("read admission record: %w", err)
	}
	return rec, nil
}

// startLockPath is the lock a jailoc process holds while it starts the VM in
// stateDir. It lives next to the state directory, like the reload lock.
func startLockPath(stateDir string) string { return stateDir + ".start.lock" }

// holdStartLock takes the start lock of workspace name without waiting.
// Callers hold the admission lock, so startLockHeld never races with it.
func holdStartLock(stateDir, name string) (release func(), err error) {
	path := startLockPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create directory of the start lock of workspace %q: %w", name, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, fmt.Errorf("open start lock of workspace %q: %w", name, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: file descriptors fit in int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("workspace %q is being started by another jailoc process", name)
		}
		return nil, fmt.Errorf("take start lock of workspace %q: %w", name, err)
	}
	return func() { _ = f.Close() }, nil // closing the descriptor releases the flock
}

// startLockHeld reports whether a jailoc process holds the start lock of the
// VM in stateDir.
func startLockHeld(stateDir string) (bool, error) {
	f, err := os.Open(startLockPath(stateDir)) //nolint:gosec // G304: jailoc-owned state path
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open start lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) //nolint:gosec // G115: file descriptors fit in int
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN):
		return true, nil
	default:
		return false, fmt.Errorf("check start lock: %w", err)
	}
}
