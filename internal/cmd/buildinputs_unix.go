//go:build darwin || linux

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// Kinds of build input, as error messages name them.
const (
	inputBaseDockerfile = "base Dockerfile"
	inputDockerfile     = "Dockerfile"
	inputContext        = "build context"
)

// buildInput is a local host path that the build of a workspace image reads.
// dir marks a build context, whose whole tree the build reads.
type buildInput struct {
	kind string
	path string
	dir  bool
}

// effectiveBuildInputs returns the local files and directories that
// ResolveAndLayerImage reads to build the image of ws: the [base] Dockerfile
// of the base image cascade, the workspace Dockerfile and its build context.
// Images used directly, Dockerfiles fetched from a URL and the empty context
// of a URL Dockerfile read nothing local. Paths are as the build receives
// them, not cleaned: a lexical clean would drop the effect of a symbolic link
// followed by "..".
func effectiveBuildInputs(cfg *config.Config, ws *workspace.Resolved) ([]buildInput, error) {
	var inputs []buildInput
	switch strategy, _ := resolveImageStrategy(ws.Image, cfg.Defaults.Image, ws.Dockerfile); strategy {
	case strategyDirectImage, strategyDefaultsDirect:
		return nil, nil
	case strategyCascade:
		if base := strings.TrimSpace(cfg.Base.Dockerfile); base != "" {
			local, err := docker.IsLocalDockerfile(base)
			if err != nil {
				return nil, fmt.Errorf("base dockerfile: %w", err)
			}
			if local {
				inputs = append(inputs, buildInput{kind: inputBaseDockerfile, path: base})
			}
		}
	}
	// The overlay is built only from a Dockerfile; without one the build
	// context is unused.
	dockerfile := ws.Dockerfile
	if strings.TrimSpace(dockerfile) == "" {
		return inputs, nil
	}
	local, err := docker.IsLocalDockerfile(dockerfile)
	if err != nil {
		return nil, fmt.Errorf("workspace %q dockerfile: %w", ws.Name, err)
	}
	if local {
		inputs = append(inputs, buildInput{kind: inputDockerfile, path: dockerfile})
	}
	switch {
	case strings.TrimSpace(ws.BuildContext) != "":
		inputs = append(inputs, buildInput{kind: inputContext, path: ws.BuildContext, dir: true})
	case local:
		inputs = append(inputs, buildInput{kind: inputContext, path: filepath.Dir(dockerfile), dir: true})
	}
	return inputs, nil
}

// workspaceShare is a share of the VM of a workspace.
type workspaceShare struct {
	workspace string
	share     vm.Share
}

