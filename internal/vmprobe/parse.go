package vmprobe

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePortRanges parses "1-1024,2222" into a sorted port list.
func ParsePortRanges(s string) ([]int, error) {
	var out []int
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, found := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("port range %q: %w", part, err)
		}
		b := a
		if found {
			if b, err = strconv.Atoi(hi); err != nil {
				return nil, fmt.Errorf("port range %q: %w", part, err)
			}
		}
		if a < 0 || b < a || b > 0xffffffff {
			return nil, fmt.Errorf("port range %q invalid", part)
		}
		for i := a; i <= b; i++ {
			out = append(out, i)
		}
	}
	return out, nil
}

// MountInfo is one /proc/self/mountinfo entry.
type MountInfo struct {
	Root       string
	MountPoint string
	Options    string
	FSType     string
	Source     string
	SuperOpts  string
}

func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ParseMountInfo parses mountinfo content.
func ParseMountInfo(data string) []MountInfo {
	var out []MountInfo
	for line := range strings.SplitSeq(data, "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f := strings.Fields(pre)
		g := strings.Fields(post)
		if len(f) < 6 || len(g) < 3 {
			continue
		}
		out = append(out, MountInfo{
			Root: unescapeMount(f[3]), MountPoint: unescapeMount(f[4]), Options: f[5],
			FSType: g[0], Source: unescapeMount(g[1]), SuperOpts: g[2],
		})
	}
	return out
}

// SharedFSType reports whether fstype can carry host files into the guest.
func SharedFSType(fstype string) bool {
	switch fstype {
	case "virtiofs", "9p", "nfs", "nfs4", "cifs", "smb3", "vboxsf", "prl_fs", "vmhgfs", "fakeowner", "fuse":
		return true
	}
	return strings.HasPrefix(fstype, "fuse.") && fstype != "fuse.lxcfs"
}

func cleanSuperOpts(opts string) string {
	var keep []string
	for o := range strings.SplitSeq(opts, ",") {
		if o != "ro" && o != "rw" && o != "" {
			keep = append(keep, o)
		}
	}
	return strings.Join(keep, ",")
}
