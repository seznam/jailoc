package vm

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/erofs/go-erofs"
)

// OCI whiteouts and their overlayfs counterparts. A layer converted to EROFS
// is an overlayfs lower layer, so a deleted entry becomes a 0/0 character
// device and an opaque directory carries the opaque xattr.
const (
	whiteoutPrefix = ".wh."
	opaqueWhiteout = ".wh..wh..opq"
	overlayOpaque  = "trusted.overlay.opaque"
	overlayXattrs  = "trusted.overlay."
	paxXattr       = "SCHILY.xattr."
)

// Unix file type bits of EROFS inodes.
const (
	sIFIFO  = 0o010000
	sIFCHR  = 0o020000
	sIFBLK  = 0o060000
	sIFSOCK = 0o140000
)

// writeLayerEROFS converts the uncompressed OCI layer tarball r into an
// EROFS image written to out, for use as an overlayfs lower layer. File data
// is spooled in tmpDir until the image is written. The image keeps the
// numeric owners, modes (setuid, setgid, sticky), xattrs, hard links and
// device nodes of the tarball.
func writeLayerEROFS(r io.Reader, out io.WriteSeeker, tmpDir string) error {
	c := &layerConverter{
		w:         erofs.Create(out, erofs.WithTempDir(tmpDir), erofs.WithBuildTime(0, 0)),
		whiteouts: map[string]bool{},
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read layer: %w", err)
		}
		if err := c.add(hdr, tr); err != nil {
			return fmt.Errorf("layer entry %s: %w", hdr.Name, err)
		}
	}
	if err := c.markOpaque(); err != nil {
		return err
	}
	if err := c.w.Close(); err != nil {
		return fmt.Errorf("write layer image: %w", err)
	}
	return nil
}

type layerConverter struct {
	w *erofs.Writer
	// whiteouts are the paths the layer deletes from the layers below. An
	// entry of the layer itself at such a path replaces the whiteout, and a
	// directory there must hide the lower directory's content.
	whiteouts map[string]bool
}

func (c *layerConverter) add(hdr *tar.Header, r io.Reader) error {
	name := path.Join("/", hdr.Name) //nolint:gosec // G305: rooted inside the EROFS image, not the host
	if name == "/" && hdr.Typeflag != tar.TypeDir {
		return nil
	}
	dir, base := path.Split(name)
	dir = path.Clean(dir)
	switch {
	case base == opaqueWhiteout:
		if err := c.parentDir(dir); err != nil {
			return err
		}
		return c.w.Setxattr(dir, overlayOpaque, "y")
	case strings.HasPrefix(base, whiteoutPrefix):
		return c.whiteout(path.Join(dir, strings.TrimPrefix(base, whiteoutPrefix)))
	}
	if err := c.parentDir(dir); err != nil {
		return err
	}
	if err := c.replace(name, hdr.Typeflag == tar.TypeDir); err != nil {
		return err
	}
	if err := c.create(name, hdr, r); err != nil {
		return err
	}
	if hdr.Typeflag == tar.TypeLink {
		// A hard link shares the metadata of its target.
		return nil
	}
	return c.metadata(name, hdr)
}

// whiteout records that the layer deletes name from the layers below. An
// entry the layer adds at name itself stays.
func (c *layerConverter) whiteout(name string) error {
	if name == "/" {
		return errors.New("whiteout of the root directory")
	}
	c.whiteouts[name] = true
	if _, err := c.w.Lstat(name); err == nil {
		return nil
	}
	if err := c.parentDir(path.Dir(name)); err != nil {
		return err
	}
	return c.w.Mknod(name, sIFCHR, 0)
}

