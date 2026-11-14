package vm

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/erofs/go-erofs"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/stacklok/go-microvm/image"
)

// rootfsRepo names workspace images in the image cache. The tag is the
// image ID, so a reference always denotes the same content.
const rootfsRepo = "jailoc.local/erofs"

// legacyRootfsRepo names workspace images unpacked into directories by
// go-microvm's image cache before jailoc converted layers to EROFS. VMs
// started by older releases still run from them until they restart.
const legacyRootfsRepo = "jailoc.local/rootfs"

// The EROFS image cache lives in erofsDir of the VM image cache: one EROFS
// image per layer in layersDir, named after the layer's DiffID, and one
// directory per workspace image in imagesDir, named after its tag, with hard
// links to its layers and the image manifest.
const (
	erofsDir          = "erofs"
	layersDir         = "layers"
	imagesDir         = "images"
	imageManifestFile = "image.json"
)

// ImportPhase is the current phase of [ImportImage].
type ImportPhase int

const (
	// PhaseSave reads the `docker save` tarball and keeps uncached layer blobs.
	PhaseSave ImportPhase = iota
	// PhaseExtract converts new layers to EROFS images.
	PhaseExtract
)

// ImportProgress reports how far [ImportImage] has progressed.
type ImportProgress struct {
	Phase         ImportPhase
	Bytes         int64 // tarball bytes read so far (PhaseSave)
	Layers, Total int   // new layers converted and total new layers (PhaseExtract)
}

// RootFSRef is the image-cache reference of a built workspace image.
func RootFSRef(imageID string) string {
	return rootfsRepo + ":" + strings.TrimPrefix(imageID, "sha256:")
}

// imageManifest describes a workspace image in the EROFS image cache.
type imageManifest struct {
	// Layers are the file names of the layer images in the image
	// directory, the bottom layer first.
	Layers []string `json:"layers"`
	// Env is the image's environment (Dockerfile ENV).
	Env []string `json:"env,omitempty"`
}

// imageTag matches the tags of workspace image references, which name the
// image directories.
var imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// imageDir returns the directory of the workspace image ref in the image
// cache.
func imageDir(cacheDir, ref string) (string, error) {
	tag, ok := strings.CutPrefix(ref, rootfsRepo+":")
	if !ok || !imageTag.MatchString(tag) {
		return "", fmt.Errorf("image %s is not a workspace image reference", ref)
	}
	return filepath.Join(cacheDir, erofsDir, imagesDir, tag), nil
}

func layerPath(cacheDir string, diffID v1.Hash) string {
	return filepath.Join(cacheDir, erofsDir, layersDir, diffID.Hex+".erofs")
}

// readImage returns the directory and manifest of the workspace image ref.
func readImage(cacheDir, ref string) (string, imageManifest, error) {
	dir, err := imageDir(cacheDir, ref)
	if err != nil {
		return "", imageManifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, imageManifestFile)) //nolint:gosec // G304: file inside jailoc's image cache
	if errors.Is(err, fs.ErrNotExist) {
		return "", imageManifest{}, fmt.Errorf("image %s is not in the image cache; run jailoc up to build it", ref)
	}
	if err != nil {
		return "", imageManifest{}, fmt.Errorf("read image %s: %w", ref, err)
	}
	var m imageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return "", imageManifest{}, fmt.Errorf("parse image %s: %w", ref, err)
	}
	if len(m.Layers) == 0 {
		return "", imageManifest{}, fmt.Errorf("image %s has no layers", ref)
	}
	for _, l := range m.Layers {
		if !imageTag.MatchString(l) {
			return "", imageManifest{}, fmt.Errorf("image %s has an invalid layer name %q", ref, l)
		}
	}
	return dir, m, nil
}

