package guest

import (
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
