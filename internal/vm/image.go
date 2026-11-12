package vm

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/stacklok/go-microvm/image"
)

// rootfsRepo names workspace images in the image cache. The tag is the
// image ID, so a reference always denotes the same content.
const rootfsRepo = "jailoc.local/rootfs"

// ImportPhase is the current phase of [ImportImage].
type ImportPhase int

const (
	// PhaseSave reads the `docker save` tarball and keeps uncached layer blobs.
	PhaseSave ImportPhase = iota
	// PhaseExtract unpacks new layers and composes the root filesystem.
	PhaseExtract
)

// ImportProgress reports how far [ImportImage] has progressed.
type ImportProgress struct {
	Phase         ImportPhase
	Bytes         int64 // tarball bytes read so far (PhaseSave)
	Layers, Total int   // new layers unpacked and total new layers (PhaseExtract)
}

// RootFSRef is the image-cache reference of a built workspace image.
func RootFSRef(imageID string) string {
	return rootfsRepo + ":" + strings.TrimPrefix(imageID, "sha256:")
}

// ImportImage extracts a built workspace image into the image cache under
// ref, unless it is already there. save writes the image as a `docker save`
// tarball in OCI layout (Docker Engine 25 or later). Layer blobs whose
// content is already in the layer cache are dropped while the tarball is
// read, so only new layers are kept on disk until they are unpacked.
func ImportImage(
	ctx context.Context,
	cacheDir, ref string,
	save func(io.Writer) error,
	progress func(ImportProgress),
) error {
	unlock, err := lockCache(cacheDir)
	if err != nil {
		return err
	}
	defer unlock()
	cache := image.NewCache(cacheDir)
	if rfs := cache.LookupRef(ref); rfs != nil {
		// Heals images unpacked before ownership was normalized.
		return normalizeOwnership(rfs.Path)
	}
	dir, err := os.MkdirTemp(cacheDir, ".save-*")
	if err != nil {
		return fmt.Errorf("create image layout directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	pr, pw := io.Pipe()
	saveErr := make(chan error, 1)
	go func() {
		err := save(pw)
		pw.CloseWithError(err)
		saveErr <- err
	}()
	reader := newProgressReader(pr, progress)
	unpackErr := unpackLayout(ctx, reader, dir, cache.LayerCache())
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
	img, err = imageWithProgress(img, cache.LayerCache(), progress)
	if err != nil {
		return err
	}
	rfs, err := image.PullWithFetcher(ctx, ref, cache, staticFetcher{img: img})
	if err != nil {
		return fmt.Errorf("unpack workspace image: %w", err)
	}
	return normalizeOwnership(rfs.Path)
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

type progressImage struct {
	v1.Image
	lc       *image.LayerCache
	diffIDs  []v1.Hash
	mu       sync.Mutex
	adjusted bool
	done     int
	total    int
	progress func(ImportProgress)
}

func imageWithProgress(img v1.Image, lc *image.LayerCache, progress func(ImportProgress)) (v1.Image, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("read image layers: %w", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read image config: %w", err)
	}
	if len(layers) != len(cfg.RootFS.DiffIDs) {
		return nil, fmt.Errorf("image has %d layers but %d DiffIDs", len(layers), len(cfg.RootFS.DiffIDs))
	}
	uncached := make(map[v1.Hash]bool, len(layers))
	for _, diffID := range cfg.RootFS.DiffIDs {
		if !lc.Has(diffID) {
			uncached[diffID] = true
		}
	}
	p := &progressImage{
		Image:    img,
		lc:       lc,
		diffIDs:  cfg.RootFS.DiffIDs,
		total:    len(uncached),
		progress: progress,
	}
	p.reportInitial()
	return p, nil
}

func (p *progressImage) Layers() ([]v1.Layer, error) {
	layers, err := p.Image.Layers()
	if err != nil {
		return nil, err
	}
	for i, layer := range layers {
		layers[i] = &progressLayer{Layer: layer, index: i, image: p}
	}
	return layers, nil
}

func (p *progressImage) reportInitial() {
	if p.progress != nil {
		p.progress(ImportProgress{Phase: PhaseExtract, Layers: 0, Total: p.total})
	}
}

func (p *progressImage) adjustStart(index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.adjusted {
		return
	}
	p.adjusted = true
	uncached := make(map[v1.Hash]bool, len(p.diffIDs)-index)
	for _, diffID := range p.diffIDs[index:] {
		if !p.lc.Has(diffID) {
			uncached[diffID] = true
		}
	}
	if len(uncached) < p.total {
		p.total = len(uncached)
		if p.progress != nil {
			p.progress(ImportProgress{Phase: PhaseExtract, Layers: p.done, Total: p.total})
		}
	}
}

func (p *progressImage) layerDone() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done++
	if p.done > p.total {
		p.total = p.done
	}
	if p.progress != nil {
		p.progress(ImportProgress{Phase: PhaseExtract, Layers: p.done, Total: p.total})
	}
}

type progressLayer struct {
	v1.Layer
	index int
	image *progressImage
	once  sync.Once
}

func (l *progressLayer) Uncompressed() (io.ReadCloser, error) {
	l.image.adjustStart(l.index)
	r, err := l.Layer.Uncompressed()
	if err != nil {
		return nil, err
	}
	return &progressReadCloser{ReadCloser: r, done: func() {
		l.once.Do(l.image.layerDone)
	}}, nil
}

type progressReadCloser struct {
	io.ReadCloser
	done func()
}

func (r *progressReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.done()
	return err
}

// unpackLayout writes the OCI layout files of a `docker save` tarball to dir,
// dropping blobs that are layers already in lc.
func unpackLayout(ctx context.Context, r io.Reader, dir string, lc *image.LayerCache) error {
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
		if blob && lc.Has(v1.Hash{Algorithm: "sha256", Hex: path.Base(name)}) {
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
		if ok && lc.Has(diffID) {
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