// ImportImage converts a built workspace image into EROFS layer images in
// the image cache under ref, unless it is already there. save writes the
// image as a `docker save` tarball in OCI layout (Docker Engine 25 or
// later). Layer blobs whose content is already in the cache are dropped
// while the tarball is read, so only new layers are kept on disk until they
// are converted.
func ImportImage(
	ctx context.Context,
	cacheDir, ref string,
	save func(io.Writer) error,
	progress func(ImportProgress),
) error {
	dst, err := imageDir(cacheDir, ref)
	if err != nil {
		return err
	}
	unlock, err := lockCache(cacheDir)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(dst, imageManifestFile)); err == nil {
		// A fresh modification time gives the image the pruning grace period
		// until the workspace that imports it records it in its spec.
		now := time.Now()
		if err := os.Chtimes(dst, now, now); err != nil {
			return fmt.Errorf("refresh cached image: %w", err)
		}
		return nil
	}
	for _, d := range []string{layersDir, imagesDir} {
		if err := os.MkdirAll(filepath.Join(cacheDir, erofsDir, d), 0o700); err != nil {
			return fmt.Errorf("create image cache: %w", err)
		}
	}
	dir, err := os.MkdirTemp(cacheDir, ".save-*")
	if err != nil {
		return fmt.Errorf("create image layout directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	hasLayer := func(diffID v1.Hash) bool {
		_, err := os.Stat(layerPath(cacheDir, diffID))
		return err == nil
	}
	pr, pw := io.Pipe()
	saveErr := make(chan error, 1)
	go func() {
		err := save(pw)
		pw.CloseWithError(err)
		saveErr <- err
	}()
	reader := newProgressReader(pr, progress)
	unpackErr := unpackLayout(ctx, reader, dir, hasLayer)
	reader.reportFinal()
	// Unblock save if unpacking stopped early; its error is then only the
	// consequence of the unpack error.
	_ = pr.CloseWithError(errors.New("image tarball reader closed"))
	err = <-saveErr
	if unpackErr != nil {
		return unpackErr
	}
	if err != nil {
		return err
	}

	img, err := layoutImage(dir)
	if err != nil {
		return err
	}
	return convertImage(ctx, cacheDir, dst, img, hasLayer, progress)
}

// convertImage converts the layers of img missing from the cache and
// records img in the image directory dst.
func convertImage(ctx context.Context, cacheDir, dst string, img v1.Image, hasLayer func(v1.Hash) bool, progress func(ImportProgress)) error {
	cfg, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("read image config: %w", err)
	}
	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("read image layers: %w", err)
	}
	diffIDs := cfg.RootFS.DiffIDs
	if len(layers) != len(diffIDs) {
		return fmt.Errorf("image has %d layers but %d DiffIDs", len(layers), len(diffIDs))
	}
	if len(layers) == 0 {
		return errors.New("image has no layers")
	}
	var missing []int
	for i, d := range diffIDs {
		if !hasLayer(d) && !slices.ContainsFunc(missing, func(j int) bool { return diffIDs[j] == d }) {
			missing = append(missing, i)
		}
	}
	report := func(done int) {
		if progress != nil {
			progress(ImportProgress{Phase: PhaseExtract, Layers: done, Total: len(missing)})
		}
	}
	report(0)
	for n, i := range missing {
		if err := convertLayer(ctx, cacheDir, layers[i], diffIDs[i]); err != nil {
			return err
		}
		report(n + 1)
	}

	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create image directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	m := imageManifest{Env: cfg.Config.Env}
	for i, d := range diffIDs {
		name := fmt.Sprintf("%03d.erofs", i)
		if err := os.Link(layerPath(cacheDir, d), filepath.Join(tmp, name)); err != nil {
			return fmt.Errorf("link image layer: %w", err)
		}
		m.Layers = append(m.Layers, name)
	}
	if err := addDirFixup(tmp, &m); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode image manifest: %w", err)
	}
	if err := writeSynced(filepath.Join(tmp, imageManifestFile), data); err != nil {
		return err
	}
	// The directory is shared read-only with the VM; libkrun only needs to
	// traverse it.
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // G302: inside the private image cache
		return fmt.Errorf("create image directory: %w", err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("replace image directory: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("create image directory: %w", err)
	}
	return nil
}

