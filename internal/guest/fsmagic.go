package guest

import (
	"bytes"
	"errors"
	"io"
)

// extMagicAt reports whether r has the ext superblock magic 0xEF53 at byte
// 1080 (superblock at 1024, s_magic at offset 56, little endian).
func extMagicAt(r io.ReaderAt) (bool, error) {
	var magic [2]byte
	if _, err := r.ReadAt(magic[:], 1080); err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return magic[0] == 0x53 && magic[1] == 0xEF, nil
}

// extVolumeLabelAt returns the ext volume label of r: the 16-byte
// s_volume_name at byte 1144 (superblock at 1024, offset 120), without its
// trailing NUL bytes. A device shorter than the field has an empty label.
func extVolumeLabelAt(r io.ReaderAt) (string, error) {
	var name [16]byte
	if _, err := r.ReadAt(name[:], 1144); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	return string(bytes.TrimRight(name[:], "\x00")), nil
}
