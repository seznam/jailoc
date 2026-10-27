package vm

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/stacklok/go-microvm/image"
)

// rootfsRepo names workspace images in the image cache. The tag is the
// image ID, so a reference always denotes the same content.
const rootfsRepo = "jailoc.local/rootfs"

// RootFSRef is the image-cache reference of a built workspace image.
func RootFSRef(imageID string) string {
	return rootfsRepo + ":" + strings.TrimPrefix(imageID, "sha256:")
}

// ImportImage extracts a built workspace image into the image cache under
// ref, unless it is already there. save writes the image as a `docker save`
// tarball.
func ImportImage(ctx context.Context, cacheDir, ref string, save func(io.Writer) error) error {
	unlock, err := lockCache(cacheDir)
	if err != nil {
		return err
	}
	defer unlock()
	cache := image.NewCache(cacheDir)
	if cache.LookupRef(ref) != nil {
		return nil
	}
	tmp, err := os.CreateTemp(cacheDir, ".save-*.tar")
	if err != nil {
		return fmt.Errorf("create image tarball: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := save(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close image tarball: %w", err)
	}
	img, err := tarball.ImageFromPath(tmp.Name(), nil)
	if err != nil {
		return fmt.Errorf("read image tarball: %w", err)
	}
	if _, err := image.PullWithFetcher(ctx, ref, cache, staticFetcher{img: img}); err != nil {
		return fmt.Errorf("unpack workspace image: %w", err)
	}
	return nil
}

type staticFetcher struct{ img v1.Image }

func (f staticFetcher) Pull(context.Context, string) (v1.Image, error) { return f.img, nil }

// ImageHasFile reports whether the unpacked image ref in the image cache
// contains any of the given paths (relative to the root file system).
func ImageHasFile(cacheDir, ref string, paths ...string) (bool, error) {
	rootfs := image.NewCache(cacheDir).LookupRef(ref)
	if rootfs == nil {
		return false, fmt.Errorf("image %s is not in the image cache", ref)
	}
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(rootfs.Path, filepath.FromSlash(p))); err == nil {
			return true, nil
		}
	}
	return false, nil
}
