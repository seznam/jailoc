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

func TestMaxSharesExtraDisks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		docker bool
		tmp    bool
		extra  []bool
		want   int
	}{
		{"no extras", false, false, nil, 11},
		{"state only", false, false, []bool{true, false}, 10},
		{"cache only", false, false, []bool{false, true}, 10},
		{"state and cache", false, false, []bool{true, true}, 9},
		{"all disks", true, true, []bool{true, true}, 7},
		{"false extras", true, false, []bool{false, false}, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := maxShares("amd64", tt.docker, tt.tmp, tt.extra...); got != tt.want {
				t.Errorf("maxShares = %d, want %d", got, tt.want)
			}
		})
	}
}
