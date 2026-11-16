package vm

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erofs/go-erofs"
)

type layerEntry struct {
	hdr  tar.Header
	data string
}

func layerTar(t *testing.T, entries ...layerEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e.hdr
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.data))
		}
		if hdr.ModTime.IsZero() {
			hdr.ModTime = time.Unix(1700000000, 0)
		}
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatalf("write header %s: %v", hdr.Name, err)
		}
		if _, err := io.WriteString(tw, e.data); err != nil {
			t.Fatalf("write %s: %v", hdr.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

// layerImage converts a layer tarball and opens the resulting image.
func layerImage(t *testing.T, layer []byte) fs.FS {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "layer.erofs")) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := writeLayerEROFS(context.Background(), bytes.NewReader(layer), f, dir, newConvertLimits()); err != nil {
		t.Fatalf("writeLayerEROFS: %v", err)
	}
	img, err := erofs.Open(f)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	return img
}

type erofsStat interface {
	UID() uint32
	GID() uint32
	Rdev() uint64
	Ino() uint64
	GetXattr(string) (string, bool)
}

func lstatLayer(t *testing.T, img fs.FS, name string) (fs.FileInfo, erofsStat) {
	t.Helper()
	fi, err := img.(interface {
		Lstat(string) (fs.FileInfo, error)
	}).Lstat(name)
	if err != nil {
		t.Fatalf("lstat %s: %v", name, err)
	}
	st, ok := fi.(erofsStat)
	if !ok {
		t.Fatalf("%s: file info %T has no EROFS stat accessors", name, fi)
	}
	return fi, st
}

