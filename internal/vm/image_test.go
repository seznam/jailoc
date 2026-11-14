package vm

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// saveTarball returns img as an OCI layout tarball like `docker save` writes.
func saveTarball(t *testing.T, img v1.Image) []byte {
	t.Helper()
	dir := t.TempDir()
	p, err := layout.Write(dir, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendImage(img); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: test fixture path
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0o444, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportImageSkipsCachedLayers(t *testing.T) {
	t.Parallel()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	saved := saveTarball(t, img)
	save := func(w io.Writer) error {
		_, err := w.Write(saved)
		return err
	}
	cacheDir := t.TempDir()
	ref := RootFSRef("sha256:1111")

	if err := ImportImage(context.Background(), cacheDir, ref, save, nil); err != nil {
		t.Fatalf("ImportImage: %v", err)
	}
	if _, _, err := readImage(cacheDir, ref); err != nil {
		t.Fatalf("%s not in the image cache after import: %v", ref, err)
	}

	// A second save of an image with the same layers keeps only non-layer blobs.
	dir := t.TempDir()
	hasLayer := func(h v1.Hash) bool {
		_, err := os.Stat(layerPath(cacheDir, h))
		return err == nil
	}
	if err := unpackLayout(t.Context(), bytes.NewReader(saved), dir, hasLayer); err != nil {
		t.Fatalf("unpackLayout: %v", err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range layers {
		d, err := l.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "blobs", "sha256", d.Hex)); !os.IsNotExist(err) {
			t.Errorf("cached layer blob %s kept: %v", d, err)
		}
	}
	cfg, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs", "sha256", cfg.Hex)); err != nil {
		t.Errorf("config blob dropped: %v", err)
	}
	if _, err := layoutImage(dir); err != nil {
		t.Errorf("layoutImage after dropping cached layers: %v", err)
	}
}

func TestImportImageReportsProgress(t *testing.T) {
	t.Parallel()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	saved := saveTarball(t, img)
	save := func(w io.Writer) error {
		_, err := w.Write(saved)
		return err
	}
	cacheDir := t.TempDir()

	var mu sync.Mutex
	progress := []ImportProgress{}
	report := func(p ImportProgress) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, p)
	}
	if err := ImportImage(
		context.Background(),
		cacheDir,
		RootFSRef("sha256:progress-new"),
		save,
		report,
	); err != nil {
		t.Fatalf("ImportImage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var finalSave, finalExtract ImportProgress
	for _, p := range progress {
		switch p.Phase {
		case PhaseSave:
			finalSave = p
		case PhaseExtract:
			finalExtract = p
		}
	}
	if finalSave.Bytes != int64(len(saved)) {
		t.Errorf("final save bytes = %d, want %d", finalSave.Bytes, len(saved))
	}
	if finalExtract.Total != 2 || finalExtract.Layers != finalExtract.Total {
		t.Errorf("final extract progress = %+v, want 2/2 layers", finalExtract)
	}
}

func TestImportImageReportsNoUncachedLayers(t *testing.T) {
	t.Parallel()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	saved := saveTarball(t, img)
	save := func(w io.Writer) error {
		_, err := w.Write(saved)
		return err
	}
	cacheDir := t.TempDir()
	if err := ImportImage(context.Background(), cacheDir, RootFSRef("sha256:cached-source"), save, nil); err != nil {
		t.Fatalf("prime cache: %v", err)
	}

	var mu sync.Mutex
	progress := []ImportProgress{}
	if err := ImportImage(
		context.Background(),
		cacheDir,
		RootFSRef("sha256:cached-destination"),
		save,
		func(p ImportProgress) {
			mu.Lock()
			defer mu.Unlock()
			progress = append(progress, p)
		},
	); err != nil {
		t.Fatalf("ImportImage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var extracts []ImportProgress
	for _, p := range progress {
		if p.Phase == PhaseExtract {
			extracts = append(extracts, p)
		}
	}
	if len(extracts) != 1 || extracts[0].Layers != 0 || extracts[0].Total != 0 {
		t.Errorf("extract progress = %+v, want one 0/0 event", extracts)
	}
}
func TestImportImageRejectsLegacyTarball(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o444, Size: 2, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("[]")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	err := ImportImage(context.Background(), t.TempDir(), RootFSRef("sha256:2222"), func(w io.Writer) error {
		_, err := w.Write(buf.Bytes())
		return err
	}, nil)
	if err == nil {
		t.Fatal("ImportImage accepted a tarball without an OCI layout")
	}
	if !strings.Contains(err.Error(), "Docker Engine 25") {
		t.Errorf("error %q does not name the Docker Engine requirement", err)
	}
}

func TestImportImageReportsUnpackError(t *testing.T) {
	t.Parallel()
	// save keeps writing after the unpacker gave up; the user must see why
	// unpacking failed, not that save lost its reader.
	err := ImportImage(context.Background(), t.TempDir(), RootFSRef("sha256:3333"), func(w io.Writer) error {
		_, err := w.Write(bytes.Repeat([]byte("not a tar archive "), 1<<16))
		return err
	}, nil)
	if err == nil {
		t.Fatal("ImportImage accepted a corrupt tarball")
	}
	if !strings.Contains(err.Error(), "read image tarball") {
		t.Errorf("error %q is not the unpack error", err)
	}
}

func TestImportImageStopsWhenCancelled(t *testing.T) {
	t.Parallel()
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	saved := saveTarball(t, img)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = ImportImage(ctx, t.TempDir(), RootFSRef("sha256:4444"), func(w io.Writer) error {
		_, err := w.Write(saved)
		return err
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ImportImage after cancel = %v, want context.Canceled", err)
	}
}

// layeredImage returns an image of the layer tarballs, the bottom first,
// with env as its environment.
func layeredImage(t *testing.T, env []string, layers ...[]byte) v1.Image {
	t.Helper()
	img := empty.Image
	for _, data := range layers {
		l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if img, err = mutate.AppendLayers(img, l); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.Config.Env = env
	if img, err = mutate.ConfigFile(img, cfg); err != nil {
		t.Fatal(err)
	}
	return img
}

func TestImportImageEROFS(t *testing.T) {
	t.Parallel()
	base := layerTar(t,
		layerEntry{hdr: tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/dockerd", Typeflag: tar.TypeReg, Mode: 0o755}, data: "d"},
		layerEntry{hdr: tar.Header{Name: "opt/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "opt/tool", Typeflag: tar.TypeReg, Mode: 0o755}, data: "t"},
		layerEntry{hdr: tar.Header{Name: "srv/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "srv/data", Typeflag: tar.TypeReg, Mode: 0o644}, data: "s"},
	)
	top := layerTar(t,
		layerEntry{hdr: tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/.wh.dockerd", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "opt/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "opt/.wh..wh..opq", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "srv", Typeflag: tar.TypeSymlink, Linkname: "/var/srv"}},
	)
	env := []string{"PATH=/opt/bin:/usr/bin", "TZ=Europe/Prague"}
	cacheDir := t.TempDir()
	importImg := func(ref string, img v1.Image) {
		t.Helper()
		saved := saveTarball(t, img)
		if err := ImportImage(context.Background(), cacheDir, ref, func(w io.Writer) error {
			_, err := w.Write(saved)
			return err
		}, nil); err != nil {
			t.Fatalf("ImportImage %s: %v", ref, err)
		}
	}
	baseRef, topRef := RootFSRef("sha256:base"), RootFSRef("sha256:top")
	importImg(baseRef, layeredImage(t, nil, base))
	importImg(topRef, layeredImage(t, env, base, top))

	dir, m, err := readImage(cacheDir, topRef)
	if err != nil {
		t.Fatalf("readImage: %v", err)
	}
	if want := []string{"000.erofs", "001.erofs"}; !slices.Equal(m.Layers, want) {
		t.Errorf("layers = %v, want %v", m.Layers, want)
	}
	if !slices.Equal(m.Env, env) {
		t.Errorf("env = %v, want %v", m.Env, env)
	}
	info, err := os.Stat(filepath.Join(dir, "000.erofs"))
	if err != nil {
		t.Fatal(err)
	}
	// The base layer is converted once and linked from both images.
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 3 {
		t.Errorf("base layer has %v links, want 3 (cache and two images)", info.Sys())
	}
	entries, err := os.ReadDir(filepath.Join(cacheDir, erofsDir, layersDir))
	if err != nil || len(entries) != 2 {
		t.Errorf("layer cache holds %d entries (%v), want 2", len(entries), err)
	}

	for _, tt := range []struct {
		ref, path string
		want      bool
	}{
		{baseRef, "usr/bin/dockerd", true},
		{topRef, "usr/bin/dockerd", false},
		{baseRef, "opt/tool", true},
		{topRef, "opt/tool", false},
		{topRef, "srv/data", false},
		{topRef, "usr/bin", true},
		{topRef, "missing", false},
	} {
		got, err := ImageHasFile(cacheDir, tt.ref, tt.path)
		if err != nil || got != tt.want {
			t.Errorf("ImageHasFile(%s, %s) = %v, %v; want %v", tt.ref, tt.path, got, err, tt.want)
		}
	}
	if _, err := ImageHasFile(cacheDir, RootFSRef("sha256:missing"), "x"); err == nil {
		t.Error("ImageHasFile of a missing image succeeded")
	}
}

func TestImageDirRejectsForeignRefs(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"docker@sha256:feed", legacyRootfsRepo + ":aa", rootfsRepo + ":../x", rootfsRepo + ":", rootfsRepo + ":.hidden"} {
		if _, err := imageDir("/cache", ref); err == nil {
			t.Errorf("imageDir(%q) succeeded", ref)
		}
	}
}

func TestImportImageRefreshesCachedImage(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	ref := RootFSRef("sha256:cached")
	saved := saveTarball(t, layeredImage(t, nil, layerTar(t,
		layerEntry{hdr: tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755}},
	)))
	importImg := func() {
		t.Helper()
		if err := ImportImage(context.Background(), cacheDir, ref, func(w io.Writer) error {
			_, err := w.Write(saved)
			return err
		}, nil); err != nil {
			t.Fatalf("ImportImage: %v", err)
		}
	}
	importImg()
	dir, err := imageDir(cacheDir, ref)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatal(err)
	}

	// A cache hit gives the image a new grace period, so a prune before the
	// workspace records the image in its spec keeps it.
	importImg()
	if _, err := PruneImages(cacheDir, nil); err != nil {
		t.Fatalf("PruneImages: %v", err)
	}
	if _, _, err := readImage(cacheDir, ref); err != nil {
		t.Errorf("cached image pruned after a cache hit: %v", err)
	}
}

func TestLayersHavePath(t *testing.T) {
	t.Parallel()
	dir := func(name string) layerEntry {
		return layerEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}}
	}
	file := func(name string) layerEntry {
		return layerEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755}, data: "x"}
	}
	link := func(name, target string) layerEntry {
		return layerEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target}}
	}
	bottom := layerImage(t, layerTar(t,
		dir("opt/"), dir("opt/docker/"), dir("opt/docker/bin/"), file("opt/docker/bin/dockerd"),
		dir("usr/"), dir("usr/bin/"), file("usr/bin/real"),
	))
	middle := layerImage(t, layerTar(t,
		dir("usr/"), dir("usr/local/"), link("usr/local/bin", "/opt/docker/bin"),
		link("bin", "usr/bin"),
		dir("rel/"), link("rel/up", "../usr/bin"),
		link("loop-a", "loop-b"), link("loop-b", "loop-a"),
		link("dangling", "/nope"),
		link("tool", "usr/bin/real"),
	))
	opaqueTop := layerImage(t, layerTar(t,
		dir("opt/"), file("opt/.wh..wh..opq"), file("opt/other"),
	))
	rootOpaqueTop := layerImage(t, layerTar(t,
		file(".wh..wh..opq"), dir("usr/"), dir("usr/bin/"), file("usr/bin/new"),
	))
	whiteoutTop := layerImage(t, layerTar(t,
		dir("usr/"), dir("usr/bin/"), file("usr/bin/.wh.real"),
	))

	for _, tt := range []struct {
		layers []fs.FS
		path   string
		want   bool
	}{
		{[]fs.FS{middle, bottom}, "usr/local/bin/dockerd", true},
		{[]fs.FS{middle, bottom}, "bin/real", true},
		{[]fs.FS{middle, bottom}, "rel/up/real", true},
		{[]fs.FS{middle, bottom}, "tool", true},
		{[]fs.FS{middle, bottom}, "usr/bin/../bin/real", true},
		{[]fs.FS{middle, bottom}, "usr/bin/real/x", false},
		{[]fs.FS{middle, bottom}, "loop-a", false},
		{[]fs.FS{middle, bottom}, "dangling", false},
		{[]fs.FS{middle, bottom}, "usr/local/bin/missing", false},
		{[]fs.FS{opaqueTop, middle, bottom}, "usr/local/bin/dockerd", false},
		{[]fs.FS{opaqueTop, middle, bottom}, "opt/other", true},
		{[]fs.FS{opaqueTop, middle, bottom}, "bin/real", true},
		{[]fs.FS{rootOpaqueTop, middle, bottom}, "usr/bin/new", true},
		{[]fs.FS{rootOpaqueTop, middle, bottom}, "usr/bin/real", false},
		{[]fs.FS{rootOpaqueTop, middle, bottom}, "usr/local/bin/dockerd", false},
		{[]fs.FS{whiteoutTop, middle, bottom}, "bin/real", false},
		{[]fs.FS{whiteoutTop, middle, bottom}, "tool", false},
		{[]fs.FS{whiteoutTop, middle, bottom}, "usr/local/bin/dockerd", true},
	} {
		got, err := layersHavePath(tt.layers, tt.path)
		if err != nil || got != tt.want {
			t.Errorf("layersHavePath(%d layers, %s) = %v, %v; want %v", len(tt.layers), tt.path, got, err, tt.want)
		}
	}
}
