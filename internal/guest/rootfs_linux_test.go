//go:build linux

package guest

import "testing"

func TestOverlayOptions(t *testing.T) {
	t.Parallel()
	got := overlayOptions([]string{bootLowerDir, imageLowerDir}, "/rw/upper", "/rw/work")
	want := "lowerdir=/.jailoc/overlay/boot:/.jailoc/overlay/image,upperdir=/rw/upper,workdir=/rw/work"
	if got != want {
		t.Errorf("overlayOptions = %q, want %q", got, want)
	}
}
