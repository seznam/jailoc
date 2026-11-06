//go:build darwin || linux

package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/seznam/jailoc/internal/vm"
)

// maxGitScanDirs bounds the directory walk that looks for Git repositories
// in the writable shares.
const maxGitScanDirs = 500_000

// gitScanSkip are directory names that never contain repositories worth
// protecting and are expensive to walk.
var gitScanSkip = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	".tox": true, ".terraform": true, ".next": true, ".gradle": true, ".cache": true,
}

// gitProtection finds the Git repositories under the writable shares and
// returns the guest paths jailoc-guest protects: read-only binds over every
// hooks directory, config file and .git file, and pins on every git
// directory, repository and directory between a share and a repository, so
// that the agent cannot swap a protected git directory for its own by
// renaming. Git directories that a .git file or a worktree's commondir
// points at (separate git directories, worktree and submodule metadata) are
// protected the same way when they are under a writable share; others are
// not writable in the VM. Files the repository configuration makes host Git
// read or run are protected too: config.worktree, files named by include
// and includeIf, the core.hooksPath directory and the targets of symlinked
// hooks. The host configuration files in inherited (system and global
// config) are read first, because a core.hooksPath set there applies to
// every repository. Missing hooks directories, and files Git would read
// once created, are created, because the agent could otherwise create them
// with its own content. A symbolic link on the way to a protected path
// inside a share fails the scan, because the agent could repoint it.
func gitProtection(shares []vm.Share, inherited []string) (pins, readOnly []string, err error) {
	p := gitProtector{
		pins: map[string]bool{}, ro: map[string]bool{}, done: map[string]bool{},
		config: map[string]*gitConfig{},
	}
	for _, s := range shares {
		guest := s.GuestPath
		if guest == "" {
			guest = s.HostPath
		}
		p.mounts = append(p.mounts, filepath.Clean(guest))
		if s.ReadOnly {
			continue
		}
		host := filepath.Clean(s.HostPath)
		resolved, err := filepath.EvalSymlinks(host)
		if err != nil {
			return nil, nil, fmt.Errorf("scan %s for git repositories: %w", host, err)
		}
		p.roots = append(p.roots, shareRoot{guest: filepath.Clean(guest), host: host, real: resolved})
	}
	for _, path := range inherited {
		if err := p.configFile(path, &p.inherited, newConfigWalk()); err != nil {
			return nil, nil, err
		}
	}
	// Git reads extensions.worktreeConfig from the repository only.
	p.inherited.worktreeConfig = false
	scanned := 0
	for i := range p.roots {
		r := &p.roots[i]
		walkErr := filepath.WalkDir(r.real, func(hostPath string, d fs.DirEntry, err error) error {
			if err != nil {
				if hostPath == r.real {
					return err
				}
				return nil // unreadable subtree: nothing to protect there
			}
			if hostPath != r.real && d.IsDir() && slices.Contains(p.mounts, r.guestOf(hostPath)) {
				return fs.SkipDir // another share covers this guest path
			}
			if d.Name() == ".git" && hostPath != r.real {
				if err := p.repo(r, hostPath, d); err != nil {
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
			if hostPath != r.real && gitScanSkip[d.Name()] {
				return fs.SkipDir
			}
			if isBareRepo(hostPath) {
				if err := p.gitDir(r, hostPath, ""); err != nil {
					return err
				}
				return fs.SkipDir
			}
			scanned++
			if scanned > maxGitScanDirs {
				return fmt.Errorf("more than %d directories under the writable shares; protect_git needs narrower paths", maxGitScanDirs)
			}
			return nil
		})
		if walkErr != nil {
			return nil, nil, fmt.Errorf("scan %s for git repositories: %w", r.host, walkErr)
		}
	}
	return sortedKeys(p.pins), sortedKeys(p.ro), nil
}

// gitInheritedConfig returns the host Git configuration files every
// repository inherits, in the order Git reads them: the system file of the
// common Git installations unless GIT_CONFIG_NOSYSTEM is set, and the
// global files. GIT_CONFIG_SYSTEM and GIT_CONFIG_GLOBAL replace them as
// they do for Git. Missing files are skipped when read.
func gitInheritedConfig() []string {
	var files []string
	if !gitBool(os.Getenv("GIT_CONFIG_NOSYSTEM")) {
		if f := os.Getenv("GIT_CONFIG_SYSTEM"); f != "" {
			files = append(files, f)
		} else {
			files = append(files,
				"/etc/gitconfig",
				"/opt/homebrew/etc/gitconfig",
				"/usr/local/etc/gitconfig",
				"/Library/Developer/CommandLineTools/usr/share/git-core/gitconfig",
				"/Applications/Xcode.app/Contents/Developer/usr/share/git-core/gitconfig",
			)
		}
	}
	if f := os.Getenv("GIT_CONFIG_GLOBAL"); f != "" {
		return append(files, f)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return files
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return append(files, filepath.Join(xdg, "git", "config"), filepath.Join(home, ".gitconfig"))
}

// shareRoot is a writable share: its guest path, its configured host path
// and its symlink-free host path.
type shareRoot struct{ guest, host, real string }

// guestOf returns the guest path of hostPath, a path under r.real.
func (r *shareRoot) guestOf(hostPath string) string {
	rel, err := filepath.Rel(r.real, hostPath)
	if err != nil {
		return r.guest
	}
	return filepath.Join(r.guest, rel)
}

// view is the guest path at which a writable share shows a host path.
type view struct{ guest, root string }

// views returns every guest path at which a writable share shows the
// symlink-free host path full. A guest path under a deeper mount point of
// any share, read-only ones included, is hidden by that share and left
// out.
func (p *gitProtector) views(full string) []view {
	var out []view
	for i := range p.roots {
		r := &p.roots[i]
		if !within(full, r.real) {
			continue
		}
		guest := r.guestOf(full)
		hidden := false
		for _, m := range p.mounts {
			if len(m) > len(r.guest) && within(guest, m) {
				hidden = true
				break
			}
		}
		if !hidden {
			out = append(out, view{guest: guest, root: r.guest})
		}
	}
	return out
}

// within reports whether path is base or below it.
func within(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && filepath.IsLocal(rel)
}

// pinHost pins the host directory full and its parents up to the share in
// every writable share that shows it.
func (p *gitProtector) pinHost(full string) {
	for _, v := range p.views(full) {
		p.pinUpTo(v.guest, v.root)
	}
}

// readOnlyHost marks the host path full read-only and pins its parents in
// every writable share that shows it.
func (p *gitProtector) readOnlyHost(full string) {
	for _, v := range p.views(full) {
		p.pinUpTo(filepath.Dir(v.guest), v.root)
		p.ro[v.guest] = true
	}
}

// gitConfig is what jailoc reads from a repository configuration.
type gitConfig struct {
	hooksPaths     []string // every core.hooksPath value, already expanded
	worktreeConfig bool     // extensions.worktreeConfig
}

// clone returns a copy of c that can be changed without changing c.
func (c *gitConfig) clone() gitConfig {
	if c == nil {
		return gitConfig{}
	}
	return gitConfig{hooksPaths: slices.Clone(c.hooksPaths), worktreeConfig: c.worktreeConfig}
}

// gitProtector collects the protections of the repositories under roots.
type gitProtector struct {
	roots     []shareRoot
	mounts    []string // guest mount points of every share
	pins      map[string]bool
	ro        map[string]bool
	done      map[string]bool       // git directories already protected
	config    map[string]*gitConfig // configuration by git directory
	inherited gitConfig             // host system and global configuration
}

// repo records the protections of the repository whose .git entry is the
// host path gitPath.
func (p *gitProtector) repo(r *shareRoot, gitPath string, d fs.DirEntry) error {
	worktree := filepath.Dir(gitPath)
	p.pinHost(filepath.Dir(gitPath))
	if d.IsDir() {
		return p.gitDir(r, gitPath, worktree)
	}
	// A gitfile (separate git directory, worktree or submodule checkout)
	// points at the real git directory; keep the pointer itself unchanged
	// and protect what it points at.
	p.readOnlyHost(gitPath)
	if !d.Type().IsRegular() {
		return nil
	}
	content, ok := readPointer(gitPath)
	target, isGitfile := strings.CutPrefix(content, "gitdir:")
	if !ok || !isGitfile {
		return nil
	}
	return p.linked(resolvePointer(worktree, strings.TrimSpace(target)), worktree)
}

// linked protects the git directory target, reached through a pointer,
// when it is under a writable share.
func (p *gitProtector) linked(target, worktree string) error {
	r, dir, err := p.locate(target)
	if err != nil || r == nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil //nolint:nilerr // not a git directory
	}
	return p.gitDir(r, dir, worktree)
}

// gitDir protects the host git directory dir, used by the checkout
// worktree (empty when unknown), and the files its configuration makes Git
// read or run for that checkout. A worktree git directory takes hooks and
// config from its common directory, which is protected as well.
func (p *gitProtector) gitDir(r *shareRoot, dir, worktree string) error {
	common, err := p.protectGitDir(r, dir)
	if err != nil {
		return err
	}
	// Settings of one worktree must not leak into another, so each starts
	// from a copy of the common configuration.
	cfg := p.config[common].clone()
	if cfg.worktreeConfig {
		if err := p.configFile(filepath.Join(dir, "config.worktree"), &cfg, newConfigWalk()); err != nil {
			return err
		}
	} else if _, err := p.protect(filepath.Join(dir, "config.worktree"), false, false); err != nil {
		return err
	}
	return p.hooksPath(&cfg, worktree, dir)
}

// protectGitDir pins the git directory dir and protects the hooks and
// configuration it uses, and returns the directory holding them: dir, or
// the common directory of a worktree git directory (empty when that is not
// under a writable share).
func (p *gitProtector) protectGitDir(r *shareRoot, dir string) (string, error) {
	common, ok := readPointer(filepath.Join(dir, "commondir"))
	if !ok {
		return dir, p.protectCommonDir(r, dir)
	}
	if !p.done[dir] {
		p.done[dir] = true
		p.pinHost(dir)
		p.readOnlyHost(filepath.Join(dir, "commondir"))
	}
	cr, commonDir, err := p.locate(resolvePointer(dir, common))
	if err != nil || cr == nil {
		return "", err
	}
	if info, err := os.Stat(commonDir); err != nil || !info.IsDir() {
		return "", nil //nolint:nilerr // not a git directory
	}
	// Git does not follow a commondir of a common directory; refusing it
	// also stops a commondir that points back at itself.
	if _, ok := readPointer(filepath.Join(commonDir, "commondir")); ok {
		return "", fmt.Errorf("%s: commondir points at %s, which has a commondir of its own; protect_git cannot protect it", r.guestOf(dir), cr.guestOf(commonDir))
	}
	return commonDir, p.protectCommonDir(cr, commonDir)
}

// protectCommonDir pins the git directory dir, which holds hooks and
// configuration, marks them read-only and records the configuration.
// Submodule git directories under dir/modules are protected the same way.
func (p *gitProtector) protectCommonDir(r *shareRoot, dir string) error {
	if p.done[dir] {
		return nil
	}
	p.done[dir] = true
	p.pinHost(dir)
	hooks, err := p.protect(filepath.Join(dir, "hooks"), true, true)
	if err != nil {
		return err
	}
	if err := p.hookTargets(hooks); err != nil {
		return err
	}
	cfg := p.inherited.clone()
	p.config[dir] = &cfg
	if err := p.configFile(filepath.Join(dir, "config"), &cfg, newConfigWalk()); err != nil {
		return err
	}
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
		if err := p.gitDir(r, path, ""); err != nil {
			return err
		}
		return fs.SkipDir
	})
}

