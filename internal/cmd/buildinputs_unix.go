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
// inside it, and a Dockerfile with other hard links, whose other names a
// share may expose. Read-only shares are allowed. Paths are compared with
// their symbolic links resolved, and a missing input by the path it would
// have, so the guest cannot create it later. A read-write share that cannot
// be resolved, and an input path that cannot be resolved, fail the check.
func checkBuildInputs(inputs []buildInput, exports []workspaceShare) error {
	var (
		writable []workspaceShare
		roots    []string
	)
	for _, e := range exports {
		if e.share.ReadOnly {
			continue
		}
		real, err := resolveShareRoot(e.share.HostPath)
		if err != nil {
			return fmt.Errorf("resolve the read-write share %s of workspace %q: %w", e.share.HostPath, e.workspace, err)
		}
		writable = append(writable, e)
		roots = append(roots, real)
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

// resolveShareRoot returns the absolute path of host with its symbolic links
// resolved.
func resolveShareRoot(host string) (string, error) {
	real, err := filepath.EvalSymlinks(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	if filepath.IsAbs(real) {
		return real, nil
	}
	if real, err = filepath.Abs(real); err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	if real, err = filepath.EvalSymlinks(real); err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	return real, nil
}

// checkBuildInput returns the conflicts of one build input with the roots of
// the read-write shares in writable.
func checkBuildInput(in buildInput, writable []workspaceShare, roots []string) []error {
	real, links, err := resolveBuildInputPath(in.path)
	if err != nil {
		return []error{fmt.Errorf("%s %s: %w", in.kind, in.path, err)}
	}
	var errs []error
	for i, root := range roots {
		w := writable[i]
		switch {
		case pathWithin(real, root):
			errs = append(errs, fmt.Errorf("%s %s lies in the read-write share %s of workspace %q", in.kind, in.path, w.share.HostPath, w.workspace))
		case in.dir && pathWithin(root, real):
			errs = append(errs, fmt.Errorf("%s %s contains the read-write share %s of workspace %q", in.kind, in.path, w.share.HostPath, w.workspace))
		}
	}
	if link, i := linkInsideRoots(links, roots); link != "" {
		w := writable[i]
		errs = append(errs, fmt.Errorf("%s %s runs through the symbolic link %q inside the read-write share %s of workspace %q", in.kind, in.path, link, w.share.HostPath, w.workspace))
	}
	if in.dir {
		return errs
	}
	if info, err := os.Lstat(real); err == nil && info.Mode().IsRegular() && hardLinked(info) {
		errs = append(errs, fmt.Errorf("%s %s has other hard links, which a read-write share can expose; give it a file of its own", in.kind, in.path))
	}
	return errs
}

// linkInsideRoots returns the first of links that lies strictly inside one of
// roots, with the index of that root, or "" and -1.
func linkInsideRoots(links, roots []string) (string, int) {
	for _, link := range links {
		for i, root := range roots {
			if link != root && pathWithin(link, root) {
				return link, i
			}
		}
	}
	return "", -1
}

// resolveBuildInputPath resolves path the way the build opens it: from the
// root, or from the physical working directory for a relative path, one
// component at a time, with ".." applied to the resolved prefix. It returns
// the real path, which for a missing target is the resolved prefix plus the
// missing rest, and the location of every symbolic link it followed. A
// missing target followed by ".." and a non-directory followed by any
// component fail.
func resolveBuildInputPath(path string) (string, []string, error) {
	sep := string(filepath.Separator)
	cur := sep
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err == nil {
			cur, err = filepath.EvalSymlinks(wd)
		}
		if err != nil {
			return "", nil, fmt.Errorf("resolve %s: %w", path, err)
		}
	}
	var (
		links []string
		rest  = strings.Split(path, sep)
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
			return missingBuildInputPath(path, next, rest, links)
		}
		if err != nil {
			return "", nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.IsDir() && len(rest) > 0 {
				return "", nil, fmt.Errorf("resolve %s: %s: %w", path, next, syscall.ENOTDIR)
			}
			cur = next
			continue
		}
		if links = append(links, next); len(links) > maxLinkHops {
			return "", nil, fmt.Errorf("resolve %s: too many levels of symbolic links", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		if filepath.IsAbs(target) {
			cur = sep
		}
		rest = append(strings.Split(target, sep), rest...)
	}
	return cur, links, nil
}

// missingBuildInputPath returns the path a missing target would have, which a
// guest can create later, with the links followed so far, or an error when the rest of path steps back out of
// the missing directory with "..".
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
