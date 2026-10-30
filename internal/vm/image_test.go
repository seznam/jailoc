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
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/stacklok/go-microvm/image"
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

	if err := ImportImage(context.Background(), cacheDir, ref, save); err != nil {
		t.Fatalf("ImportImage: %v", err)
	}
	if image.NewCache(cacheDir).LookupRef(ref) == nil {
		t.Fatalf("%s not in the image cache after import", ref)
	}

	// A second save of an image with the same layers keeps only non-layer blobs.
	dir := t.TempDir()
	if err := unpackLayout(t.Context(), bytes.NewReader(saved), dir, image.NewCache(cacheDir).LayerCache()); err != nil {
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
	})
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
	})
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
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ImportImage after cancel = %v, want context.Canceled", err)
	}
}