// maxConfigFiles bounds the configuration files, with every include and
// includeIf, read for one configuration.
const maxConfigFiles = 256

// configWalk tracks the files read for one configuration, so that a file
// included twice or in a loop is read once.
type configWalk struct {
	seen  map[string]bool
	count int
}

func newConfigWalk() *configWalk { return &configWalk{seen: map[string]bool{}} }

// configFile protects the Git configuration file path, creating it empty
// when it is missing, and the files it includes, and records what it sets
// in cfg.
func (p *gitProtector) configFile(path string, cfg *gitConfig, walk *configWalk) error {
	path = filepath.Clean(path)
	if walk.seen[path] {
		return nil
	}
	walk.seen[path] = true
	walk.count++
	if walk.count > maxConfigFiles {
		return fmt.Errorf("git config %s includes more than %d files; protect_git cannot check them", path, maxConfigFiles)
	}
	if _, err := p.protect(path, false, true); err != nil {
		return err
	}
	data, ok := readSmall(path, maxConfigSize, false)
	if !ok {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("git config %s cannot be read (larger than 1 MiB or not a regular file); protect_git cannot check it", path)
	}
	for _, e := range parseGitConfig(data) {
		switch {
		case e.section == "core" && e.key == "hookspath":
			// Every value is kept: an includeIf jailoc does not evaluate
			// may or may not apply.
			cfg.hooksPaths = append(cfg.hooksPaths, expandGitPath(e.value, ""))
		case e.section == "extensions" && e.key == "worktreeconfig":
			// Any true value counts: an includeIf may set it.
			cfg.worktreeConfig = cfg.worktreeConfig || gitBool(e.value)
		case (e.section == "include" || e.section == "includeif") && e.key == "path" && e.value != "":
			inc := expandGitPath(e.value, filepath.Dir(path))
			if err := p.configFile(inc, cfg, walk); err != nil {
				return err
			}
		}
	}
	return nil
}