// checkBuildInputs rejects the build inputs that a read-write share in
// exports lets a guest change: an input that lies in the share, a build
// context that contains it, an input whose path runs through a symbolic link
// or an ordinary directory inside it, which the guest can replace with a
// link, and a Dockerfile with other hard links, whose other names a share may
// expose. Read-only shares are allowed. Paths are resolved, a missing input
// is taken by the path it would have, so the guest cannot create it later, and
// containment is decided by file identity, so a path spelled with other case
// on a case-insensitive filesystem conflicts too. A read-write share that
// cannot be resolved, and an input path that cannot be resolved, fail the
// check.
func checkBuildInputs(inputs []buildInput, exports []workspaceShare) error {
	var (
		writable []workspaceShare
		roots    []rwShareRoot
	)
	for _, e := range exports {
		if e.share.ReadOnly {
			continue
		}
		root, err := resolveRWShareRoot(e.share.HostPath)
		if err != nil {
			return fmt.Errorf("resolve the read-write share %s of workspace %q: %w", e.share.HostPath, e.workspace, err)
		}
		writable = append(writable, e)
		roots = append(roots, root)
	}
	if len(writable) == 0 {
		return nil
	}
	var errs []error
	for _, in := range inputs {
		errs = append(errs, checkBuildInput(in, writable, roots)...)
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w\nkeep build inputs out of read-write shares: share directories without them, mount them read-only, or use a prebuilt image", errors.Join(errs...))
}

// rwShareRoot is the resolved root directory of a read-write share and its
// file identity.
type rwShareRoot struct {
	path string
	info os.FileInfo
}

// resolveRWShareRoot resolves host to an absolute path with its symbolic links
// resolved, and takes its file identity.
func resolveRWShareRoot(host string) (rwShareRoot, error) {
	real, err := filepath.EvalSymlinks(host)
	if err != nil {
		return rwShareRoot{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	if !filepath.IsAbs(real) {
		if real, err = filepath.Abs(real); err != nil {
			return rwShareRoot{}, fmt.Errorf("resolve %s: %w", host, err)
		}
		if real, err = filepath.EvalSymlinks(real); err != nil {
			return rwShareRoot{}, fmt.Errorf("resolve %s: %w", host, err)
		}
	}
	info, err := os.Lstat(real)
	if err != nil {
		return rwShareRoot{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	return rwShareRoot{path: real, info: info}, nil
}

// isRoot reports whether path, if it exists, is the root itself.
func (r rwShareRoot) isRoot(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("compare %s with the share root: %w", path, err)
	}
	return os.SameFile(info, r.info), nil
}

// contains reports whether the root is path or one of its ancestors, by file
// identity. Ancestors that do not exist are skipped.
func (r rwShareRoot) contains(path string) (bool, error) {
	for p := path; ; p = filepath.Dir(p) {
		if same, err := r.isRoot(p); err != nil || same {
			return same, err
		}
		if filepath.Dir(p) == p {
			return false, nil
		}
	}
}

// containsBelow reports whether path lies strictly inside the root, that is
// whether the root is an ancestor of path.
func (r rwShareRoot) containsBelow(path string) (bool, error) {
	parent := filepath.Dir(path)
	if parent == path {
		return false, nil
	}
	return r.contains(parent)
}

// checkBuildInput returns the conflicts of one build input with the roots of
// the read-write shares in writable.
func checkBuildInput(in buildInput, writable []workspaceShare, roots []rwShareRoot) []error {
	real, links, dirs, err := resolveBuildInputPath(in.path)
	if err != nil {
		return []error{fmt.Errorf("%s %s: %w", in.kind, in.path, err)}
	}
	var errs []error
	fail := func(err error) []error { return append(errs, fmt.Errorf("%s %s: %w", in.kind, in.path, err)) }
	for i, root := range roots {
		w := writable[i]
		inside, err := root.contains(real)
		if err != nil {
			return fail(err)
		}
		inside = inside || pathWithin(real, root.path)
		var above bool
		if in.dir && !inside {
			// A context that exists contains the share if it is an ancestor of its root.
			if above, err = containsByIdentity(real, root); err != nil {
				return fail(err)
			}
			above = above || pathWithin(root.path, real)
		}
		switch {
		case inside:
			errs = append(errs, fmt.Errorf("%s %s lies in the read-write share %s of workspace %q", in.kind, in.path, w.share.HostPath, w.workspace))
		case above:
			errs = append(errs, fmt.Errorf("%s %s contains the read-write share %s of workspace %q", in.kind, in.path, w.share.HostPath, w.workspace))
		}
	}
	link, i, err := firstInsideRoots(links, roots)
	if err != nil {
		return fail(err)
	}
	if i >= 0 {
		w := writable[i]
		errs = append(errs, fmt.Errorf("%s %s runs through the symbolic link %q inside the read-write share %s of workspace %q", in.kind, in.path, link, w.share.HostPath, w.workspace))
	}
	// The guest can replace an ordinary directory inside a share with a link
	// and so redirect the ".." that follows it.
	dir, i, err := firstInsideRoots(dirs, roots)
	if err != nil {
		return fail(err)
	}
	if i >= 0 {
		w := writable[i]
		errs = append(errs, fmt.Errorf("%s %s runs through the directory %q inside the read-write share %s of workspace %q, which the guest can replace with a symbolic link", in.kind, in.path, dir, w.share.HostPath, w.workspace))
	}
	if in.dir {
		return errs
	}
	if info, err := os.Lstat(real); err == nil && info.Mode().IsRegular() && hardLinked(info) {
		errs = append(errs, fmt.Errorf("%s %s has other hard links, which a read-write share can expose; give it a file of its own", in.kind, in.path))
	}
	return errs
}

// containsByIdentity reports whether the existing path is an ancestor of the
// root, or the root itself, by file identity.
func containsByIdentity(path string, root rwShareRoot) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("compare %s with the share root: %w", path, err)
	}
	for p := root.path; ; p = filepath.Dir(p) {
		anc, err := os.Lstat(p)
		if err == nil && os.SameFile(info, anc) {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("compare %s with the share root: %w", p, err)
		}
		if filepath.Dir(p) == p {
			return false, nil
		}
	}
}

// firstInsideRoots returns the first of paths that lies strictly inside one
// of roots, with the index of that root, or "" and -1. The root itself is
// not inside it.
func firstInsideRoots(paths []string, roots []rwShareRoot) (string, int, error) {
	for _, p := range paths {
		for i, r := range roots {
			in, err := r.containsBelow(p)
			if err != nil {
				return "", -1, err
			}
			if in {
				return p, i, nil
			}
		}
	}
	return "", -1, nil
}

// resolveBuildInputPath resolves path the way the build opens it: from the
// root, or from the physical working directory for a relative path, one
// component at a time, with ".." applied to the resolved prefix. It returns
// the real path, which for a missing target is the resolved prefix plus the
// missing rest, the location of every symbolic link it followed and every
// existing directory it stepped into, also those it left again with "..". A
// missing target followed by ".." and a non-directory followed by any
// component fail.
func resolveBuildInputPath(path string) (string, []string, []string, error) {
	sep := string(filepath.Separator)
	cur := sep
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err == nil {
			cur, err = filepath.EvalSymlinks(wd)
		}
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolve %s: %w", path, err)
		}
	}
	var (
		links, dirs []string
		rest        = strings.Split(path, sep)
	)
	for len(rest) > 0 {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			real, links, err := missingBuildInputPath(path, next, rest, links)
			return real, links, dirs, err
		}
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.IsDir() && len(rest) > 0 {
				return "", nil, nil, fmt.Errorf("resolve %s: %s: %w", path, next, syscall.ENOTDIR)
			}
			if info.IsDir() {
				dirs = append(dirs, next)
			}
			cur = next
			continue
		}
		if links = append(links, next); len(links) > maxLinkHops {
			return "", nil, nil, fmt.Errorf("resolve %s: too many levels of symbolic links", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		if filepath.IsAbs(target) {
			cur = sep
		}
		rest = append(strings.Split(target, sep), rest...)
	}
	return cur, links, dirs, nil
}

// missingBuildInputPath returns the path a missing target would have, which a
// guest can create later, with the links followed so far, or an error when the
// rest of path steps back out of the missing directory with "..".
func missingBuildInputPath(path, missing string, rest, links []string) (string, []string, error) {
	real := missing
	for _, part := range rest {
		switch part {
		case "", ".":
		case "..":
			return "", nil, fmt.Errorf("resolve %s: %s does not exist, so \"..\" after it has no meaning", path, missing)
		default:
			real = filepath.Join(real, part)
		}
	}
	return real, links, nil
}
