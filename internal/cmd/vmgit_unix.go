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
// hooks. Missing hooks directories, and files Git would read once created,
// are created, because the agent could otherwise create them with its own
// content. A symbolic link on the way to a protected path inside a share
// fails the scan, because the agent could repoint it.
func gitProtection(shares []vm.Share) (pins, readOnly []string, err error) {
	p := gitProtector{
		pins: map[string]bool{}, ro: map[string]bool{}, done: map[string]bool{},
		config: map[string]*gitConfig{},
	}
	for _, s := range shares {
		if s.ReadOnly {
			continue
		}
		host := filepath.Clean(s.HostPath)
		guest := s.GuestPath
		if guest == "" {
			guest = host
		}
		resolved, err := filepath.EvalSymlinks(host)
		if err != nil {
			return nil, nil, fmt.Errorf("scan %s for git repositories: %w", host, err)
		}
		p.roots = append(p.roots, shareRoot{guest: filepath.Clean(guest), host: host, real: resolved})
	}
	guestRoots := map[string]bool{}
	for _, r := range p.roots {
		guestRoots[r.guest] = true
	}
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
			if hostPath != r.real && d.IsDir() && guestRoots[r.guestOf(hostPath)] {
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

// gitConfig is what jailoc reads from a repository configuration.
type gitConfig struct {
	hooksPath      string // core.hooksPath, already expanded
	worktreeConfig bool   // extensions.worktreeConfig
}

// gitProtector collects the protections of the repositories under roots.
type gitProtector struct {
	roots  []shareRoot
	pins   map[string]bool
	ro     map[string]bool
	done   map[string]bool       // git directories already protected
	config map[string]*gitConfig // configuration by git directory
}

// repo records the protections of the repository whose .git entry is the
// host path gitPath.
func (p *gitProtector) repo(r *shareRoot, gitPath string, d fs.DirEntry) error {
	worktree := filepath.Dir(gitPath)
	p.pinUpTo(filepath.Dir(r.guestOf(gitPath)), r.guest)
	if d.IsDir() {
		return p.gitDir(r, gitPath, worktree)
	}
	// A gitfile (separate git directory, worktree or submodule checkout)
	// points at the real git directory; keep the pointer itself unchanged
	// and protect what it points at.
	p.ro[r.guestOf(gitPath)] = true
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

// gitDir pins the host git directory dir and the directories between it
// and its share, and marks its hooks and configuration read-only. A
// worktree git directory takes hooks and config from its common directory,
// which is protected instead. Submodule git directories under dir/modules
// are protected the same way. worktree is the checkout using dir, or empty
// when unknown.
func (p *gitProtector) gitDir(r *shareRoot, dir, worktree string) error {
	if common, ok := readPointer(filepath.Join(dir, "commondir")); ok {
		if !p.done[dir] {
			p.done[dir] = true
			p.pinUpTo(r.guestOf(dir), r.guest)
			p.ro[r.guestOf(filepath.Join(dir, "commondir"))] = true
		}
		commonDir := resolvePointer(dir, common)
		if err := p.linked(commonDir, worktree); err != nil {
			return err
		}
		cfg := &gitConfig{}
		if _, real, err := p.locate(commonDir); err == nil && p.config[real] != nil {
			cfg = p.config[real]
		}
		if cfg.worktreeConfig {
			if err := p.configFile(filepath.Join(dir, "config.worktree"), cfg, 0); err != nil {
				return err
			}
		} else if _, err := p.protect(filepath.Join(dir, "config.worktree"), false, false); err != nil {
			return err
		}
		return p.hooksPath(cfg, worktree, dir)
	}
	if p.done[dir] {
		return p.hooksPath(p.config[dir], worktree, dir)
	}
	p.done[dir] = true
	p.pinUpTo(r.guestOf(dir), r.guest)
	hooks, err := p.protect(filepath.Join(dir, "hooks"), true, true)
	if err != nil {
		return err
	}
	if err := p.hookTargets(hooks); err != nil {
		return err
	}
	cfg := &gitConfig{}
	p.config[dir] = cfg
	if err := p.configFile(filepath.Join(dir, "config"), cfg, 0); err != nil {
		return err
	}
	if cfg.worktreeConfig {
		if err := p.configFile(filepath.Join(dir, "config.worktree"), cfg, 0); err != nil {
			return err
		}
	} else if _, err := p.protect(filepath.Join(dir, "config.worktree"), false, false); err != nil {
		return err
	}
	if err := p.hooksPath(cfg, worktree, dir); err != nil {
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

// maxConfigDepth bounds nested include and includeIf files.
const maxConfigDepth = 10

// configFile protects the Git configuration file path, creating it empty
// when it is missing, and the files it includes, and records what it sets
// in cfg.
func (p *gitProtector) configFile(path string, cfg *gitConfig, depth int) error {
	if depth > maxConfigDepth {
		return nil
	}
	if _, err := p.protect(path, false, true); err != nil {
		return err
	}
	data, ok := readSmall(path, maxConfigSize, false)
	if !ok {
		return nil
	}
	for _, e := range parseGitConfig(data) {
		switch {
		case e.section == "core" && e.key == "hookspath":
			cfg.hooksPath = expandGitPath(e.value, "")
		case e.section == "extensions" && e.key == "worktreeconfig":
			cfg.worktreeConfig = gitBool(e.value)
		case (e.section == "include" || e.section == "includeif") && e.key == "path" && e.value != "":
			inc := expandGitPath(e.value, filepath.Dir(path))
			if err := p.configFile(inc, cfg, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// hooksPath protects the core.hooksPath directory of cfg. A relative
// hooksPath is relative to the worktree, or to the git directory dir of a
// bare repository.
func (p *gitProtector) hooksPath(cfg *gitConfig, worktree, dir string) error {
	if cfg == nil || cfg.hooksPath == "" {
		return nil
	}
	path := cfg.hooksPath
	if !filepath.IsAbs(path) {
		base := worktree
		if base == "" {
			base = dir
		}
		path = filepath.Join(base, path)
	}
	hooks, err := p.protect(path, true, true)
	if err != nil {
		return err
	}
	return p.hookTargets(hooks)
}

// maxHookEntries bounds the entries of a hooks directory jailoc inspects.
const maxHookEntries = 1024

// hookTargets protects the files that symlinked hooks in the host
// directory dir point at.
func (p *gitProtector) hookTargets(dir string) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil //nolint:nilerr // unreadable hooks directory: git cannot run its hooks either
	}
	for i, e := range entries {
		if i >= maxHookEntries {
			break
		}
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		link, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if _, err := p.protect(resolvePointer(dir, link), false, false); err != nil {
			return err
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
	p.pinUpTo(filepath.Dir(guest), r.guest)
	p.ro[guest] = true
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
// link outside the shares into one is followed. It returns a nil share
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

// gitConfigEntry is one variable of a Git configuration file.
type gitConfigEntry struct{ section, key, value string }

// parseGitConfig returns the variables of a Git configuration file with
// lower-case section (without subsection) and key names. It covers the
// syntax Git writes and common hand edits; continuation lines are not
// joined.
func parseGitConfig(data []byte) []gitConfigEntry {
	var out []gitConfigEntry
	section := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
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
			value = gitConfigValue(value)
		}
		if key != "" {
			out = append(out, gitConfigEntry{section: section, key: key, value: value})
		}
	}
	return out
}

// gitConfigValue decodes a Git configuration value: quotes, escapes and
// trailing comments.
func gitConfigValue(raw string) string {
	var b strings.Builder
	quoted := false
	pending := "" // unquoted whitespace kept only before more content
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\' && i+1 < len(raw):
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
			return strings.TrimSpace(b.String())
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
	return b.String()
}

// gitBool reports whether a Git configuration value is true.
func gitBool(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