// hooksPath protects the core.hooksPath directories of cfg. Git resolves
// a relative hooksPath against the directory a hook runs in: the worktree
// for most hooks, the git directory dir for hooks run on the receiving
// side of a push. Both are protected.
func (p *gitProtector) hooksPath(cfg *gitConfig, worktree, dir string) error {
	for _, path := range cfg.hooksPaths {
		if path == "" {
			continue
		}
		paths := []string{path}
		if !filepath.IsAbs(path) {
			paths = []string{filepath.Join(dir, path)}
			if worktree != "" {
				paths = append(paths, filepath.Join(worktree, path))
			}
		}
		for _, path := range paths {
			hooks, err := p.protect(path, true, true)
			if err != nil {
				return err
			}
			if err := p.hookTargets(hooks); err != nil {
				return err
			}
		}
	}
	return nil
}

// maxHookEntries bounds the entries of a hooks directory jailoc inspects.
const maxHookEntries = 1024

// hookTargets protects the files that symlinked hooks in the host
// directory dir point at. A hook pointing at a missing file in a writable
// share fails, because the agent could create that file.
func (p *gitProtector) hookTargets(dir string) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil //nolint:nilerr // unreadable hooks directory: git cannot run its hooks either
	}
	if len(entries) > maxHookEntries {
		return fmt.Errorf("hooks directory %s has more than %d entries; protect_git cannot check them", dir, maxHookEntries)
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		hook := filepath.Join(dir, e.Name())
		link, err := os.Readlink(hook)
		if err != nil {
			continue
		}
		target := resolvePointer(dir, link)
		full, err := p.protect(target, false, false)
		if err != nil {
			return err
		}
		if full != "" {
			continue
		}
		if r, _, _ := p.locate(target); r != nil {
			return fmt.Errorf("hook %s points at %s, which does not exist; protect_git cannot protect it (remove the hook or create the file)", hook, target)
		}
	}
	return nil
}

