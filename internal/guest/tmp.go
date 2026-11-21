package guest

import (
	"fmt"
	"strings"
	"time"
)

// Storage backends of the guest /tmp.
const (
	// TmpStorageRAM keeps /tmp on a tmpfs of TmpConfig.SizeBytes.
	TmpStorageRAM = "ram"
	// TmpStorageDisk keeps /tmp on an ext4 file system that the guest
	// creates on the scratch disk at every boot.
	TmpStorageDisk = "disk"
)

// defaultTmpSize is the tmpfs size of a guest config without one, the size
// /tmp had before it was configurable.
const defaultTmpSize = 1 << 30

// TmpConfig configures the guest /tmp. The host resolves the user-facing
// defaults; a zero TmpConfig, from a host that predates it, keeps the old
// 1 GiB tmpfs without expiry.
type TmpConfig struct {
	// Storage is TmpStorageRAM or TmpStorageDisk; empty means RAM.
	Storage string `json:"storage"`
	// SizeBytes bounds the RAM backend; the disk backend uses the whole
	// scratch disk, which the host sizes.
	SizeBytes int64 `json:"size_bytes"`
	// TTL removes regular files whose access, modification and change
	// times are all older than it, and empty directories whose access and
	// modification times are; zero disables the expiry.
	TTL time.Duration `json:"ttl_ns"`
}

// TmpUsage is the output of "jailoc-guest tmp-status": the /tmp file
// system's size, the bytes in use and the bytes the agent can still write.
type TmpUsage struct {
	CapacityBytes  uint64 `json:"capacity_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

// resolve fills in the defaults of a zero TmpConfig and rejects invalid
// settings.
func (t TmpConfig) resolve() (TmpConfig, error) {
	switch t.Storage {
	case "":
		t.Storage = TmpStorageRAM
	case TmpStorageRAM, TmpStorageDisk:
	default:
		return t, fmt.Errorf("tmp: unknown storage %q", t.Storage)
	}
	if t.SizeBytes < 0 {
		return t, fmt.Errorf("tmp: negative size %d", t.SizeBytes)
	}
	if t.TTL < 0 {
		return t, fmt.Errorf("tmp: negative ttl %s", t.TTL)
	}
	if t.SizeBytes == 0 && t.Storage == TmpStorageRAM {
		t.SizeBytes = defaultTmpSize
	}
	return t, nil
}

// tmpScratchDisk is the block device of the disk-backed /tmp: the first
// disk, or the second one when the first holds Docker's data.
func tmpScratchDisk(cfg *Config) string {
	if cfg.Docker != nil {
		return "/dev/vdb"
	}
	return "/dev/vda"
}

// tmpMkfsArgs are the mkfs.ext4 arguments that format disk for /tmp: no
// blocks reserved for root, and no discard of the whole disk at every boot.
func tmpMkfsArgs(disk string) []string {
	return []string{"-q", "-F", "-m", "0", "-E", "nodiscard", disk}
}

// tmpVars are the variables that name the temporary directory for libc,
// common runtimes and the Go toolchain.
var tmpVars = []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"}

// tmpEnv returns tmpVars entries pointing at /tmp for the ones env does not
// set, so an image or configured value wins; secrets, added later to the
// session environment, override them too.
func tmpEnv(env []string) []string {
	set := make(map[string]bool, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	var out []string
	for _, name := range tmpVars {
		if !set[name] {
			out = append(out, name+"=/tmp")
		}
	}
	return out
}

// tmpUsage computes TmpUsage from statfs block counts in units of bsize.
func tmpUsage(blocks, bfree, bavail, bsize uint64) TmpUsage {
	u := TmpUsage{CapacityBytes: blocks * bsize, AvailableBytes: bavail * bsize}
	if bfree < blocks {
		u.UsedBytes = (blocks - bfree) * bsize
	}
	return u
}
