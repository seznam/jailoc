package workspace

import "testing"

func TestPathSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want int
	}{
		{"/a/b/c", 3},
		{"/a/bb", 2},
		{"/a", 1},
		{"/", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := pathSegments(tt.path); got != tt.want {
				t.Fatalf("pathSegments(%q) = %d, want %d", tt.path, got, tt.want)
			}
		})
	}
}