// protect marks the host path read-only when it is under a writable share
// and returns its symlink-free host path, or "" when it is not shared
// writable or does not exist and create is false. A missing path is
// created as an empty directory or file when create is set. dir selects a
// directory or a regular file.
func (p *gitProtector) protect(path string, dir, create bool) (string, error) {
	r, full, err := p.locate(path)
	if err != nil || r == nil {
		return "", err
	}
	guest := r.guestOf(full)
	info, err := os.Lstat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !create {
			return "", nil
		}
		if err := createEmpty(full, dir); err != nil {
			return "", err
		}
	case err != nil:
		return "", fmt.Errorf("protect %s: %w", guest, err)
	case dir && !info.IsDir():
		return "", fmt.Errorf("protect %s: not a directory; protect_git cannot protect it", guest)
	case !dir && !info.Mode().IsRegular():
		return "", fmt.Errorf("protect %s: not a regular file; protect_git cannot protect it", guest)
	}
	p.readOnlyHost(full)
	return full, nil
}

// createEmpty creates the empty directory or file path and its parents.
func createEmpty(path string, dir bool) error {
	if dir {
		if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // G301: matches the directories git creates
			return fmt.Errorf("create %s: %w", path, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: matches the directories git creates
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302,G304: git config file under a workspace share
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// locate finds the writable share holding the host or guest path and
// returns it with the symlink-free host path. A path under a share must
// not pass through a symbolic link inside the share, because the agent
// could repoint it. A path outside every share is resolved first, so a
// link outside the shares into one is followed, while a link inside a
// share on the way fails the same. It returns a nil share
// when the path is not in a writable share.
func (p *gitProtector) locate(path string) (*shareRoot, string, error) {
	path = filepath.Clean(path)
	var best *shareRoot
	bestRel, bestLen := "", -1
	match := func(r *shareRoot, base string) {
		rel, err := filepath.Rel(base, path)
		if err == nil && filepath.IsLocal(rel) && len(base) > bestLen {
			best, bestRel, bestLen = r, rel, len(base)
		}
	}
	for i := range p.roots {
		match(&p.roots[i], p.roots[i].real)
		match(&p.roots[i], p.roots[i].host)
	}
	if best == nil {
		// Pointers written inside the VM name guest paths.
		for i := range p.roots {
			match(&p.roots[i], p.roots[i].guest)
		}
	}
	if best != nil {
		return best, filepath.Join(best.real, bestRel), noSymlinks(best, bestRel)
	}
	reals := make([]string, len(p.roots))
	for i := range p.roots {
		reals[i] = p.roots[i].real
	}
	link, i, err := linkInside(path, reals)
	if err != nil {
		return nil, "", err
	}
	if link != "" {
		r := &p.roots[i]
		return nil, "", fmt.Errorf("%s is a symbolic link that Git follows to a protected path; protect_git cannot keep it in place (replace it with the file or directory, or disable protect_git)", r.guestOf(link))
	}
	resolved := resolveExisting(path)
	for i := range p.roots {
		r := &p.roots[i]
		if rel, err := filepath.Rel(r.real, resolved); err == nil && filepath.IsLocal(rel) {
			return r, filepath.Join(r.real, rel), nil
		}
	}
	return nil, "", nil
}

// noSymlinks fails when a component of rel under the share r is a symbolic
// link.
func noSymlinks(r *shareRoot, rel string) error {
	cur := r.real
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("protect %s: %w", r.guestOf(cur), err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link that Git follows to a protected path; protect_git cannot keep it in place (replace it with the file or directory, or disable protect_git)", r.guestOf(cur))
		}
	}
	return nil
}

// resolveExisting resolves the symbolic links of the longest existing
// prefix of path.
func resolveExisting(path string) string {
	rest := ""
	for cur := path; ; cur = filepath.Dir(cur) {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		if cur == filepath.Dir(cur) {
			return path
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

// isBareRepo reports whether dir looks like a bare repository.
func isBareRepo(dir string) bool {
	if info, err := os.Lstat(filepath.Join(dir, "HEAD")); err != nil || !info.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if info, err := os.Lstat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

// pinUpTo pins dir and its parents below root.
func (p *gitProtector) pinUpTo(dir, root string) {
	for d := dir; d != root && d != filepath.Dir(d); d = filepath.Dir(d) {
		p.pins[d] = true
	}
}

// maxPointerSize bounds the gitfile and commondir files jailoc reads.
const maxPointerSize = 4096

// maxConfigSize bounds the Git configuration files jailoc reads.
const maxConfigSize = 1 << 20

// readPointer returns the first line of the small regular file path, such
// as a gitfile or a commondir file.
func readPointer(path string) (string, bool) {
	data, ok := readSmall(path, maxPointerSize, true)
	if !ok {
		return "", false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	return line, line != ""
}

// readSmall reads the regular file path if it holds at most max bytes.
// With noFollow a symbolic link is not followed. The file is opened
// without blocking, so a FIFO cannot stall the scan.
func readSmall(path string, maxSize int64, noFollow bool) ([]byte, bool) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if noFollow {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0) //nolint:gosec // G304: path is inside a workspace share or named by its git config
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }() // read-only
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil || int64(len(data)) > maxSize {
		return nil, false
	}
	return data, true
}

// resolvePointer resolves target relative to dir, as Git does for gitfiles
// and commondir files.
func resolvePointer(dir, target string) string {
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return filepath.Clean(target)
}

// expandGitPath expands a leading ~/ to the home directory and resolves a
// relative path against base, as Git does for include paths. With an empty
// base a relative path is returned unchanged.
func expandGitPath(path, base string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	if base != "" && !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

// gitConfigEntry is one variable of a Git configuration file, on lines first
// to last (counted from 0) of the file.
type gitConfigEntry struct {
	section, key, value string
	first, last         int
}

// parseGitConfig returns the variables of a Git configuration file with
// lower-case section (without subsection) and key names. It covers the
// syntax Git writes and common hand edits, including values continued on
// the next line with a trailing backslash.
func parseGitConfig(data []byte) []gitConfigEntry {
	var out []gitConfigEntry
	section := ""
	lines := strings.Split(string(data), "\n")
	for n := 0; n < len(lines); n++ {
		first := n
		line := strings.TrimSpace(lines[n])
		if strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			name := strings.TrimSpace(line[1:end])
			if i := strings.IndexAny(name, " \t."); i >= 0 {
				name = name[:i]
			}
			section = strings.ToLower(name)
			line = strings.TrimSpace(line[end+1:])
		}
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		key, value, hasValue := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !hasValue {
			key, _, _ = strings.Cut(key, "#")
			key, _, _ = strings.Cut(key, ";")
			key = strings.TrimSpace(key)
			value = "true"
		} else {
			raw := value
			v, more := gitConfigValue(raw)
			for more && n+1 < len(lines) {
				n++
				raw = raw[:len(raw)-1] + strings.TrimRight(lines[n], "\r")
				v, more = gitConfigValue(raw)
			}
			value = v
		}
		if key != "" {
			out = append(out, gitConfigEntry{section: section, key: key, value: value, first: first, last: n})
		}
	}
	return out
}

// gitConfigValue decodes a Git configuration value: quotes, escapes and
// trailing comments. continued reports a trailing backslash outside a
// comment, which continues the value on the next line.
func gitConfigValue(raw string) (value string, continued bool) {
	var b strings.Builder
	quoted := false
	pending := "" // unquoted whitespace kept only before more content
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\' && i+1 == len(raw):
			return b.String(), true
		case c == '\\':
			i++
			b.WriteString(pending)
			pending = ""
			switch raw[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			default:
				b.WriteByte(raw[i])
			}
		case c == '"':
			quoted = !quoted
		case !quoted && (c == '#' || c == ';'):
			return b.String(), false
		case !quoted && (c == ' ' || c == '\t'):
			if b.Len() > 0 {
				pending += string(c)
			}
		default:
			b.WriteString(pending)
			pending = ""
			b.WriteByte(c)
		}
	}
	return b.String(), false
}

// gitBool reports whether a Git configuration value is true. Like Git it
// accepts true, yes, on and their opposites, and integers with an optional
// k, m or g suffix, where any value other than zero is true.
func gitBool(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "true", "yes", "on":
		return true
	case "", "false", "no", "off":
		return false
	}
	if n := len(v) - 1; n > 0 && strings.ContainsRune("kmg", rune(v[n])) {
		v = v[:n]
	}
	i, err := strconv.ParseInt(v, 0, 64)
	return err == nil && i != 0
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
