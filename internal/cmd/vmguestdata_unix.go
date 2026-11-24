//go:build darwin || linux

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/fatih/color"
	goversion "github.com/hashicorp/go-version"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/agentstate"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// Directories inside the guest data directory of a workspace VM.
const (
	guestDataConfig    = "config"    // copy of the workspace's jailoc config dir
	guestDataCache     = "cache"     // copy of the workspace's jailoc cache dir
	guestDataHome      = "home"      // persistent home directories
	guestDataEphemeral = "ephemeral" // agent state of profile = false, emptied on VM start
)

// persistentHomeDirs survive restarts: the state of the workspace's agent
// (sessions, logins, history) and caches.
func persistentHomeDirs(h agent.Harness) []agent.StateDir {
	return append(slices.Clone(h.State), agent.StateDir{Name: "cache", Path: guestUserCache})
}

// vmGuestData prepares the internal shares of the workspace VM and returns
// the binds that place their parts in the guest: copies of the workspace's
// jailoc config and cache directories read-only from the read-only share,
// home directories read-write from the state and cache disks unless a share
// covers them. The agent's state is persistent, or ephemeral with
// profile = false: the guest empties it at boot. The copies are refreshed on
// every up and reload.
func vmGuestData(ws *workspace.Resolved, cacheDir string, shares []vm.Share, start bool) ([]guest.Bind, error) {
	stateDir := vmStateDir(ws.Name)
	if err := migrateHomeDirs(stateDir); err != nil {
		return nil, err
	}
	root, err := vm.OpenGuestData(stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if start {
		// Copies of an older layout kept them in the read-write share.
		for _, name := range []string{guestDataConfig, guestDataCache, snapshotsDir} {
			if err := root.RemoveAll(name); err != nil {
				return nil, fmt.Errorf("remove %s of an older layout: %w", name, err)
			}
		}
	}
	ro, err := vm.OpenGuestReadOnly(stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = ro.Close() }()
	if err := syncDir(ro, guestDataConfig, filepath.Join(config.ConfigDir(), "workspaces", ws.Name)); err != nil {
		return nil, fmt.Errorf("workspace %q: copy jailoc config: %w", ws.Name, err)
	}
	if err := syncDir(ro, guestDataCache, cacheDir); err != nil {
		return nil, fmt.Errorf("workspace %q: copy jailoc cache: %w", ws.Name, err)
	}
	roSrc := func(name string) string { return path.Join(guest.ReadOnlyHostDir, name) }
	binds := []guest.Bind{
		{Source: roSrc(guestDataConfig), Target: guestConfigDir, ReadOnly: true},
		{Source: roSrc(guestDataCache), Target: guestCacheDir, ReadOnly: true},
	}
	if ws.Agent.TUIPlugin != agent.NoTUIPlugin {
		if err := ro.MkdirAll(guestDataCache+"/tui-plugin", 0o700); err != nil {
			return nil, fmt.Errorf("create tui plugin dir: %w", err)
		}
		binds = append(binds, guest.Bind{Source: roSrc(guestDataCache + "/tui-plugin"), Target: tuiPluginContainerDir, ReadOnly: true})
	}
	ephemeral := ws.AgentProfile != nil && ws.AgentProfile.Ephemeral()
	if start {
		// An older layout kept the ephemeral agent state here; the state
		// disk holds it now and the guest empties it.
		if err := root.RemoveAll(guestDataEphemeral); err != nil {
			return nil, fmt.Errorf("remove ephemeral agent state of an older layout: %w", err)
		}
	}
	for _, d := range persistentHomeDirs(ws.Agent) {
		if i := slices.IndexFunc(shares, func(s vm.Share) bool { return s.GuestPath == d.Path }); i >= 0 {
			// The VM claimed a named profile before this start; other
			// shares belong to the user.
			if name := workspaceProfile(ws); start && name != "" && shares[i].HostPath == filepath.Join(profilesDir(), name, d.Name) {
				addProfileStateDefaults(shares[i].HostPath, d, ws.Agent)
			}
			continue
		}
		// The disks hold the rest; the guest prepares the directories.
		switch {
		case d.Path == guestUserCache:
			binds = append(binds, guest.Bind{Source: path.Join(guest.CacheDiskDir, d.Name), Target: d.Path})
		case ephemeral:
			binds = append(binds, guest.Bind{Source: path.Join(guest.StateDiskDir, guestDataEphemeral, d.Name), Target: d.Path})
		default:
			binds = append(binds, guest.Bind{Source: path.Join(guest.StateDiskDir, d.Name), Target: d.Path})
		}
	}
	return binds, nil
}

// agentVersionsFile records, outside the directories shared with the guest,
// the highest version of each host backing state directory that jailoc started
// a workspace with.
const (
	agentVersionsFile = "agent-versions.json"
	agentVersionsLock = "agent-versions.lock"
)

func agentVersionsPath() string {
	return filepath.Join(vmRootDir(), agentVersionsFile)
}

func withAgentVersionsLock(fn func(file string)) {
	dir := vmRootDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	lock, err := os.OpenFile(filepath.Join(dir, agentVersionsLock), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned lock path
	if err != nil {
		return
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: file descriptors fit in int
		return
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }() //nolint:gosec // G115: file descriptors fit in int
	fn(filepath.Join(dir, agentVersionsFile))
}

func canonicalHostPath(p string) string {
	clean := filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		return real
	}
	return clean
}

// installsConfiguredAgent reports whether the image strategy for ws installs
// the configured ws.Agent.Version rather than using a pre-built image directly.
func installsConfiguredAgent(cfg *config.Config, ws *workspace.Resolved) bool {
	if !ws.AgentInstall || ws.Agent.Version == "" || len(ws.Agent.State) == 0 {
		return false
	}
	if cfg != nil {
		strategy, _ := resolveImageStrategy(ws.Image, cfg.Defaults.Image, ws.Dockerfile)
		if strategy == strategyDirectImage || strategy == strategyDefaultsDirect {
			return false
		}
	}
	return true
}

// agentStateHostDir returns the canonical host directory that backs the
// agent's primary state directory for ws: an explicit share covering it, or
// the workspace's state disk image with the directory name, which stays
// inside the state directory purgeWorkspaceAgentStateVersions cleans.
func agentStateHostDir(cfg *config.Config, ws *workspace.Resolved, shares []vm.Share) string {
	if !installsConfiguredAgent(cfg, ws) {
		return ""
	}
	d := ws.Agent.State[0]
	for _, s := range shares {
		if s.GuestPath == d.Path {
			return canonicalHostPath(s.HostPath)
		}
	}
	if ws.AgentProfile != nil && ws.AgentProfile.Ephemeral() {
		return ""
	}
	return canonicalHostPath(vm.StateDiskPath(vmStateDir(ws.Name))) + "#" + d.Name
}

func loadAgentVersions(file string) map[string]string {
	var versions map[string]string
	if data, err := os.ReadFile(file); err == nil { //nolint:gosec // G304: jailoc-owned metadata path
		_ = json.Unmarshal(data, &versions)
	}
	if versions == nil {
		versions = map[string]string{}
	}
	return versions
}

func saveAgentVersions(file string, versions map[string]string) {
	data, err := json.MarshalIndent(versions, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "agent-versions-*.json")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, file); err != nil {
		_ = os.Remove(tmpName)
	}
}

