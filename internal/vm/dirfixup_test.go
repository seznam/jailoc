package vm

import (
	"archive/tar"
	"context"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/erofs/go-erofs"
)

func TestImageDirFixup(t *testing.T) {
	t.Parallel()
	mtime := time.Unix(1600000000, 0)
	dir := func(name string, mode int64, uid int, xattrs ...string) layerEntry {
		hdr := tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: mode, Uid: uid, Gid: uid, ModTime: mtime}
		for i := 0; i+1 < len(xattrs); i += 2 {
			if hdr.PAXRecords == nil {
				hdr.PAXRecords = map[string]string{}
			}
			hdr.PAXRecords[paxXattr+xattrs[i]] = xattrs[i+1]
		}
		return layerEntry{hdr: hdr}
	}
	file := func(name string) layerEntry {
		return layerEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}, data: "x"}
	}
	base := layerImage(t, layerTar(t,
		dir("opt/", 0o755, 0),
		dir("opt/private/", 0o700, 1000, "user.note", "kept"),
		file("opt/private/old"),
		dir("gone/", 0o700, 1000),
		dir("keep/", 0o2770, 1000),
		dir("keep/sub/", 0o750, 1000),
		dir("swap/", 0o700, 1000),
		dir("explicit/", 0o700, 1000),
	))
	middle := layerImage(t, layerTar(t,
		file(".wh.gone"),
		file("swap"),
	))
	top := layerImage(t, layerTar(t,
		file("opt/private/new"),
		file("gone/x"),
		file("keep/.wh..wh..opq"),
		file("keep/sub/x"),
		file("swap/y"),
		dir("explicit/", 0o750, 5),
		file("explicit/z"),
	))

	fixup, err := imageDirFixup([]fs.FS{base, middle, top})
	if err != nil {
		t.Fatalf("imageDirFixup: %v", err)
	}
	got := map[string]dirMeta{}
	for _, d := range fixup {
		got[d.name] = d.meta
	}
	if want := []string{"gone", "keep", "keep/sub", "opt", "opt/private", "swap"}; !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
		t.Fatalf("fixup directories = %v, want %v", slices.Sorted(maps.Keys(got)), want)
	}
	for _, tt := range []struct {
		name string
		mode fs.FileMode
		uid  uint32
	}{
		// Inherited from the layer below.
		{"opt", 0o755, 0},
		{"opt/private", 0o700, 1000},
		{"keep", 0o770 | fs.ModeSetgid, 1000},
		// Deleted below, so new.
		{"gone", 0o755, 0},
		{"keep/sub", 0o755, 0},
		{"swap", 0o755, 0},
	} {
		m := got[tt.name]
		if m.mode != fs.ModeDir|tt.mode || m.uid != tt.uid || m.gid != tt.uid {
			t.Errorf("%s: mode %v owner %d:%d, want %v %d:%d", tt.name, m.mode, m.uid, m.gid, fs.ModeDir|tt.mode, tt.uid, tt.uid)
		}
	}
	if m := got["opt/private"]; !m.mtime.Equal(mtime) || !maps.Equal(m.xattrs, map[string]string{"user.note": "kept"}) {
		t.Errorf("opt/private: mtime %v xattrs %v, want %v and the lower layer's xattrs", m.mtime, m.xattrs, mtime)
	}

	// Layers without implied directories need no fixup.
	if fixup, err := imageDirFixup([]fs.FS{base}); err != nil || len(fixup) != 0 {
		t.Errorf("imageDirFixup(base) = %v, %v; want none", fixup, err)
	}
}

func TestImportImageDirFixup(t *testing.T) {
	t.Parallel()
	base := layerTar(t,
		layerEntry{hdr: tar.Header{Name: "opt/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "opt/private/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 1000, Gid: 1000}},
		layerEntry{hdr: tar.Header{Name: "opt/private/old", Typeflag: tar.TypeReg, Mode: 0o600, Uid: 1000, Gid: 1000}, data: "o"},
	)
	top := layerTar(t,
		layerEntry{hdr: tar.Header{Name: "opt/private/new", Typeflag: tar.TypeReg, Mode: 0o644}, data: "n"},
	)
	cacheDir := t.TempDir()
	ref := RootFSRef("sha256:fixup")
	saved := saveTarball(t, layeredImage(t, nil, base, top))
	if err := ImportImage(context.Background(), cacheDir, ref, func(w io.Writer) error {
		_, err := w.Write(saved)
		return err
	}, nil); err != nil {
		t.Fatalf("ImportImage: %v", err)
	}
	dir, m, err := readImage(cacheDir, ref)
	if err != nil {
		t.Fatalf("readImage: %v", err)
	}
	if want := []string{"000.erofs", "001.erofs", "002.erofs"}; !slices.Equal(m.Layers, want) {
		t.Fatalf("layers = %v, want %v with the fixup layer on top", m.Layers, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 4 {
		t.Errorf("image directory holds %d entries (%v), want three layers and the manifest", len(entries), err)
	}
	f, err := os.Open(filepath.Join(dir, "002.erofs")) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	img, err := erofs.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	fi, st := lstatLayer(t, img, "opt/private")
	if fi.Mode().Perm() != 0o700 || st.UID() != 1000 || st.GID() != 1000 {
		t.Errorf("fixup opt/private: mode %v owner %d:%d, want 0700 1000:1000", fi.Mode(), st.UID(), st.GID())
	}
	for _, x := range []string{impliedXattr, overlayOpaque} {
		if _, ok := st.GetXattr(x); ok {
			t.Errorf("fixup opt/private has %s", x)
		}
	}
	if des, err := fs.ReadDir(img, "opt/private"); err != nil || len(des) != 0 {
		t.Errorf("fixup opt/private holds %d entries (%v), want none", len(des), err)
	}
	for _, p := range []string{"opt/private/old", "opt/private/new"} {
		if has, err := ImageHasFile(cacheDir, ref, p); err != nil || !has {
			t.Errorf("ImageHasFile(%s) = %v, %v; want true", p, has, err)
		}
	}
}
