//go:build darwin || linux

package cmd

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fatih/color"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// guestGitConfig is the destination of the git_config default copy.
const guestGitConfig = "/home/agent/.gitconfig"

// vmCopies returns the workspace copies: git_config as a default copy of
// ~/.gitconfig (or ~/.config/git/config), overridden by configured copies.
func vmCopies(ws *workspace.Resolved) ([]config.Mount, error) {
	var defaults []string
	if src := resolveGitConfig(ws.GitConfig); src != "" {
		defaults = append(defaults, src+":"+guestGitConfig+":ro")
	}
	merged, err := config.MergeMounts(defaults, ws.Copies)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: merge copies: %w", ws.Name, err)
	}
	copies := make([]config.Mount, 0, len(merged))
	for _, spec := range merged {
		m, err := config.ParseMount(spec)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", ws.Name, err)
		}
		host, err := config.ExpandPath(m.Host)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: expand copy source %q: %w", ws.Name, m.Host, err)
		}
		m.Host = host
		m.Container = filepath.Clean(m.Container)
		copies = append(copies, m)
	}
	return copies, nil
}

// checkCopies rejects copies whose destination overlaps a shared path:
// binding into a share would create a mount point on the host, and binding
// over a share's parent would hide the share.
func checkCopies(ws *workspace.Resolved, copies []config.Mount, shares []vm.Share) error {
	for _, c := range copies {
		if overlaps(c.Container, guest.Dir) {
			return fmt.Errorf("workspace %q: copy destination %q is reserved by jailoc", ws.Name, c.Container)
		}
		for _, s := range shares {
			if p := s.GuestPath; p != "" && overlaps(c.Container, p) {
				return fmt.Errorf("workspace %q: copy destination %q overlaps the shared path %q", ws.Name, c.Container, p)
			}
		}
	}
	return nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// stageCopies snapshots every copy source into stateDir/copies, which is
// shared read-only with the VM, and returns the guest bind list.
func stageCopies(stateDir string, copies []config.Mount) (string, []guest.Copy, error) {
	dir := filepath.Join(stateDir, "copies")
	if err := os.RemoveAll(dir); err != nil {
		return "", nil, fmt.Errorf("clear staged copies: %w", err)
	}
	if len(copies) == 0 {
		return "", nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create staged copies: %w", err)
	}
	binds := make([]guest.Copy, 0, len(copies))
	for i, c := range copies {
		name := strconv.Itoa(i)
		if err := copyTree(c.Host, filepath.Join(dir, name)); err != nil {
			return "", nil, fmt.Errorf("copy %q: %w", c.Host, err)
		}
		binds = append(binds, guest.Copy{Source: guest.CopiesDir + "/" + name, Target: c.Container})
	}
	return dir, binds, nil
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
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "warning: copy skips %s (not a regular file, directory or symlink)\n", path)
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