// addDirFixup adds the directory fixup layer of the image in the image
// directory dir to it and m, when the image needs one.
func addDirFixup(dir string, m *imageManifest) error {
	layers := make([]fs.FS, 0, len(m.Layers))
	for _, name := range m.Layers {
		f, err := os.Open(filepath.Join(dir, name)) //nolint:gosec // G304: layer inside jailoc's image cache
		if err != nil {
			return fmt.Errorf("open image layer: %w", err)
		}
		defer func() { _ = f.Close() }()
		img, err := erofs.Open(f)
		if err != nil {
			return fmt.Errorf("open image layer %s: %w", name, err)
		}
		layers = append(layers, img)
	}
	dirs, err := imageDirFixup(layers)
	if err != nil || len(dirs) == 0 {
		return err
	}
	name := fmt.Sprintf("%03d.erofs", len(m.Layers))
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o444) //nolint:gosec // G304: path inside jailoc's image cache
	if err != nil {
		return fmt.Errorf("create directory fixup layer: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := writeDirFixup(dirs, f, dir); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write directory fixup layer: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write directory fixup layer: %w", err)
	}
	m.Layers = append(m.Layers, name)
	return nil
}

// convertLayer converts layer to its EROFS image in the cache, checking that
// its content matches diffID.
func convertLayer(ctx context.Context, cacheDir string, layer v1.Layer, diffID v1.Hash) error {
	rc, err := layer.Uncompressed()
	if err != nil {
		return fmt.Errorf("read layer %s: %w", diffID, err)
	}
	defer func() { _ = rc.Close() }()
	dir := filepath.Dir(layerPath(cacheDir, diffID))
	f, err := os.CreateTemp(dir, ".tmp-*.erofs")
	if err != nil {
		return fmt.Errorf("create layer image: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	h := sha256.New()
	r := io.TeeReader(ctxReader{ctx: ctx, r: rc}, h)
	if err := writeLayerEROFS(r, f, dir); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("convert layer %s: %w", diffID, ctxErr)
		}
		return fmt.Errorf("convert layer %s: %w", diffID, err)
	}
	// The tar reader stops at the end-of-archive marker; the DiffID also
	// covers the padding after it.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return fmt.Errorf("read layer %s: %w", diffID, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); diffID.Algorithm != "sha256" || got != diffID.Hex {
		return fmt.Errorf("layer %s has content sha256:%s", diffID, got)
	}
	if err := f.Chmod(0o444); err != nil {
		return fmt.Errorf("write layer image: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write layer image: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write layer image: %w", err)
	}
	if err := os.Rename(f.Name(), layerPath(cacheDir, diffID)); err != nil {
		return fmt.Errorf("write layer image: %w", err)
	}
	return nil
}

// writeSynced writes data to the new file path and syncs it.
func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) //nolint:gosec // G304: path inside jailoc's image cache
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

const progressByteInterval = 1 << 20

type progressReader struct {
	r        io.Reader
	progress func(ImportProgress)
	bytes    atomic.Int64
	reported int64
}

func newProgressReader(r io.Reader, progress func(ImportProgress)) *progressReader {
	reader := &progressReader{r: r, progress: progress}
	reader.report(0)
	return reader
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	bytes := r.bytes.Add(int64(n))
	if bytes-r.reported >= progressByteInterval {
		r.report(bytes)
	}
	return n, err
}

func (r *progressReader) reportFinal() {
	if bytes := r.bytes.Load(); bytes != r.reported {
		r.report(bytes)
	}
}

func (r *progressReader) report(bytes int64) {
	r.reported = bytes
	if r.progress != nil {
		r.progress(ImportProgress{Phase: PhaseSave, Bytes: bytes})
	}
}

// unpackLayout writes the OCI layout files of a `docker save` tarball to dir,
// dropping blobs that are layers hasLayer reports as cached.
func unpackLayout(ctx context.Context, r io.Reader, dir string, hasLayer func(v1.Hash) bool) error {
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("read image tarball: %w", err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read image tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(hdr.Name)
		blob := strings.HasPrefix(name, "blobs/sha256/") && validHex(strings.TrimPrefix(name, "blobs/sha256/"))
		if !blob && !slices.Contains(layoutFiles, name) {
			continue
		}
		// An uncompressed layer's digest is its DiffID: skip it unread.
		if blob && hasLayer(v1.Hash{Algorithm: "sha256", Hex: path.Base(name)}) {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := writeFile(target, tr, hdr.Size, 0o600); err != nil {
			return err
		}
		if !blob {
			continue
		}
		diffID, ok, err := layerDiffID(ctx, target)
		if err != nil {
			return err
		}
		if ok && hasLayer(diffID) {
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("drop cached layer blob: %w", err)
			}
		}
	}
}

