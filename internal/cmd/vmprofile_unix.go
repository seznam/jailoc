//go:build darwin || linux

package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// profilesDir holds the named agent profiles shared by workspaces.
func profilesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jailoc-profiles")
	}
	return filepath.Join(home, ".local", "share", "jailoc", "profiles")
}

// workspaceProfile is the named profile of the workspace's agent, or "".
func workspaceProfile(ws *workspace.Resolved) string {
	if ws.AgentProfile == nil {
		return ""
	}
	name, _ := ws.AgentProfile.Name()
	return name
}

// claimProfile records the workspace as the user of its named profile. A
// profile serves one running VM at a time: virtio-fs gives each VM its own
// page cache and file locks, so two VMs would corrupt the agent's state. A
// claim of a workspace whose VM no longer runs with the profile is stale and
// taken over.
func claimProfile(ws *workspace.Resolved) error {
	name := workspaceProfile(ws)
	if name == "" {
		return nil
	}
	return withProfileLock(name, func(f *os.File) error {
		owner, err := readProfileOwner(f)
		if err != nil {
			return err
		}
		if owner != "" && owner != ws.Name {
			held, err := profileHeld(owner, name)
			if err != nil {
				return err
			}
			if held {
				return fmt.Errorf("profile %q is in use by running workspace %q: stop it, or use another profile", name, owner)
			}
		}
		return writeProfileOwner(f, ws.Name)
	})
}

// releaseProfile drops the workspace's claim on the named profile.
func releaseProfile(wsName, name string) error {
	return withProfileLock(name, func(f *os.File) error {
		owner, err := readProfileOwner(f)
		if err != nil || owner != wsName {
			return err
		}
		return writeProfileOwner(f, "")
	})
}

// profileHeld reports whether the VM of workspace owner runs with profile
// name.
func profileHeld(owner, name string) (bool, error) {
	stateDir := vmStateDir(owner)
	st, err := runningVM(stateDir)
	if err != nil || st == nil {
		return false, err
	}
	spec, err := vm.ReadSpec(stateDir)
	if err != nil {
		return false, err
	}
	return spec.Profile == name, nil
}

// withProfileLock runs fn with the lock file of profile name open and
// exclusively locked.
func withProfileLock(name string, fn func(*os.File) error) error {
	dir := filepath.Join(profilesDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create profile %q: %w", name, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned profile path
	if err != nil {
		return fmt.Errorf("open lock of profile %q: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: file descriptors fit in int
		return fmt.Errorf("lock profile %q: %w", name, err)
	}
	return fn(f)
}

func readProfileOwner(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("read profile owner: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read profile owner: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func writeProfileOwner(f *os.File, owner string) error {
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("write profile owner: %w", err)
	}
	if owner == "" {
		return nil
	}
	if _, err := f.WriteAt([]byte(owner+"\n"), 0); err != nil {
		return fmt.Errorf("write profile owner: %w", err)
	}
	return nil
}
