package guest

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// reclaimFloorDiv keeps MemTotal/reclaimFloorDiv of file cache resident.
	reclaimFloorDiv = 8
	// reclaimStepDiv reclaims at most 1/reclaimStepDiv of the file cache per
	// round, so pages in use are read back before the next round takes more.
	reclaimStepDiv = 4
	// reclaimMin skips rounds that would free less than this many bytes.
	reclaimMin = 32 << 20
)

// meminfo holds /proc/meminfo fields in bytes.
type meminfo map[string]uint64

// parseMeminfo reads /proc/meminfo; values in kB are converted to bytes.
func parseMeminfo(r io.Reader) (meminfo, error) {
	info := meminfo{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && fields[1] == "kB" {
			v <<= 10
		}
		info[name] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read meminfo: %w", err)
	}
	if info["MemTotal"] == 0 {
		return nil, fmt.Errorf("read meminfo: no MemTotal")
	}
	return info, nil
}

// reclaimTarget is the number of bytes of file cache to reclaim in one
// round, or 0 when the cache is small enough. The guest kernel reports freed
// pages to the host through the balloon, so reclaimed cache is memory the
// host gets back.
func reclaimTarget(info meminfo) uint64 {
	cache := info["Active(file)"] + info["Inactive(file)"]
	floor := info["MemTotal"] / reclaimFloorDiv
	if cache <= floor {
		return 0
	}
	target := min(cache-floor, cache/reclaimStepDiv)
	if target < reclaimMin {
		return 0
	}
	return target
}

// swapHeader returns the first page of a version 1 Linux swap area of size
// bytes, the header mkswap writes.
func swapHeader(pageSize int, size uint64) ([]byte, error) {
	pages := size / uint64(pageSize) //nolint:gosec // G115: page size is a small positive kernel constant
	if pages < 10 || pages-1 > 0xffffffff {
		return nil, fmt.Errorf("swap size %d bytes out of range", size)
	}
	page := make([]byte, pageSize)
	// union swap_header: 1024 boot bytes, version, last_page, nr_badpages,
	// then the "SWAPSPACE2" signature in the last 10 bytes of the page.
	binary.LittleEndian.PutUint32(page[1024:], 1)
	binary.LittleEndian.PutUint32(page[1028:], uint32(pages-1))
	copy(page[pageSize-10:], "SWAPSPACE2")
	return page, nil
}
