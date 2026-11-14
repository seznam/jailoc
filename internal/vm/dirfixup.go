package vm

import (
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/erofs/go-erofs"
)

// dirMeta is the metadata of a directory as overlayfs shows it.
type dirMeta struct {
	mode     fs.FileMode
	uid, gid uint32
	mtime    time.Time
	xattrs   map[string]string
}

// dirState is a directory of the merged image layers.
type dirState struct {
	// meta is the directory's metadata in the image.
	meta dirMeta
	// implied is set when the topmost layer that has the directory has no
	// entry of its own for it, so overlayfs would show that layer's default
	// metadata instead of meta.
	implied bool
}

// fixupDir is a directory of the fixup layer.
type fixupDir struct {
	name string
	meta dirMeta
}

// imageDirFixup replays the image layers, the bottom first, and returns the
// directories whose topmost layer has only an implied directory for them,
// with the metadata the image gives them, and their ancestors. A layer
// above all others that holds these directories makes overlayfs show the
// image's metadata, as a runtime that applies the layers on top of each
// other does. The root directory is left out: the boot root covers it.
func imageDirFixup(layers []fs.FS) ([]fixupDir, error) {
	dirs := map[string]dirState{}
	for _, l := range layers {
		if err := replayLayer(dirs, l); err != nil {
			return nil, err
		}
	}
	want := map[string]bool{}
	for name, d := range dirs {
		if !d.implied {
			continue
		}
		for ; name != "."; name = path.Dir(name) {
			want[name] = true
		}
	}
	fixup := make([]fixupDir, 0, len(want))
	for _, name := range slices.Sorted(maps.Keys(want)) {
		d, ok := dirs[name]
		if !ok {
			return nil, fmt.Errorf("image layers lack directory %s", name)
		}
		fixup = append(fixup, fixupDir{name: name, meta: d.meta})
	}
	return fixup, nil
}

// replayLayer applies the directories of layer l to dirs.
func replayLayer(dirs map[string]dirState, l fs.FS) error {
	return fs.WalkDir(l, ".", func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read image layer: %w", err)
		}
		fi, err := lstat(l, name)
		if err != nil {
			return fmt.Errorf("read image layer: %w", err)
		}
		if !fi.IsDir() {
			// Whiteouts and other entries replace a directory below.
			if _, ok := dirs[name]; ok {
				removeDirs(dirs, name, true)
			}
			return nil
		}
		if isOpaque(fi) {
			removeDirs(dirs, name, false)
		}
		if name == "." {
			return nil
		}
		_, implied := xattr(fi, impliedXattr)
		if d, ok := dirs[name]; ok && implied {
			d.implied = true
			dirs[name] = d
			return nil
		}
		dirs[name] = dirState{meta: metaOf(fi), implied: implied}
		return nil
	})
}

// removeDirs removes the directories below name from dirs, and name itself
// when self is set.
func removeDirs(dirs map[string]dirState, name string, self bool) {
	prefix := name + "/"
	if name == "." {
		prefix = ""
	}
	for d := range dirs {
		if d == name && self || strings.HasPrefix(d, prefix) {
			delete(dirs, d)
		}
	}
}

func metaOf(fi fs.FileInfo) dirMeta {
	m := dirMeta{mode: fi.Mode(), mtime: fi.ModTime(), xattrs: map[string]string{}}
	if o, ok := fi.(interface {
		UID() uint32
		GID() uint32
	}); ok {
		m.uid, m.gid = o.UID(), o.GID()
	}
	if x, ok := fi.(interface{ GetAllXattr() map[string]string }); ok {
		for k, v := range x.GetAllXattr() {
			if !strings.HasPrefix(k, overlayXattrs) && k != impliedXattr {
				m.xattrs[k] = v
			}
		}
	}
	return m
}

func xattr(fi fs.FileInfo, name string) (string, bool) {
	x, ok := fi.(interface {
		GetXattr(string) (string, bool)
	})
	if !ok {
		return "", false
	}
	return x.GetXattr(name)
}

// writeDirFixup writes the fixup layer holding dirs, parents first, as an
// EROFS image to out.
func writeDirFixup(dirs []fixupDir, out io.WriteSeeker, tmpDir string) error {
	w := erofs.Create(out, erofs.WithTempDir(tmpDir), erofs.WithBuildTime(0, 0))
	for _, d := range dirs {
		name := "/" + d.name
		if err := w.Mkdir(name, d.meta.mode.Perm()); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		if err := w.Chmod(name, d.meta.mode); err != nil {
			return fmt.Errorf("set mode of %s: %w", name, err)
		}
		if err := w.Chown(name, int(d.meta.uid), int(d.meta.gid)); err != nil {
			return fmt.Errorf("set owner of %s: %w", name, err)
		}
		if err := w.Chtimes(name, time.Unix(0, 0), d.meta.mtime); err != nil {
			return fmt.Errorf("set times of %s: %w", name, err)
		}
		for k, v := range d.meta.xattrs {
			if err := w.Setxattr(name, k, v); err != nil {
				return fmt.Errorf("set xattr of %s: %w", name, err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("write directory fixup layer: %w", err)
	}
	return nil
}