// layoutFiles are the non-blob files of an OCI layout `docker save` writes.
var layoutFiles = []string{"index.json", "oci-layout"}

func validHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// layerDiffID returns the digest of the uncompressed content of a layer blob.
// ok is false for blobs that are not uncompressed or gzip-compressed tar
// archives (image configs, manifests, other compressions); they are kept.
func layerDiffID(ctx context.Context, blob string) (v1.Hash, bool, error) {
	f, err := os.Open(blob) //nolint:gosec // G304: blob is a sha256 path inside jailoc's own temp layout
	if err != nil {
		return v1.Hash{}, false, fmt.Errorf("open layer blob: %w", err)
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReaderSize(f, 1<<20)
	var r io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return v1.Hash{}, false, nil //nolint:nilerr // not a gzip layer; keep the blob
		}
		defer func() { _ = zr.Close() }()
		r = zr
	} else if header, _ := br.Peek(tarMagicEnd); len(header) < tarMagicEnd || string(header[257:262]) != "ustar" {
		return v1.Hash{}, false, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx: ctx, r: r}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return v1.Hash{}, false, fmt.Errorf("hash layer blob: %w", ctxErr)
		}
		// A blob that fails to decompress is not a layer jailoc can match;
		// keep it and let the unpacker report real corruption.
		return v1.Hash{}, false, nil //nolint:nilerr // see above
	}
	return v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(h.Sum(nil))}, true, nil
}

// ctxReader stops reading once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// tarMagicEnd is the end offset of the "ustar" magic in a tar header.
const tarMagicEnd = 262

// layoutImage returns the image for the host architecture from the OCI
// layout in dir.
func layoutImage(dir string) (v1.Image, error) {
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		return nil, fmt.Errorf("docker save did not write an OCI image layout; jailoc needs Docker Engine 25 or later: %w", err)
	}
	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		return nil, fmt.Errorf("read image layout: %w", err)
	}
	return indexImage(idx)
}

func indexImage(idx v1.ImageIndex) (v1.Image, error) {
	manifest, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("read image index: %w", err)
	}
	for _, desc := range manifest.Manifests {
		if desc.Platform != nil && (desc.Platform.OS != "linux" || desc.Platform.Architecture != runtime.GOARCH) {
			continue
		}
		switch {
		case desc.MediaType.IsImage():
			img, err := idx.Image(desc.Digest)
			if err != nil {
				return nil, fmt.Errorf("read image %s: %w", desc.Digest, err)
			}
			return img, nil
		case desc.MediaType.IsIndex():
			child, err := idx.ImageIndex(desc.Digest)
			if err != nil {
				return nil, fmt.Errorf("read image index %s: %w", desc.Digest, err)
			}
			if img, err := indexImage(child); err == nil {
				return img, nil
			}
		}
	}
	return nil, fmt.Errorf("image layout has no linux/%s image", runtime.GOARCH)
}

