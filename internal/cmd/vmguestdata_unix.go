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

	"github.com/fatih/color"

	"github.com/seznam/jailoc/internal/agent"
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
// home directories read-write from the guest data directory unless a share
// covers them. The agent's state is persistent, or ephemeral with
// profile = false: start (a new VM, not a reload) empties it. The copies are
// refreshed on every up and reload.
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
	src := func(name string) string { return path.Join(guest.HostDir, name) }
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
	if ephemeral && start {
		if err := root.RemoveAll(guestDataEphemeral); err != nil {
			return nil, fmt.Errorf("empty ephemeral agent state: %w", err)
		}
	}
	for _, d := range persistentHomeDirs(ws.Agent) {
		if slices.ContainsFunc(shares, func(s vm.Share) bool { return s.GuestPath == d.Path }) {
			continue
		}
		rel := guestDataHome + "/" + d.Name
		if ephemeral && slices.Contains(ws.Agent.State, d) {
			rel = guestDataEphemeral + "/" + d.Name
		}
		if err := root.MkdirAll(rel, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", rel, err)
		}
		if start {
			if err := removeConfigMountPoints(root, rel, d, ws.Agent); err != nil {
				return nil, fmt.Errorf("workspace %q: %w", ws.Name, err)
			}
		}
		if slices.Contains(ws.Agent.State, d) {
			addStatusLine(root, rel, ws.Agent)
		}
		binds = append(binds, guest.Bind{Source: src(rel), Target: d.Path})
	}
	return binds, nil
}

// removeConfigMountPoints removes the empty files and directories the guest
// created in state directory d, at dir inside root, as mount points of
// import_agent_config snapshots. A snapshot that no longer applies, such as one
// turned off or one whose host file is gone, would otherwise leave the agent
// an empty settings file. The guest creates them again for the snapshots it
// binds.
func removeConfigMountPoints(root *os.Root, dir string, d agent.StateDir, h agent.Harness) error {
	for _, cf := range h.ConfigFiles {
		inner, ok := strings.CutPrefix(guestHome+"/"+cf.Path, d.Path+"/")
		if !ok {
			continue
		}
		p := path.Join(dir, inner)
		info, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", p, err)
		}
		switch {
		case info.Mode().IsRegular() && info.Size() == 0:
		case info.IsDir():
			entries, err := fs.ReadDir(root.FS(), p)
			if err != nil {
				return fmt.Errorf("read %s: %w", p, err)
			}
			if len(entries) > 0 {
				continue
			}
		default:
			continue
		}
		if err := root.Remove(p); err != nil {
			return fmt.Errorf("remove mount point %s: %w", p, err)
		}
	}
	return nil
}

// addStatusLine sets the jailoc defaults in the settings file of agent h in
// its state directory dir inside root: the status line (unless the file sets
// one other than the script status line of an older jailoc) and, for GitHub
// Copilot CLI, storeTokenPlaintext (when unset) so logins work without a
// system keyring in the VM. A failure leaves the agent without the defaults,
// so it is only reported.
func addStatusLine(root *os.Root, dir string, h agent.Harness) {
	file, harness, ok := h.TUIPlugin.SettingsFile()
	if !ok {
		return
	}
	if err := setDefaultStatusLine(root, path.Join(dir, file), harness); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: could not add the jailoc status line to the %s settings: %v\n", h.Title, err)
	}
}

func setDefaultStatusLine(root *os.Root, file, harness string) error {
	settings := map[string]json.RawMessage{}
	data, err := readRegular(root, file, maxAgentConfig)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse %s: %w", path.Base(file), err)
		}
	}
	changed := false
	if old, ok := settings["statusLine"]; !ok || isLegacyStatusLine(old, harness) {
		line, err := json.Marshal(statusLine(harness))
		if err != nil {
			return err
		}
		settings["statusLine"] = line
		changed = true
	}
	if harness == "copilot" {
		if _, ok := settings["storeTokenPlaintext"]; !ok {
			settings["storeTokenPlaintext"] = json.RawMessage("true")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	data, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := root.MkdirAll(path.Dir(file), 0o700); err != nil {
		return err
	}
	return root.WriteFile(file, append(data, '\n'), 0o600)
}

func isLegacyStatusLine(raw json.RawMessage, harness string) bool {
	var line struct{ Command string }
	return json.Unmarshal(raw, &line) == nil && line.Command == scriptStatusLineCommand(harness)
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
