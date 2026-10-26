package vmprobe

import (
	"slices"
	"testing"
)

func TestParsePortRanges(t *testing.T) {
	t.Parallel()
	got, err := ParsePortRanges("1-3, 10,20-20")
	if err != nil {
		t.Fatalf("ParsePortRanges: %v", err)
	}
	if want := []int{1, 2, 3, 10, 20}; !slices.Equal(got, want) {
		t.Fatalf("ParsePortRanges = %v, want %v", got, want)
	}
	for _, bad := range []string{"x", "5-1", "1-y"} {
		if _, err := ParsePortRanges(bad); err == nil {
			t.Errorf("ParsePortRanges(%q) = nil error", bad)
		}
	}
}

func TestParseMountInfo(t *testing.T) {
	t.Parallel()
	data := `22 1 0:21 / / rw,relatime - ext4 /dev/vda1 rw
40 22 0:35 /projects /Users/me/my\040proj rw,relatime shared:1 - virtiofs mount0 rw
41 22 0:36 / /mnt/ro ro,relatime - 9p share1 ro,trans=virtio,version=9p2000.L
42 22 0:37 / /var/lib/lxcfs rw - fuse.lxcfs lxcfs rw
`
	m := ParseMountInfo(data)
	if len(m) != 4 {
		t.Fatalf("got %d mounts, want 4", len(m))
	}
	if m[1].MountPoint != "/Users/me/my proj" || m[1].Root != "/projects" || m[1].FSType != "virtiofs" || m[1].Source != "mount0" {
		t.Fatalf("unexpected virtiofs entry %+v", m[1])
	}
	var shared []string
	for _, x := range m {
		if SharedFSType(x.FSType) {
			shared = append(shared, x.MountPoint)
		}
	}
	if want := []string{"/Users/me/my proj", "/mnt/ro"}; !slices.Equal(shared, want) {
		t.Fatalf("shared mounts = %v, want %v", shared, want)
	}
	if got := cleanSuperOpts(m[2].SuperOpts); got != "trans=virtio,version=9p2000.L" {
		t.Fatalf("cleanSuperOpts = %q", got)
	}
}
