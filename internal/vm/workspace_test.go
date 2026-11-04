package vm

import "testing"

func TestMaxShares(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arch   string
		docker bool
		want   int
	}{
		{"amd64", false, 11},
		{"amd64", true, 10},
		{"arm64", false, 120},
		{"arm64", true, 119},
	}
	for _, tt := range tests {
		if got := maxShares(tt.arch, tt.docker); got != tt.want {
			t.Errorf("maxShares(%q, %v) = %d, want %d", tt.arch, tt.docker, got, tt.want)
		}
	}
}
