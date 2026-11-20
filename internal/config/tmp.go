package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Values of tmp_storage: where the guest's /tmp lives.
const (
	// TmpStorageRAM keeps /tmp in memory (tmpfs).
	TmpStorageRAM = "ram"
	// TmpStorageDisk keeps /tmp on a sparse host-backed disk.
	TmpStorageDisk = "disk"
)

const (
	// DefaultTmpSize is the /tmp size when tmp_size is unset.
	DefaultTmpSize = "1g"
	// DefaultTmpTTL is the age after which /tmp entries are removed when
	// tmp_ttl is unset.
	DefaultTmpTTL = "8h"
	// MinTmpDiskSize is the smallest disk-backed /tmp the guest can format.
	MinTmpDiskSize = 16 << 20
)

// Tmp is the effective /tmp configuration of a workspace.
type Tmp struct {
	// Storage is TmpStorageRAM or TmpStorageDisk.
	Storage string
	// SizeBytes is the capacity of /tmp.
	SizeBytes int64
	// TTL is the age after which /tmp entries are removed; 0 disables cleanup.
	TTL time.Duration
}

// ParseTmpSize parses a tmp_size value: a positive integer of bytes, or
// followed by k, m or g (binary multiples) in either case.
func ParseTmpSize(s string) (int64, error) {
	mult := int64(1)
	digits := s
	if s != "" {
		switch s[len(s)-1] {
		case 'k', 'K':
			mult = 1 << 10
		case 'm', 'M':
			mult = 1 << 20
		case 'g', 'G':
			mult = 1 << 30
		}
		if mult != 1 {
			digits = s[:len(s)-1]
		}
	}
	if !validTmpDigits(digits) {
		return 0, fmt.Errorf("invalid tmp_size %q: must be a positive integer of bytes, optionally followed by k, m, or g (e.g. \"1g\", \"512m\")", s)
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid tmp_size %q: too large", s)
	}
	return n * mult, nil
}

// validTmpDigits reports whether s is a positive decimal integer without a
// sign or leading zeros.
func validTmpDigits(s string) bool {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return false
	}
	return strings.Trim(s, "0123456789") == ""
}

// ParseTmpTTL parses a tmp_ttl value: "0" disables cleanup, anything else is
// a positive Go duration such as "8h" or "15m".
func ParseTmpTTL(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid tmp_ttl %q: must be \"0\" (never clean up) or a positive duration such as \"8h\" or \"15m\"", s)
	}
	return d, nil
}

// ResolveTmp merges the tmp_* settings of a workspace over the defaults over
// the built-in values (ram, 1g, 8h) and validates the result.
func ResolveTmp(d Defaults, ws Workspace) (Tmp, error) {
	var out Tmp
	out.Storage = TmpStorageRAM
	if v := firstSet(ws.TmpStorage, d.TmpStorage); v != nil {
		out.Storage = *v
	}
	if out.Storage != TmpStorageRAM && out.Storage != TmpStorageDisk {
		return Tmp{}, fmt.Errorf("invalid tmp_storage %q: must be %q or %q", out.Storage, TmpStorageRAM, TmpStorageDisk)
	}
	sizeText := DefaultTmpSize
	if v := firstSet(ws.TmpSize, d.TmpSize); v != nil {
		sizeText = *v
	}
	size, err := ParseTmpSize(sizeText)
	if err != nil {
		return Tmp{}, err
	}
	out.SizeBytes = size
	if out.Storage == TmpStorageDisk && out.SizeBytes < MinTmpDiskSize {
		return Tmp{}, fmt.Errorf("tmp_size %d bytes is too small for tmp_storage %q: must be at least %d MiB", out.SizeBytes, TmpStorageDisk, MinTmpDiskSize>>20)
	}
	ttl := DefaultTmpTTL
	if v := firstSet(ws.TmpTTL, d.TmpTTL); v != nil {
		ttl = *v
	}
	if out.TTL, err = ParseTmpTTL(ttl); err != nil {
		return Tmp{}, err
	}
	return out, nil
}

func firstSet(a, b *string) *string {
	if a != nil {
		return a
	}
	return b
}