func TestWriteLayerEROFSMetadata(t *testing.T) {
	t.Parallel()
	layer := layerTar(t,
		layerEntry{hdr: tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/sudo", Typeflag: tar.TypeReg, Mode: 0o4755}, data: "sudo"},
		layerEntry{hdr: tar.Header{Name: "usr/bin/crontab", Typeflag: tar.TypeReg, Mode: 0o2755, Gid: 102}, data: "cron"},
		layerEntry{hdr: tar.Header{Name: "usr/bin/ping", Typeflag: tar.TypeReg, Mode: 0o755, PAXRecords: map[string]string{
			"SCHILY.xattr.security.capability": "\x01\x00\x00\x02\x00\x20\x00\x00",
			"SCHILY.xattr.user.note":           "hello",
			"SCHILY.xattr.trusted.overlay.foo": "y",
		}}, data: "ping"},
		layerEntry{hdr: tar.Header{Name: "usr/bin/pong", Typeflag: tar.TypeLink, Linkname: "usr/bin/ping"}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/vi", Typeflag: tar.TypeSymlink, Linkname: "vim"}},
		layerEntry{hdr: tar.Header{Name: "tmp/", Typeflag: tar.TypeDir, Mode: 0o1777}},
		layerEntry{hdr: tar.Header{Name: "dev/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Mode: 0o666, Devmajor: 1, Devminor: 3}},
		layerEntry{hdr: tar.Header{Name: "dev/loop300", Typeflag: tar.TypeBlock, Mode: 0o660, Devmajor: 7, Devminor: 300}},
		layerEntry{hdr: tar.Header{Name: "dev/fifo", Typeflag: tar.TypeFifo, Mode: 0o600}},
		layerEntry{hdr: tar.Header{Name: "home/agent/", Typeflag: tar.TypeDir, Mode: 0o750, Uid: 1000, Gid: 1000}},
		layerEntry{hdr: tar.Header{Name: "home/agent/Case", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 1000, Gid: 1000}, data: "upper"},
		layerEntry{hdr: tar.Header{Name: "home/agent/case", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 1000, Gid: 1000}, data: "lower"},
		// A later entry at the same path replaces the earlier one.
		layerEntry{hdr: tar.Header{Name: "etc/motd", Typeflag: tar.TypeReg, Mode: 0o644}, data: "old"},
		layerEntry{hdr: tar.Header{Name: "etc/motd", Typeflag: tar.TypeReg, Mode: 0o600, Uid: 7}, data: "new"},
	)
	img := layerImage(t, layer)

	for _, tt := range []struct {
		name     string
		mode     fs.FileMode
		uid, gid uint32
	}{
		{"usr/bin/sudo", 0o755 | fs.ModeSetuid, 0, 0},
		{"usr/bin/crontab", 0o755 | fs.ModeSetgid, 0, 102},
		{"tmp", 0o777 | fs.ModeDir | fs.ModeSticky, 0, 0},
		{"home/agent", 0o750 | fs.ModeDir, 1000, 1000},
		{"dev/null", 0o666 | fs.ModeDevice | fs.ModeCharDevice, 0, 0},
		{"dev/loop300", 0o660 | fs.ModeDevice, 0, 0},
		{"dev/fifo", 0o600 | fs.ModeNamedPipe, 0, 0},
		{"usr/bin/vi", 0o777 | fs.ModeSymlink, 0, 0},
		{"etc/motd", 0o600, 7, 0},
	} {
		fi, st := lstatLayer(t, img, tt.name)
		if fi.Mode() != tt.mode {
			t.Errorf("%s: mode %v, want %v", tt.name, fi.Mode(), tt.mode)
		}
		if st.UID() != tt.uid || st.GID() != tt.gid {
			t.Errorf("%s: owner %d:%d, want %d:%d", tt.name, st.UID(), st.GID(), tt.uid, tt.gid)
		}
	}

	_, null := lstatLayer(t, img, "dev/null")
	_, loop := lstatLayer(t, img, "dev/loop300")
	if null.Rdev() != uint64(encodeDev(1, 3)) || loop.Rdev() != uint64(encodeDev(7, 300)) {
		t.Errorf("device numbers %#x %#x, want %#x %#x", null.Rdev(), loop.Rdev(), encodeDev(1, 3), encodeDev(7, 300))
	}
	if got := encodeDev(7, 300); got != 0x10072c {
		t.Errorf("encodeDev(7, 300) = %#x, want the kernel's new_encode_dev 0x10072c", got)
	}

	_, ping := lstatLayer(t, img, "usr/bin/ping")
	_, pong := lstatLayer(t, img, "usr/bin/pong")
	if ping.Ino() != pong.Ino() {
		t.Errorf("hard link has inode %d, target %d", pong.Ino(), ping.Ino())
	}
	if v, _ := ping.GetXattr("security.capability"); v != "\x01\x00\x00\x02\x00\x20\x00\x00" {
		t.Errorf("security.capability = %q", v)
	}
	if v, _ := ping.GetXattr("user.note"); v != "hello" {
		t.Errorf("user.note = %q", v)
	}
	if _, ok := ping.GetXattr("trusted.overlay.foo"); ok {
		t.Error("the layer's own overlayfs xattr must be dropped")
	}
	if target, err := img.(interface{ ReadLink(string) (string, error) }).ReadLink("usr/bin/vi"); err != nil || target != "vim" {
		t.Errorf("symlink target %q, %v", target, err)
	}
	for name, want := range map[string]string{"home/agent/Case": "upper", "home/agent/case": "lower", "etc/motd": "new", "usr/bin/pong": "ping"} {
		data, err := fs.ReadFile(img, name)
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
}

func TestWriteLayerEROFSWhiteouts(t *testing.T) {
	t.Parallel()
	layer := layerTar(t,
		layerEntry{hdr: tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "etc/.wh.removed", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "opt/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "opt/.wh..wh..opq", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "opt/new", Typeflag: tar.TypeReg, Mode: 0o644}, data: "x"},
		// A directory deleted and recreated in the same layer.
		layerEntry{hdr: tar.Header{Name: "srv/.wh.replace", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "srv/replace/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "srv/replace/inner", Typeflag: tar.TypeReg, Mode: 0o644}, data: "x"},
		// A directory recreated before its whiteout entry.
		layerEntry{hdr: tar.Header{Name: "var/early/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "var/.wh.early", Typeflag: tar.TypeReg}},
		// A file replacing a deleted entry is a plain file.
		layerEntry{hdr: tar.Header{Name: "var/.wh.file", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "var/file", Typeflag: tar.TypeReg, Mode: 0o644}, data: "f"},
		// An opaque marker before its directory's entry, and one without.
		layerEntry{hdr: tar.Header{Name: "home/.wh..wh..opq", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "home/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 1000, Gid: 1000}},
		layerEntry{hdr: tar.Header{Name: "lib/.wh..wh..opq", Typeflag: tar.TypeReg}},
	)
	img := layerImage(t, layer)

	fi, st := lstatLayer(t, img, "etc/removed")
	if fi.Mode().Type() != fs.ModeDevice|fs.ModeCharDevice || st.Rdev() != 0 {
		t.Errorf("etc/removed: mode %v rdev %d, want an overlayfs whiteout (0/0 character device)", fi.Mode(), st.Rdev())
	}
	if _, err := fs.Stat(img, "etc/.wh.removed"); err == nil {
		t.Error("whiteout file must not appear in the image")
	}
	if _, err := fs.Stat(img, "opt/.wh..wh..opq"); err == nil {
		t.Error("opaque marker must not appear in the image")
	}
	for _, dir := range []string{"opt", "srv/replace", "var/early", "home", "lib"} {
		fi, st := lstatLayer(t, img, dir)
		if v, _ := st.GetXattr(overlayOpaque); !fi.IsDir() || v != "y" {
			t.Errorf("%s: dir %v, %s=%q; want an opaque directory", dir, fi.IsDir(), overlayOpaque, v)
		}
	}
	if fi, st := lstatLayer(t, img, "home"); fi.Mode().Perm() != 0o700 || st.UID() != 1000 || st.GID() != 1000 {
		t.Errorf("home: mode %v owner %d:%d, want the directory entry's 0700 1000:1000", fi.Mode(), st.UID(), st.GID())
	}
	for _, name := range []string{"opt/new", "srv/replace/inner"} {
		if _, err := fs.Stat(img, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if fi, st := lstatLayer(t, img, "var/file"); !fi.Mode().IsRegular() {
		t.Errorf("var/file: mode %v", fi.Mode())
	} else if _, ok := st.GetXattr(overlayOpaque); ok {
		t.Error("var/file must not be opaque")
	}
	if _, st := lstatLayer(t, img, "var"); func() bool { _, ok := st.GetXattr(overlayOpaque); return ok }() {
		t.Error("var must not be opaque")
	}
}

// convertTestLayer converts layer with lim, under a canceled context when
// canceled is set.
func convertTestLayer(t *testing.T, layer []byte, lim *convertLimits, canceled bool) error {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "layer.erofs")) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if canceled {
		cancel()
	}
	return writeLayerEROFS(ctx, bytes.NewReader(layer), f, dir, lim)
}

func TestWriteLayerEROFSRejects(t *testing.T) {
	t.Parallel()
	file := func(name, data string) layerEntry {
		return layerEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}, data: data}
	}
	for _, tt := range []struct {
		name     string
		layer    []byte
		lim      convertLimits
		canceled bool
		want     string
	}{
		{name: "entry below a symlink", layer: layerTar(t,
			layerEntry{hdr: tar.Header{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: "/etc"}},
			file("lib/passwd", "x")), want: "layer entry lib/passwd"},
		{name: "hard link to a missing file", layer: layerTar(t,
			layerEntry{hdr: tar.Header{Name: "a", Typeflag: tar.TypeLink, Linkname: "missing"}}), want: "layer entry a"},
		{name: "root whiteout", layer: layerTar(t, file(".wh.", "")), want: "layer entry .wh."},
		{name: "owner beyond 32 bits", layer: layerTar(t,
			layerEntry{hdr: tar.Header{Name: "a", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 1 << 32}}), want: "owner 4294967296:0 out of range"},
		{name: "too many entries", layer: layerTar(t, file("a", ""), file("b", ""), file("c", "")),
			lim: convertLimits{bytes: maxImageBytes, entries: 2}, want: "more than 2 entries"},
		{name: "too much data", layer: layerTar(t, file("a", "abc"), file("b", "de")),
			lim: convertLimits{bytes: 4, entries: maxLayerEntries}, want: "layer entry b: file data of the image exceeds"},
		{name: "canceled", layer: layerTar(t, file("a", "abc")), canceled: true, want: "context canceled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lim := tt.lim
			if lim == (convertLimits{}) {
				lim = *newConvertLimits()
			}
			err := convertTestLayer(t, tt.layer, &lim, tt.canceled)
			if err == nil {
				t.Fatal("writeLayerEROFS succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestWriteLayerEROFSSharesDataLimit(t *testing.T) {
	t.Parallel()
	lim := &convertLimits{bytes: 5, entries: maxLayerEntries}
	layer := layerTar(t, layerEntry{hdr: tar.Header{Name: "a", Typeflag: tar.TypeReg, Mode: 0o644}, data: "abc"})
	if err := convertTestLayer(t, layer, lim, false); err != nil {
		t.Fatalf("first layer: %v", err)
	}
	if lim.bytes != 2 {
		t.Errorf("%d bytes left, want 2", lim.bytes)
	}
	if err := convertTestLayer(t, layer, lim, false); err == nil {
		t.Fatal("second layer stayed within the limit of the first")
	}
}

func TestWriteLayerEROFSTarFormats(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range []layerEntry{
		{hdr: tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "x"}}},
		{hdr: tar.Header{Name: "contiguous", Typeflag: tar.TypeCont, Mode: 0o644, Size: 1}, data: "c"},
	} {
		e.hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(&e.hdr); err != nil {
			t.Fatalf("write header %s: %v", e.hdr.Name, err)
		}
		if _, err := io.WriteString(tw, e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	buf.Write(gnuSparseEntry(t, "sparse", 4096, "abc"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	img := layerImage(t, buf.Bytes())
	for name, want := range map[string]string{
		"contiguous": "c",
		"sparse":     strings.Repeat("\x00", 4096) + "abc",
	} {
		got, err := fs.ReadFile(img, name)
		if err != nil || string(got) != want {
			t.Errorf("%s: %d bytes, err %v; want %d bytes", name, len(got), err, len(want))
		}
	}
	if _, err := fs.Stat(img, "pax_global_header"); err == nil {
		t.Error("the global header must not appear in the image")
	}
}

// gnuSparseEntry returns an old GNU sparse file entry, which tar.Writer does
// not write, holding data after a hole of the given size.
func gnuSparseEntry(t *testing.T, name string, hole int, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(1700000000, 0), Format: tar.FormatGNU}
	if err := tw.WriteHeader(&hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	octal := func(off, size int, v int) {
		copy(b[off:off+size], fmt.Sprintf("%0*o\x00", size-1, v))
	}
	b[156] = tar.TypeGNUSparse
	octal(386, 12, hole)           // first sparse entry: offset
	octal(398, 12, len(data))      // and length of the data
	octal(483, 12, hole+len(data)) // real size
	copy(b[148:156], "        ")
	sum := 0
	for _, c := range b[:512] {
		sum += int(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

func TestWriteLayerEROFSAUFSMetadata(t *testing.T) {
	t.Parallel()
	img := layerImage(t, layerTar(t,
		layerEntry{hdr: tar.Header{Name: ".wh..wh.plnk/", Typeflag: tar.TypeDir, Mode: 0o700}},
		layerEntry{hdr: tar.Header{Name: ".wh..wh.plnk/123.4", Typeflag: tar.TypeReg, Mode: 0o755}, data: "tool"},
		layerEntry{hdr: tar.Header{Name: ".wh..wh.aufs", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "usr/.wh..wh.orph", Typeflag: tar.TypeDir, Mode: 0o700}},
		layerEntry{hdr: tar.Header{Name: "usr/bin/tool", Typeflag: tar.TypeLink, Linkname: ".wh..wh.plnk/123.4"}},
	))
	for _, name := range []string{".wh..wh.plnk", ".wh.plnk", ".wh..wh.aufs", ".wh.aufs", "usr/.wh..wh.orph", "usr/.wh.orph"} {
		if _, err := lstatLayerErr(img, name); err == nil {
			t.Errorf("%s must not appear in the image", name)
		}
	}
	fi, _ := lstatLayer(t, img, "usr/bin/tool")
	if got, err := fs.ReadFile(img, "usr/bin/tool"); err != nil || string(got) != "tool" || fi.Mode().Perm() != 0o755 {
		t.Errorf("usr/bin/tool: %q, %v, mode %v; want the hard link target's data and mode", got, err, fi.Mode())
	}
}

func lstatLayerErr(img fs.FS, name string) (fs.FileInfo, error) {
	return img.(interface {
		Lstat(string) (fs.FileInfo, error)
	}).Lstat(name)
}

func TestWriteLayerEROFSImpliedDirs(t *testing.T) {
	t.Parallel()
	img := layerImage(t, layerTar(t,
		// Parents created for an entry only.
		layerEntry{hdr: tar.Header{Name: "opt/private/new", Typeflag: tar.TypeReg, Mode: 0o644}, data: "n"},
		// A directory entry after its child.
		layerEntry{hdr: tar.Header{Name: "srv/a/b/file", Typeflag: tar.TypeReg, Mode: 0o644}, data: "f"},
		layerEntry{hdr: tar.Header{Name: "srv/a/", Typeflag: tar.TypeDir, Mode: 0o750, Uid: 7}},
		// A directory deleted and recreated for an entry below it.
		layerEntry{hdr: tar.Header{Name: "var/", Typeflag: tar.TypeDir, Mode: 0o755}},
		layerEntry{hdr: tar.Header{Name: "var/.wh.cache", Typeflag: tar.TypeReg}},
		layerEntry{hdr: tar.Header{Name: "var/cache/x", Typeflag: tar.TypeReg, Mode: 0o644}, data: "x"},
		// An opaque marker without its directory.
		layerEntry{hdr: tar.Header{Name: "lib/.wh..wh..opq", Typeflag: tar.TypeReg}},
	))
	for _, tt := range []struct {
		name            string
		implied, opaque bool
	}{
		{"opt", true, false},
		{"opt/private", true, false},
		{"srv", true, false},
		{"srv/a", false, false},
		{"srv/a/b", true, false},
		{"var", false, false},
		{"var/cache", false, true},
		{"lib", true, true},
	} {
		fi, st := lstatLayer(t, img, tt.name)
		_, implied := st.GetXattr(impliedXattr)
		_, opaque := st.GetXattr(overlayOpaque)
		if !fi.IsDir() || implied != tt.implied || opaque != tt.opaque {
			t.Errorf("%s: dir %v implied %v opaque %v; want implied %v opaque %v", tt.name, fi.IsDir(), implied, opaque, tt.implied, tt.opaque)
		}
	}
	if fi, st := lstatLayer(t, img, "srv/a"); fi.Mode().Perm() != 0o750 || st.UID() != 7 {
		t.Errorf("srv/a: mode %v uid %d, want the entry's 0750 and 7", fi.Mode(), st.UID())
	}
	if _, err := fs.Stat(img, "var/cache/x"); err != nil {
		t.Errorf("var/cache/x: %v", err)
	}
}
