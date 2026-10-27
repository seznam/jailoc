//go:build darwin || linux

package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// hooks directory, config file and .git file, and pins on every .git
// directory, repository and directory between a workspace path and a
// repository, so that the agent cannot swap a protected .git for its own by
// renaming. Workspace paths are shared at their host paths, so host and
// guest paths are the same. Missing hooks directories are created, because
// the agent could otherwise create one with its own hooks.
func gitProtection(paths []string) (pins, readOnly []string, err error) {
	pinSet := map[string]bool{}
	roSet := map[string]bool{}
	scanned := 0
	for _, root := range paths {
		root = filepath.Clean(root)
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return err
				}
				return nil // unreadable subtree: nothing to protect there
			}
			if d.Name() == ".git" && path != root {
				repo := filepath.Dir(path)
				if err := protectRepo(root, repo, path, d.IsDir(), pinSet, roSet); err != nil {
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
	return sortedKeys(pinSet), sortedKeys(roSet), nil
}

// protectRepo records the protections of one repository whose .git entry
// is gitPath.
func protectRepo(root, repo, gitPath string, isDir bool, pins, ro map[string]bool) error {
	for d := repo; d != root && d != filepath.Dir(d); d = filepath.Dir(d) {
		pins[d] = true
	}
	if !isDir {
		// A gitfile (worktree or submodule checkout) points at the real
		// git directory; keep the pointer itself unchanged.
		ro[gitPath] = true
		return nil
	}
	pins[gitPath] = true
	return protectGitDir(gitPath, ro)
}

// protectGitDir marks hooks and config of gitDir and of the submodule git
// directories under gitDir/modules read-only.
func protectGitDir(gitDir string, ro map[string]bool) error {
	hooks := filepath.Join(gitDir, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil && !errors.Is(err, fs.ErrExist) { //nolint:gosec // G301: matches the hooks directory git creates
		return fmt.Errorf("create %s: %w", hooks, err)
	}
	ro[hooks] = true
	if info, err := os.Stat(filepath.Join(gitDir, "config")); err == nil && info.Mode().IsRegular() {
		ro[filepath.Join(gitDir, "config")] = true
	}
	modules := filepath.Join(gitDir, "modules")
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
		if err := protectGitDir(path, ro); err != nil {
			return err
		}
		return fs.SkipDir
	})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
