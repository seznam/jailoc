//go:build darwin || linux

package cmd

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// guestGitConfig is the destination of the git_config default snapshot.
const guestGitConfig = "/home/agent/.gitconfig"

// guestKnownHosts is the destination of the known_hosts default snapshot that
// accompanies the SSH agent.
const guestKnownHosts = "/home/agent/.ssh/known_hosts"

// vmSnapshots returns the workspace snapshots: git_config as a default snapshot
// of ~/.gitconfig (or ~/.config/git/config), overridden by configured snapshots.
func vmSnapshots(ws *workspace.Resolved) ([]config.Mount, error) {
	var defaults []string
	if src := resolveGitConfig(ws.GitConfig); src != "" {
		defaults = append(defaults, src+":"+guestGitConfig+":ro")
	}
	if ws.SSHAuthSock {
		if home, err := os.UserHomeDir(); err == nil {
			if src := filepath.Join(home, ".ssh", "known_hosts"); isRegularFile(src) {
				defaults = append(defaults, src+":"+guestKnownHosts+":ro")
			}
		}
	}
	merged, err := config.MergeMounts(defaults, ws.Snapshots)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: merge snapshots: %w", ws.Name, err)
	}
	snapshots := make([]config.Mount, 0, len(merged))
	for _, spec := range merged {
		m, err := config.ParseMount(spec)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
		host, err := config.ExpandPath(m.Host)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: expand snapshot source %q: %w", ws.Name, m.Host, err)
		}
		m.Host = host
		m.Container = filepath.Clean(m.Container)
		snapshots = append(snapshots, m)
	}
	return snapshots, nil
}

// checkSnapshots rejects snapshots whose destination overlaps a shared path:
// binding into a share would create a mount point on the host, and binding
// over a share's parent would hide the share.
func checkSnapshots(ws *workspace.Resolved, snapshots []config.Mount, shares []vm.Share) error {
	for _, c := range snapshots {
		if overlaps(c.Container, guest.Dir) {
			return fmt.Errorf("workspace %q: snapshot destination %q is reserved by jailoc", ws.Name, c.Container)
		}
		for _, s := range shares {
			if p := s.GuestPath; p != "" && overlaps(c.Container, p) {
				return fmt.Errorf("workspace %q: snapshot destination %q overlaps the shared path %q", ws.Name, c.Container, p)
			}
		}
	}
	return nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// snapshotsShare is the read-only share of the staged snapshots. Every
// workspace VM has it, so a reload can add snapshots.
func snapshotsShare(stateDir string) vm.Share {
	return vm.Share{HostPath: filepath.Join(stateDir, "snapshots"), GuestPath: guest.SnapshotsDir, ReadOnly: true}
}

// stageSnapshots copies every snapshot source into a new generation directory
// under stateDir/snapshots and returns the guest bind list. Generations other
// than keep, the one the running guest binds, are removed. A new directory
// per generation keeps the guest from reading stale virtio-fs entries.
func stageSnapshots(stateDir string, snapshots []config.Mount, keep string) ([]guest.Snapshot, error) {
	dir := snapshotsShare(stateDir).HostPath
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create staged snapshots: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read staged snapshots: %w", err)
	}
	for _, e := range entries {
		if e.Name() != keep {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return nil, fmt.Errorf("clear staged snapshots: %w", err)
			}
		}
	}
	if len(snapshots) == 0 {
		return nil, nil
	}
	gen := strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.Mkdir(filepath.Join(dir, gen), 0o700); err != nil {
		return nil, fmt.Errorf("create staged snapshots: %w", err)
	}
	binds := make([]guest.Snapshot, 0, len(snapshots))
	for i, c := range snapshots {
		name := strconv.Itoa(i)
		if err := copyTree(c.Host, filepath.Join(dir, gen, name)); err != nil {
			_ = os.RemoveAll(filepath.Join(dir, gen))
			return nil, fmt.Errorf("snapshot %q: %w", c.Host, err)
		}
		binds = append(binds, guest.Snapshot{Source: path.Join(guest.SnapshotsDir, gen, name), Target: c.Container})
	}
	return binds, nil
}

// snapshotGeneration returns the staging generation that binds come from.
func snapshotGeneration(binds []guest.Snapshot) string {
	if len(binds) == 0 {
		return ""
	}
	rel := strings.TrimPrefix(binds[0].Source, guest.SnapshotsDir+"/")
	gen, _, _ := strings.Cut(rel, "/")
	return gen
}

// copyTree copies a file or directory. Symbolic links inside a directory
// are copied as links; sockets, devices and pipes are skipped.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyRegular(src, dst, info.Mode().Perm())
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target) //nolint:gosec // G122: link is copied verbatim into the staging directory and resolved only inside the guest
		case d.Type().IsRegular():
			return copyRegular(path, target, info.Mode().Perm())
		default:
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "warning: snapshot skips %s (not a regular file, directory or symlink)\n", path)
			return nil
		}
	})
}

func copyRegular(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // G304: user-configured copy source
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm|0o400) //nolint:gosec // G304: inside jailoc's staging directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