// parentDir fails when an ancestor of an entry is not a directory, which
// would make the entry unreachable or let it escape through a symlink.
// Missing ancestors are created by the writer.
func (c *layerConverter) parentDir(dir string) error {
	fi, err := c.w.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, erofs.ErrNotDirectory):
		return fmt.Errorf("parent of %s is not a directory", dir)
	case err != nil:
		return fmt.Errorf("look up %s: %w", dir, err)
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// replace removes an earlier entry at name, which a later entry of the same
// tarball overrides. A directory replacing a directory keeps its content.
func (c *layerConverter) replace(name string, dir bool) error {
	fi, err := c.w.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up %s: %w", name, err)
	}
	if dir && fi.IsDir() {
		return nil
	}
	if err := c.w.RemoveAll(name); err != nil {
		return fmt.Errorf("replace %s: %w", name, err)
	}
	return nil
}

func (c *layerConverter) create(name string, hdr *tar.Header, r io.Reader) error {
	mode := hdr.FileInfo().Mode()
	switch hdr.Typeflag {
	case tar.TypeDir:
		if fi, err := c.w.Lstat(name); err == nil && fi.IsDir() {
			return nil
		}
		return c.w.Mkdir(name, mode.Perm())
	case tar.TypeReg:
		f, err := c.w.Create(name)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, r); err != nil {
			_ = f.Close()
			return fmt.Errorf("copy data: %w", err)
		}
		return f.Close()
	case tar.TypeSymlink:
		return c.w.Symlink(hdr.Linkname, name)
	case tar.TypeLink:
		return c.w.Link(path.Join("/", hdr.Linkname), name) //nolint:gosec // G305: rooted inside the EROFS image
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		kind := map[byte]uint16{tar.TypeChar: sIFCHR, tar.TypeBlock: sIFBLK, tar.TypeFifo: sIFIFO}[hdr.Typeflag]
		return c.w.Mknod(name, kind|unixPerm(mode), encodeDev(hdr.Devmajor, hdr.Devminor))
	}
	return fmt.Errorf("unsupported tar entry type %q", hdr.Typeflag)
}

func (c *layerConverter) metadata(name string, hdr *tar.Header) error {
	if hdr.Typeflag != tar.TypeSymlink {
		if err := c.w.Chmod(name, hdr.FileInfo().Mode()); err != nil {
			return err
		}
	}
	if err := c.w.Chown(name, hdr.Uid, hdr.Gid); err != nil {
		return err
	}
	if err := c.w.Chtimes(name, time.Unix(0, 0), hdr.ModTime); err != nil {
		return err
	}
	for key, value := range hdr.PAXRecords {
		attr, ok := strings.CutPrefix(key, paxXattr)
		// The layer's own overlayfs xattrs would change how the guest
		// overlay reads it.
		if !ok || strings.HasPrefix(attr, overlayXattrs) {
			continue
		}
		if err := c.w.Setxattr(name, attr, value); err != nil {
			return err
		}
	}
	return nil
}

// markOpaque makes directories the layer adds where it also deletes lower
// entries opaque, so the content below stays hidden.
func (c *layerConverter) markOpaque() error {
	for name := range c.whiteouts {
		fi, err := c.w.Lstat(name)
		if err != nil || !fi.IsDir() {
			continue
		}
		if err := c.w.Setxattr(name, overlayOpaque, "y"); err != nil {
			return fmt.Errorf("mark %s opaque: %w", name, err)
		}
	}
	return nil
}

func unixPerm(mode fs.FileMode) uint16 {
	perm := uint16(mode.Perm()) //nolint:gosec // G115: Perm is at most 0o777
	if mode&fs.ModeSetuid != 0 {
		perm |= 0o4000
	}
	if mode&fs.ModeSetgid != 0 {
		perm |= 0o2000
	}
	if mode&fs.ModeSticky != 0 {
		perm |= 0o1000
	}
	return perm
}

// encodeDev encodes a device number as the Linux kernel's new_encode_dev,
// which EROFS stores.
func encodeDev(major, minor int64) uint32 {
	return uint32(minor&0xff) | uint32(major&0xfff)<<8 | uint32(minor&^0xff)<<12 //nolint:gosec // G115: Linux device numbers fit the 32-bit encoding
}