// checkAgentStateVersion warns when the backing host directory of ws's agent
// state was previously used with a newer version of the agent, which may have
// migrated its state database to a schema the configured version cannot read.
func checkAgentStateVersion(cfg *config.Config, ws *workspace.Resolved, shares []vm.Share) {
	hostDir := agentStateHostDir(cfg, ws, shares)
	if hostDir == "" {
		return
	}
	curV, err := goversion.NewVersion(ws.Agent.Version)
	if err != nil {
		return
	}
	withAgentVersionsLock(func(file string) {
		versions := loadAgentVersions(file)
		if prev := versions[hostDir]; prev != "" && prev != ws.Agent.Version {
			if prevV, err := goversion.NewVersion(prev); err == nil && curV.LessThan(prevV) {
				_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: workspace %q: %s state was used with newer version %s (current: %s); if the agent fails to start, upgrade it or reset its state\n", ws.Name, ws.Agent.Title, prev, ws.Agent.Version)
			}
		}
	})
}

// recordAgentStateVersion records ws's agent version as the highest version
// that ran against its backing state directory after the workspace VM starts.
func recordAgentStateVersion(cfg *config.Config, ws *workspace.Resolved, shares []vm.Share) {
	hostDir := agentStateHostDir(cfg, ws, shares)
	if hostDir == "" {
		return
	}
	curV, err := goversion.NewVersion(ws.Agent.Version)
	if err != nil {
		return
	}
	withAgentVersionsLock(func(file string) {
		versions := loadAgentVersions(file)
		if prev := versions[hostDir]; prev != "" {
			if prev == ws.Agent.Version {
				return
			}
			if prevV, err := goversion.NewVersion(prev); err == nil && !curV.GreaterThan(prevV) {
				return
			}
		}
		versions[hostDir] = ws.Agent.Version
		saveAgentVersions(file, versions)
	})
}

