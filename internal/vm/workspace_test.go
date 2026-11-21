package vm

import "testing"

func TestMaxShares(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arch   string
		docker bool
		tmp    bool
		want   int
	}{
		{"amd64", false, false, 11},
		{"amd64", true, false, 10},
		{"amd64", false, true, 10},
		{"amd64", true, true, 9},
		{"arm64", false, false, 120},
		{"arm64", true, false, 119},
		{"arm64", false, true, 119},
		{"arm64", true, true, 118},
	}
	for _, tt := range tests {
		if got := maxShares(tt.arch, tt.docker, tt.tmp); got != tt.want {
			t.Errorf("maxShares(%q, docker=%v, tmp=%v) = %d, want %d", tt.arch, tt.docker, tt.tmp, got, tt.want)
		}
	}
}
