//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fatih/color"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

// defaultBuilderDisk is the Docker data disk of the shared builder VM when
// [vm] builder_disk is unset.
const defaultBuilderDisk = "20g"

// builderDiskSize returns the size of the Docker data disk of the builder VM
// that builds ws's image: the shared builder takes [vm] builder_disk, the
// isolated one of a workspace its docker_disk.
func builderDiskSize(cfg *config.Config, ws *workspace.Resolved) (int64, error) {
	if ws.IsolatedBuild {
		return vmDockerDiskSize(ws.DockerDisk)
	}
	size := cfg.VM.BuilderDisk
	if size == "" {
		size = defaultBuilderDisk
	}
	return vmDiskSize("builder_disk", size)
}

// vmDiskSize parses a disk size setting like vmDockerDiskSize does, naming
// setting in its errors.
func vmDiskSize(setting, size string) (int64, error) {
	if size == "" {
		return 0, fmt.Errorf("empty %s", setting)
	}
	mult := int64(1)
	switch unit := strings.ToLower(size[len(size)-1:]); unit {
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	}
	digits := strings.TrimRight(size, "kmgKMG")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s %q", setting, size)
	}
	if n > (math.MaxInt64-(1<<20-1))/mult {
		return 0, fmt.Errorf("%s %q is too large", setting, size)
	}
	bytes := (n*mult + (1<<20 - 1)) &^ (1<<20 - 1)
	if bytes < vmMinDockerDiskSize {
		return 0, fmt.Errorf("%s %q is below the %d GiB minimum", setting, size, vmMinDockerDiskSize>>30)
	}
	return bytes, nil
}

// builderKeepIDs returns the IDs of the images the VM image cache at
// cacheDir holds, which the builder daemon keeps so that rebuilding an
// unchanged workspace skips the conversion. The cache names each workspace
// image after its Docker image ID without the sha256: prefix
// (see vm.RootFSRef); names starting with a dot are staging entries of
// interrupted imports.
func builderKeepIDs(cacheDir string) (map[string]bool, error) {
	entries, err := os.ReadDir(filepath.Join(cacheDir, "erofs", "images"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read workspace images of the VM image cache: %w", err)
	}
	keep := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			keep[e.Name()] = true
		}
	}
	return keep, nil
}

// maintainBuilderDaemon trims the build cache and removes obsolete jailoc
// images from the Docker daemon DOCKER_HOST selects, which must be one of a
// jailoc-owned builder VM. Failures only warn: the build does not depend on
// the maintenance.
func maintainBuilderDaemon(ctx context.Context, diskSize int64, phase string, stdout, stderr io.Writer) {
	warn := func(err error) {
		_, _ = color.New(color.FgYellow).Fprintf(stderr, "WARNING: builder maintenance (%s): %v\n", phase, err)
	}
	keep, err := builderKeepIDs(filepath.Join(vmRootDir(), "images"))
	if err != nil {
		warn(fmt.Errorf("skipped, the images in use are unknown: %w", err))
		return
	}
	res, err := docker.MaintainBuilder(ctx, diskSize, keep)
	if err != nil {
		warn(err)
	}
	_, _ = color.New(color.FgCyan).Fprintf(stdout, "Builder maintenance (%s): reclaimed %s of build cache, removed %d unused jailoc image(s)\n",
		phase, formatBytes(int64(min(res.CacheReclaimed, math.MaxInt64))), res.ImagesRemoved) //nolint:gosec // G115: clamped to MaxInt64
}

// withBuilderMaintenance runs build between two maintenance runs of the
// builder daemon, the second also after a failed build. A cancelled context
// skips the maintenance. The build's result and error are returned as they
// are; maintain reports its own failures.
func withBuilderMaintenance(ctx context.Context, maintain func(ctx context.Context, phase string), build func() (string, error)) (string, error) {
	if ctx.Err() == nil {
		maintain(ctx, "before build")
	}
	ref, err := build()
	if ctx.Err() == nil {
		maintain(ctx, "after build")
	}
	return ref, err
}
