//go:build darwin || linux

package cmd

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// guestGitConfig is the destination of the ~/.gitconfig default snapshot.
const guestGitConfig = guestHome + "/.gitconfig"

// guestKnownHosts is the destination of the known_hosts default snapshot that
// accompanies the SSH agent.
const guestKnownHosts = guestHome + "/.ssh/known_hosts"

// snapshot is a copy of a host file or directory bound read-only into the
// guest. When data is not nil it is the content of the copy, generated from
// the host file.
type snapshot struct {
	config.Mount
	data []byte
}

// vmSnapshots returns the workspace snapshots: the Git configuration
// (git_config, see gitConfigSnapshots) and known_hosts (ssh_auth_sock) as
// default snapshots, overridden by configured snapshots.
func vmSnapshots(ws *workspace.Resolved) ([]snapshot, error) {
	var defaults []snapshot
	home, homeErr := os.UserHomeDir()
	if ws.GitConfig && homeErr == nil {
		defaults = gitConfigSnapshots(home, os.Getenv("XDG_CONFIG_HOME"), func(format string, args ...any) {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "warning: workspace %q: git_config: "+format+"\n", append([]any{ws.Name}, args...)...)
		})
	}
	if ws.SSHAuthSock && homeErr == nil {
		if src := filepath.Join(home, ".ssh", "known_hosts"); isRegularFile(src) {
			defaults = append(defaults, snapshot{Mount: mountOf(src, guestKnownHosts)})
		}
	}
	specs := make([]string, 0, len(defaults))
	byDest := make(map[string]snapshot, len(defaults))
	for _, d := range defaults {
		specs = append(specs, d.Host+":"+d.Container+":ro")
		byDest[d.Container] = d
	}
	merged, err := config.MergeMounts(specs, ws.Snapshots)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: merge snapshots: %w", ws.Name, err)
	}
	// A configured snapshot replaces the default with the same destination even
	// when it names the same source.
	explicit := make(map[string]bool, len(ws.Snapshots))
	for _, spec := range ws.Snapshots {
		if m, err := config.ParseMount(spec); err == nil {
			explicit[filepath.Clean(m.Container)] = true
		}
	}
	snapshots := make([]snapshot, 0, len(merged))
	var configured []string
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
		if d, ok := byDest[m.Container]; ok && !explicit[m.Container] {
			snapshots = append(snapshots, d)
			continue
		}
		snapshots = append(snapshots, snapshot{Mount: m})
		configured = append(configured, m.Container)
	}
	// A configured snapshot of a directory, such as ~/.config/git, replaces
	// the default snapshots inside it.
	return slices.DeleteFunc(snapshots, func(s snapshot) bool {
		_, ok := byDest[s.Container]
		return ok && !explicit[s.Container] && slices.ContainsFunc(configured, func(c string) bool {
			return c != s.Container && overlaps(c, s.Container)
		})
	}), nil
}

// checkSnapshots rejects snapshots whose destination overlaps a shared path:
// binding into a share would create a mount point on the host, and binding
// over a share's parent would hide the share.
func checkSnapshots(ws *workspace.Resolved, snapshots []snapshot, shares []vm.Share) error {
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

// snapshotsDir is the directory of the staged snapshots inside the guest
// data directory.
const snapshotsDir = "snapshots"

// stageSnapshots copies every snapshot source into a new generation directory
// of the staged snapshots in the read-only guest data directory and returns
// the guest bind list. Generations other than keep, the one the running
// guest binds, are removed. A new directory per generation keeps the guest
// from reading stale virtio-fs entries. Every write goes through an os.Root
// that does not follow links out of the directory.
func stageSnapshots(stateDir string, snapshots []snapshot, keep string) ([]guest.Snapshot, error) {
	root, err := vm.OpenGuestReadOnly(stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(snapshotsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create staged snapshots: %w", err)
	}
	entries, err := fs.ReadDir(root.FS(), snapshotsDir)
	if err != nil {
		return nil, fmt.Errorf("read staged snapshots: %w", err)
	}
	for _, e := range entries {
		if e.Name() != keep {
			if err := root.RemoveAll(path.Join(snapshotsDir, e.Name())); err != nil {
				return nil, fmt.Errorf("clear staged snapshots: %w", err)
			}
		}
	}
	if len(snapshots) == 0 {
		return nil, nil
	}
	gen := strconv.FormatInt(time.Now().UnixNano(), 10)
	genDir := path.Join(snapshotsDir, gen)
	if err := root.Mkdir(genDir, 0o700); err != nil {
		return nil, fmt.Errorf("create staged snapshots: %w", err)
	}
	binds := make([]guest.Snapshot, 0, len(snapshots))
	for i, c := range snapshots {
		dst := path.Join(genDir, strconv.Itoa(i))
		if err := stageSnapshot(root, c, dst); err != nil {
			_ = root.RemoveAll(genDir)
			return nil, fmt.Errorf("snapshot %q: %w", c.Host, err)
		}
		binds = append(binds, guest.Snapshot{Source: path.Join(guest.SnapshotsDir, gen, strconv.Itoa(i)), Target: c.Container})
	}
	return binds, nil
}

// stageSnapshot writes the generated content of c, or copies its source, to
// dst inside root.
func stageSnapshot(root *os.Root, c snapshot, dst string) error {
	if c.data == nil {
		return copyTree(root, c.Host, dst)
	}
	out, err := root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := out.Write(c.data); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
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

// copyTree copies a host file or directory to dst inside root. Symbolic
// links inside a directory are copied as links; sockets, devices and pipes
// are skipped.
func copyTree(root *os.Root, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyRegular(root, src, dst, info.Mode().Perm())
	}
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolve snapshot directory %q: %w", src, err)
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := path.Join(dst, filepath.ToSlash(rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return root.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return root.Symlink(link, target)
		case d.Type().IsRegular():
			return copyRegular(root, p, target, info.Mode().Perm())
		default:
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "warning: snapshot skips %s (not a regular file, directory or symlink)\n", p)
			return nil
		}
	})
}

func copyRegular(root *os.Root, src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // G304: user-configured copy source
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm|0o400)
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
	info, err := os.Stat(path) //nolint:gosec // G703: user-configured copy source
	return err == nil && info.Mode().IsRegular()
}
