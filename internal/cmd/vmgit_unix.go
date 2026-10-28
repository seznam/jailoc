//go:build darwin || linux

package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// maxGitScanDirs bounds the directory walk that looks for Git repositories
// in the workspace paths.
const maxGitScanDirs = 500_000

// gitScanSkip are directory names that never contain repositories worth
// protecting and are expensive to walk.
var gitScanSkip = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	".tox": true, ".terraform": true, ".next": true, ".gradle": true, ".cache": true,
}

// gitProtection finds the Git repositories under the workspace paths and
// returns the guest paths jailoc-guest protects: read-only binds over every
// hooks directory, config file and .git file, and pins on every git
// directory, repository and directory between a workspace path and a
// repository, so that the agent cannot swap a protected git directory for
// its own by renaming. Git directories that a .git file or a worktree's
// commondir points at (separate git directories, worktree and submodule
// metadata) are protected the same way when they are under a workspace
// path; others are not shared with the VM. Workspace paths are shared at
// their host paths, so host and guest paths are the same. Missing hooks
// directories are created, because the agent could otherwise create one
// with its own hooks.
func gitProtection(paths []string) (pins, readOnly []string, err error) {
	p := gitProtector{pins: map[string]bool{}, ro: map[string]bool{}, done: map[string]bool{}}
	for _, root := range paths {
		root = filepath.Clean(root)
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, nil, fmt.Errorf("scan %s for git repositories: %w", root, err)
		}
		p.roots = append(p.roots, shareRoot{path: root, real: resolved})
	}
	scanned := 0
	for _, r := range p.roots {
		root := r.path
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return err
				}
				return nil // unreadable subtree: nothing to protect there
			}
			if d.Name() == ".git" && path != root {
				if err := p.repo(root, path, d); err != nil {
					return err
				}
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if path != root && gitScanSkip[d.Name()] {
				return fs.SkipDir
			}
			scanned++
			if scanned > maxGitScanDirs {
				return fmt.Errorf("more than %d directories under the workspace paths; protect_git needs narrower paths", maxGitScanDirs)
			}
			return nil
		})
		if walkErr != nil {
			return nil, nil, fmt.Errorf("scan %s for git repositories: %w", root, walkErr)
		}
	}
	return sortedKeys(p.pins), sortedKeys(p.ro), nil
}

// shareRoot is a workspace path and its symlink-free host path.
type shareRoot struct{ path, real string }

// gitProtector collects the protections of the repositories under roots.
type gitProtector struct {
	roots []shareRoot
	pins  map[string]bool
	ro    map[string]bool
	done  map[string]bool // git directories already protected
}

// repo records the protections of the repository whose .git entry under
// root is gitPath.
func (p *gitProtector) repo(root, gitPath string, d fs.DirEntry) error {
	p.pinUpTo(filepath.Dir(gitPath), root)
	if d.IsDir() {
		return p.gitDir(root, gitPath)
	}
	// A gitfile (separate git directory, worktree or submodule checkout)
	// points at the real git directory; keep the pointer itself unchanged
	// and protect what it points at.
	p.ro[gitPath] = true
	if !d.Type().IsRegular() {
		return nil
	}
	content, ok := readPointer(gitPath)
	target, isGitfile := strings.CutPrefix(content, "gitdir:")
	if !ok || !isGitfile {
		return nil
	}
	return p.linked(resolvePointer(filepath.Dir(gitPath), strings.TrimSpace(target)))
}

// linked protects the git directory target, reached through a pointer,
// when it is under a workspace path.
func (p *gitProtector) linked(target string) error {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil //nolint:nilerr // dangling pointer: nothing to protect
	}
	for _, r := range p.roots {
		rel, err := filepath.Rel(r.real, resolved)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		dir := filepath.Join(r.path, rel)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return nil //nolint:nilerr // not a git directory
		}
		return p.gitDir(r.path, dir)
	}
	return nil
}

// gitDir pins dir and the directories between it and root, and marks its
// hooks and config read-only. A worktree git directory takes hooks and
// config from its common directory, which is protected instead. Submodule
// git directories under dir/modules are protected the same way.
func (p *gitProtector) gitDir(root, dir string) error {
	if p.done[dir] {
		return nil
	}
	p.done[dir] = true
	p.pinUpTo(dir, root)
	if common, ok := readPointer(filepath.Join(dir, "commondir")); ok {
		p.readOnlyFile(filepath.Join(dir, "config.worktree"))
		return p.linked(resolvePointer(dir, common))
	}
	hooks := filepath.Join(dir, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil && !errors.Is(err, fs.ErrExist) { //nolint:gosec // G301: matches the hooks directory git creates
		return fmt.Errorf("create %s: %w", hooks, err)
	}
	p.ro[hooks] = true
	p.readOnlyFile(filepath.Join(dir, "config"))
	modules := filepath.Join(dir, "modules")
	if _, err := os.Stat(modules); err != nil {
		return nil //nolint:nilerr // no submodules
	}
	return filepath.WalkDir(modules, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == modules {
			return nil //nolint:nilerr // unreadable entries hold nothing to protect
		}
		if _, statErr := os.Stat(filepath.Join(path, "HEAD")); statErr != nil {
			return nil
		}
		if err := p.gitDir(dir, path); err != nil {
			return err
		}
		return fs.SkipDir
	})
}

// pinUpTo pins dir and its parents below root.
func (p *gitProtector) pinUpTo(dir, root string) {
	for d := dir; d != root && d != filepath.Dir(d); d = filepath.Dir(d) {
		p.pins[d] = true
	}
}

// readOnlyFile marks path read-only if it is a regular file.
func (p *gitProtector) readOnlyFile(path string) {
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		p.ro[path] = true
	}
}

// maxPointerSize bounds the gitfile and commondir files jailoc reads.
const maxPointerSize = 4096

// readPointer returns the first line of the small regular file path, such
// as a gitfile or a commondir file.
func readPointer(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPointerSize {
		return "", false
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is inside a workspace path
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	return line, line != ""
}

// resolvePointer resolves target relative to dir, as Git does for gitfiles
// and commondir files.
func resolvePointer(dir, target string) string {
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return filepath.Clean(target)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