// purgeWorkspaceAgentStateVersions removes recorded agent version entries for
// state directories inside stateDir when the workspace VM is purged.
func purgeWorkspaceAgentStateVersions(stateDir string) {
	prefix := canonicalHostPath(stateDir) + string(filepath.Separator)
	rawPrefix := filepath.Clean(stateDir) + string(filepath.Separator)
	withAgentVersionsLock(func(file string) {
		versions := loadAgentVersions(file)
		changed := false
		for k := range versions {
			if strings.HasPrefix(k, prefix) || strings.HasPrefix(k, rawPrefix) {
				delete(versions, k)
				changed = true
			}
		}
		if changed {
			saveAgentVersions(file, versions)
		}
	})
}

// removeConfigMountPoints removes the empty mount points the guest created for
// import_agent_config snapshots in state directory d, at dir inside root.
func removeConfigMountPoints(root *os.Root, dir string, d agent.StateDir, h agent.Harness) error {
	return agentstate.RemoveConfigMountPoints(root, dir, d, h)
}

// addStateDefaults sets the StateDefaults of agent h that are in state
// directory d, at dir inside root. A failure leaves the agent without the
// defaults, so it is only reported.
func addStateDefaults(root *os.Root, dir string, d agent.StateDir, h agent.Harness) {
	for _, err := range agentstate.AddStateDefaults(root, dir, d, h) {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: %v\n", err)
	}
}

// addProfileStateDefaults sets the StateDefaults in state directory d of a
// named profile, at host directory dir.
func addProfileStateDefaults(dir string, d agent.StateDir, h agent.Harness) {
	if len(h.StateDefaults) == 0 {
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: could not open profile directory %s: %v\n", dir, err)
		return
	}
	defer func() { _ = root.Close() }() // directory handle
	addStateDefaults(root, ".", d, h)
}

// addStatusLine sets the jailoc defaults in the settings file of agent h in
// its state directory dir inside root. A failure leaves the agent without the
// defaults, so it is only reported.
func addStatusLine(root *os.Root, dir string, h agent.Harness) {
	if err := agentstate.AddStatusLine(root, dir, h); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: could not %v\n", err)
	}
}

// workspaceStorageDisks returns the names of the agent state directories the
// state disk holds for ws and whether the cache disk holds ~/.cache: those
// that no share covers.
func workspaceStorageDisks(ws *workspace.Resolved, shares []vm.Share) (stateDirs []string, cache bool) {
	covered := func(guestPath string) bool {
		return slices.ContainsFunc(shares, func(s vm.Share) bool { return filepath.Clean(s.GuestPath) == filepath.Clean(guestPath) })
	}
	for _, d := range ws.Agent.State {
		if !covered(d.Path) {
			stateDirs = append(stateDirs, d.Name)
		}
	}
	return stateDirs, !covered(guestUserCache)
}