// ImageHasFile reports whether the workspace image ref in the image cache
// contains any of the given paths (relative to the root file system). It
// looks the paths up in the layers the way overlayfs merges them, following
// symlinks across layers.
func ImageHasFile(cacheDir, ref string, paths ...string) (bool, error) {
	if strings.HasPrefix(ref, legacyRootfsRepo+":") {
		return legacyImageHasFile(cacheDir, ref, paths...)
	}
	dir, m, err := readImage(cacheDir, ref)
	if err != nil {
		return false, err
	}
	layers := make([]fs.FS, 0, len(m.Layers))
	for _, name := range slices.Backward(m.Layers) {
		f, err := os.Open(filepath.Join(dir, name)) //nolint:gosec // G304: layer inside jailoc's image cache
		if err != nil {
			return false, fmt.Errorf("open image layer: %w", err)
		}
		defer func() { _ = f.Close() }()
		img, err := erofs.Open(f)
		if err != nil {
			return false, fmt.Errorf("open image layer %s: %w", name, err)
		}
		layers = append(layers, img)
	}
	for _, p := range paths {
		if found, err := layersHavePath(layers, path.Clean(p)); err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// maxSymlinks bounds the symlinks followed while looking up one path, like
// the kernel's limit.
const maxSymlinks = 40

// layersHavePath reports whether name exists in the file system that the
// overlay of layers (the top first) presents. Symlinks are followed across
// layers as path resolution in the guest follows them, including one at
// name itself.
func layersHavePath(layers []fs.FS, name string) (bool, error) {
	dir := "" // the resolved directory, "" for the root
	rest := strings.Split(name, "/")
	for links := 0; len(rest) > 0; {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if dir = path.Dir(dir); dir == "." {
				dir = ""
			}
			continue
		}
		p := path.Join(dir, c)
		fi, layer, err := mergedLstat(layers, p)
		if err != nil || fi == nil {
			return false, err
		}
		switch {
		case fi.Mode().Type() == fs.ModeSymlink:
			if links++; links > maxSymlinks {
				return false, nil
			}
			target, err := fs.ReadLink(layer, p)
			if err != nil {
				return false, fmt.Errorf("read link %s: %w", p, err)
			}
			if path.IsAbs(target) {
				dir = ""
			}
			rest = append(strings.Split(target, "/"), rest...)
		case len(rest) > 0 && !fi.IsDir():
			return false, nil
		default:
			dir = p
		}
	}
	return true, nil
}

// mergedLstat looks p up in the layers the way overlayfs does, given that
// every ancestor of p is a directory in the merged file system. It returns
// the entry of the topmost layer that has p and that layer, or a nil entry
// when p does not exist.
func mergedLstat(layers []fs.FS, p string) (fs.FileInfo, fs.FS, error) {
	parts := strings.Split(p, "/")
layers:
	for _, l := range layers {
		// An opaque ancestor hides the layers below this one; an ancestor
		// that is no directory in this layer hides p and the layers below.
		opaque := false
		for i := range len(parts) {
			dir := "."
			if i > 0 {
				dir = path.Join(parts[:i]...)
			}
			fi, err := layerLstat(l, dir)
			switch {
			case err != nil:
				return nil, nil, err
			case fi == nil && opaque:
				return nil, nil, nil
			case fi == nil:
				continue layers
			case !fi.IsDir():
				return nil, nil, nil
			}
			opaque = opaque || isOpaque(fi)
		}
		fi, err := layerLstat(l, p)
		switch {
		case err != nil:
			return nil, nil, err
		case fi != nil && isWhiteout(fi):
			return nil, nil, nil
		case fi != nil:
			return fi, l, nil
		case opaque:
			return nil, nil, nil
		}
	}
	return nil, nil, nil
}

// layerLstat is lstat in one layer, with a nil entry when name is missing.
func layerLstat(l fs.FS, name string) (fs.FileInfo, error) {
	fi, err := lstat(l, name)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, erofs.ErrNotDirectory) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", name, err)
	}
	return fi, nil
}

func lstat(fsys fs.FS, name string) (fs.FileInfo, error) {
	l, ok := fsys.(interface {
		Lstat(string) (fs.FileInfo, error)
	})
	if !ok {
		return fs.Stat(fsys, name)
	}
	return l.Lstat(name)
}

// isWhiteout reports whether fi is an overlayfs whiteout: a character device
// with device number 0.
func isWhiteout(fi fs.FileInfo) bool {
	d, ok := fi.(interface{ Rdev() uint64 })
	return fi.Mode().Type() == fs.ModeDevice|fs.ModeCharDevice && ok && d.Rdev() == 0
}

func isOpaque(fi fs.FileInfo) bool {
	x, ok := fi.(interface {
		GetXattr(string) (string, bool)
	})
	if !ok {
		return false
	}
	v, _ := x.GetXattr(overlayOpaque)
	return v == "y"
}

// legacyImageHasFile is ImageHasFile for images unpacked by go-microvm.
func legacyImageHasFile(cacheDir, ref string, paths ...string) (bool, error) {
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
