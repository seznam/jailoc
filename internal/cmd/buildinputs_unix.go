//go:build darwin || linux

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
// of a URL Dockerfile read nothing local.
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
				inputs = append(inputs, buildInput{kind: inputBaseDockerfile, path: filepath.Clean(base)})
			}
		}
	}
	// The overlay is built only from a Dockerfile; without one the build
	// context is unused.
	dockerfile := strings.TrimSpace(ws.Dockerfile)
	if dockerfile == "" {
		return inputs, nil
	}
	local, err := docker.IsLocalDockerfile(dockerfile)
	if err != nil {
		return nil, fmt.Errorf("workspace %q dockerfile: %w", ws.Name, err)
	}
	if local {
		inputs = append(inputs, buildInput{kind: inputDockerfile, path: filepath.Clean(dockerfile)})
	}
	switch buildContext := strings.TrimSpace(ws.BuildContext); {
	case buildContext != "":
		inputs = append(inputs, buildInput{kind: inputContext, path: filepath.Clean(buildContext), dir: true})
	case local:
		inputs = append(inputs, buildInput{kind: inputContext, path: filepath.Dir(filepath.Clean(dockerfile)), dir: true})
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
// be resolved fails the check.
func checkBuildInputs(inputs []buildInput, exports []workspaceShare) error {
	var (
		writable []workspaceShare
		roots    []string
	)
	for _, e := range exports {
		if e.share.ReadOnly {
			continue
		}
		real, err := filepath.EvalSymlinks(e.share.HostPath)
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
		real := resolveExisting(in.path)
		for i, root := range roots {
			w := writable[i]
			switch {
			case pathWithin(real, root):
				errs = append(errs, fmt.Errorf("%s %s lies in the read-write share %s of workspace %q", in.kind, in.path, w.share.HostPath, w.workspace))
			case in.dir && pathWithin(root, real):
				errs = append(errs, fmt.Errorf("%s %s contains the read-write share %s of workspace %q", in.kind, in.path, w.share.HostPath, w.workspace))
			}
		}
		switch link, i, err := linkInside(in.path, roots); {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s %s: %w", in.kind, in.path, err))
		case link != "":
			w := writable[i]
			errs = append(errs, fmt.Errorf("%s %s runs through the symbolic link %q inside the read-write share %s of workspace %q", in.kind, in.path, link, w.share.HostPath, w.workspace))
		}
		if in.dir {
			continue
		}
		if info, err := os.Lstat(real); err == nil && info.Mode().IsRegular() && hardLinked(info) {
			errs = append(errs, fmt.Errorf("%s %s has other hard links, which a read-write share can expose; give it a file of its own", in.kind, in.path))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w\nkeep build inputs out of read-write shares: share directories without them, mount them read-only, or use a prebuilt image", errors.Join(errs...))
}
