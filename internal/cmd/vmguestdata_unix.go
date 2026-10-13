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

// vmGuestData prepares the guest data directory of the workspace VM, its
// only internal share, and returns the binds that place its parts in the
// guest: copies of the workspace's jailoc config and cache directories
// read-only, home directories read-write unless a share covers them. The
// agent's state is persistent, or ephemeral with profile = false: start
// (a new VM, not a reload) empties it. The copies are refreshed on every
// up and reload.
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
	if err := syncDir(root, guestDataConfig, filepath.Join(config.ConfigDir(), "workspaces", ws.Name)); err != nil {
		return nil, fmt.Errorf("workspace %q: copy jailoc config: %w", ws.Name, err)
	}
	if err := syncDir(root, guestDataCache, cacheDir); err != nil {
		return nil, fmt.Errorf("workspace %q: copy jailoc cache: %w", ws.Name, err)
	}
	src := func(name string) string { return path.Join(guest.HostDir, name) }
	binds := []guest.Bind{
		{Source: src(guestDataConfig), Target: guestConfigDir, ReadOnly: true},
		{Source: src(guestDataCache), Target: guestCacheDir, ReadOnly: true},
	}
	if ws.Agent.TUIPlugin != agent.NoTUIPlugin {
		if err := root.MkdirAll(guestDataCache+"/tui-plugin", 0o700); err != nil {
			return nil, fmt.Errorf("create tui plugin dir: %w", err)
		}
		binds = append(binds, guest.Bind{Source: src(guestDataCache + "/tui-plugin"), Target: tuiPluginContainerDir, ReadOnly: true})
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
		if ws.Agent.TUIPlugin == agent.CopilotStatusLine && slices.Contains(ws.Agent.State, d) {
			addCopilotStatusLine(root, rel)
		}
		binds = append(binds, guest.Bind{Source: src(rel), Target: d.Path})
	}
	return binds, nil
}

// addCopilotStatusLine sets the jailoc status line in the settings.json of
// the GitHub Copilot CLI state directory dir inside root, unless the file
// sets a status line. Copilot reads the status line only from there. A
// failure leaves the agent without the indicator, so it is only reported.
func addCopilotStatusLine(root *os.Root, dir string) {
	if err := setDefaultStatusLine(root, path.Join(dir, "settings.json")); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "WARNING: could not add the jailoc status line to the Copilot settings: %v\n", err)
	}
}

func setDefaultStatusLine(root *os.Root, file string) error {
	settings := map[string]json.RawMessage{}
	data, err := root.ReadFile(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse %s: %w", path.Base(file), err)
		}
	}
	if _, ok := settings["statusLine"]; ok {
		return nil
	}
	line, err := json.Marshal(statusLine("copilot"))
	if err != nil {
		return err
	}
	settings["statusLine"] = line
	data, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return root.WriteFile(file, append(data, '\n'), 0o600)
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
