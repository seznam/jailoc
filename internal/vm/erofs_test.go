package vm

import (
	"archive/tar"
	"bytes"
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
	if err := writeLayerEROFS(bytes.NewReader(layer), f, dir); err != nil {
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
	for _, dir := range []string{"opt", "srv/replace", "var/early"} {
		fi, st := lstatLayer(t, img, dir)
		if v, _ := st.GetXattr(overlayOpaque); !fi.IsDir() || v != "y" {
			t.Errorf("%s: dir %v, %s=%q; want an opaque directory", dir, fi.IsDir(), overlayOpaque, v)
		}
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

func TestWriteLayerEROFSRejects(t *testing.T) {
	t.Parallel()
	for name, layer := range map[string][]byte{
		"entry below a symlink": layerTar(t,
			layerEntry{hdr: tar.Header{Name: "lib", Typeflag: tar.TypeSymlink, Linkname: "/etc"}},
			layerEntry{hdr: tar.Header{Name: "lib/passwd", Typeflag: tar.TypeReg, Mode: 0o644}, data: "x"}),
		"hard link to a missing file": layerTar(t,
			layerEntry{hdr: tar.Header{Name: "a", Typeflag: tar.TypeLink, Linkname: "missing"}}),
		"root whiteout": layerTar(t,
			layerEntry{hdr: tar.Header{Name: ".wh.", Typeflag: tar.TypeReg}}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			f, err := os.Create(filepath.Join(dir, "layer.erofs")) //nolint:gosec // G304: test temp dir
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			err = writeLayerEROFS(bytes.NewReader(layer), f, dir)
			if err == nil {
				t.Fatal("writeLayerEROFS succeeded")
			}
			if !strings.Contains(err.Error(), "layer entry") {
				t.Errorf("error %q does not name the entry", err)
			}
		})
	}
}
