//go:build linux

package guest

import (
	"math"
	"slices"
	"syscall"
	"testing"
)

func TestOverlayOptions(t *testing.T) {
	t.Parallel()
	got := overlayOptions([]string{bootLowerDir, layerDir + "/1", layerDir + "/0"}, "/rw/upper", "/rw/work")
	want := "lowerdir=/.jailoc/overlay/boot:/.jailoc/overlay/l/1:/.jailoc/overlay/l/0,upperdir=/rw/upper,workdir=/rw/work"
	if got != want {
		t.Errorf("overlayOptions = %q, want %q", got, want)
	}
}

func TestSwapIDs(t *testing.T) {
	t.Parallel()
	all := []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: math.MaxUint32}}
	for _, tt := range []struct {
		name string
		a, b uint32
		want []syscall.SysProcIDMap
	}{
		{"same", 1000, 1000, all},
		{"agent below host", 1000, 1001, []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: 0, Size: 1000},
			{ContainerID: 1000, HostID: 1001, Size: 1},
			{ContainerID: 1001, HostID: 1000, Size: 1},
			{ContainerID: 1002, HostID: 1002, Size: math.MaxUint32 - 1002},
		}},
		{"host below agent", 1000, 501, []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: 0, Size: 501},
			{ContainerID: 501, HostID: 1000, Size: 1},
			{ContainerID: 502, HostID: 502, Size: 498},
			{ContainerID: 1000, HostID: 501, Size: 1},
			{ContainerID: 1001, HostID: 1001, Size: math.MaxUint32 - 1001},
		}},
		{"root", 0, 1000, []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: 1000, Size: 1},
			{ContainerID: 1, HostID: 1, Size: 999},
			{ContainerID: 1000, HostID: 0, Size: 1},
			{ContainerID: 1001, HostID: 1001, Size: math.MaxUint32 - 1001},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := swapIDs(tt.a, tt.b); !slices.Equal(got, tt.want) {
				t.Errorf("swapIDs(%d, %d) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