// legacyHomeName is the directory next to the guest data directory that
// keeps the persistent home directories of an older layout once the state
// and cache disks hold their content.
const legacyHomeName = "legacy-home"

// archiveLegacyHome moves the persistent home directories of an older layout
// (<state>/guest/home) to <state>/legacy-home once the guest has copied them
// to the state disk (it leaves the home-migrated marker in the control
// directory), and tells the user. jailoc never deletes the archive. It does
// nothing when there is no legacy directory or no marker.
func archiveLegacyHome(stateDir, wsName string) error {
	data := vm.GuestDataDir(stateDir)
	src := filepath.Join(data, guestDataHome)
	if info, err := os.Lstat(src); err != nil || !info.IsDir() {
		return nil
	}
	marker := filepath.Join(data, vm.ControlName, "home-migrated")
	if _, err := os.Lstat(marker); err != nil {
		return nil
	}
	dst := filepath.Join(stateDir, legacyHomeName)
	for n := 1; ; n++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return fmt.Errorf("inspect %s: %w", dst, err)
		}
		dst = filepath.Join(stateDir, fmt.Sprintf("%s-%d", legacyHomeName, n))
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("archive legacy home directories: %w", err)
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove migration marker: %w", err)
	}
	_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "Workspace %s: agent state and cache moved to state.img and cache.img; the previous files are kept at %s and can be deleted once you no longer need them\n", wsName, dst)
	return nil
}

// migrateHomeDirs moves persistent home directories of an older layout
// (<state>/home) into the guest data directory and removes the other
// directories of that layout.
func migrateHomeDirs(stateDir string) error {
	// Directories of the older layout that moved into the guest data
	// directory; their content is recreated on every start.
	for _, name := range []string{"control", "snapshots", "copies"} {
		if err := os.RemoveAll(filepath.Join(stateDir, name)); err != nil {
			return fmt.Errorf("remove %s of an older layout: %w", name, err)
		}
	}
	old := filepath.Join(stateDir, guestDataHome)
	if _, err := os.Lstat(old); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	dst := filepath.Join(vm.GuestDataDir(stateDir), guestDataHome)
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(vm.GuestDataDir(stateDir), 0o700); err != nil {
		return fmt.Errorf("create guest data dir: %w", err)
	}
	if err := os.Rename(old, dst); err != nil {
		return fmt.Errorf("move persistent home directories: %w", err)
	}
	return nil
}

// syncDir makes dst inside root a copy of the host directory src: regular
// files and directories are copied, everything else in src is ignored, and
// entries of dst that src lacks are removed. root confines every write, so
// links the guest plants in dst cannot redirect it to other host files.
// A missing src leaves an empty dst.
func syncDir(root *os.Root, dst, src string) error {
	if err := root.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	want := map[string]bool{}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == src && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		target := path.Join(dst, filepath.ToSlash(rel))
		switch {
		case d.IsDir():
			want[target] = true
			if info, err := root.Lstat(target); err == nil && !info.IsDir() {
				if err := root.RemoveAll(target); err != nil {
					return err
				}
			}
			return root.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			want[target] = true
			data, err := os.ReadFile(p) //nolint:gosec // G304: jailoc-owned config and cache files
			if err != nil {
				return err
			}
			if info, err := root.Lstat(target); err == nil && !info.Mode().IsRegular() {
				if err := root.RemoveAll(target); err != nil {
					return err
				}
			} else if old, err := root.ReadFile(target); err == nil && bytes.Equal(old, data) {
				return nil
			}
			// Rewrite in place: the guest sees the new content through
			// its existing bind without a new directory entry.
			return root.WriteFile(target, data, 0o600)
		default:
			return nil
		}
	})
	if err != nil {
		return err
	}
	var stale []string
	_ = fs.WalkDir(root.FS(), dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dst {
			return nil
		}
		if !want[p] {
			stale = append(stale, p)
			if d.IsDir() {
				return fs.SkipDir
			}
		}
		return nil
	})
	for _, p := range stale {
		if err := root.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}
