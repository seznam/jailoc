package guest

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseMeminfo(t *testing.T) {
	t.Parallel()
	info, err := parseMeminfo(strings.NewReader("MemTotal:        4012345 kB\nMemFree:  100 kB\nHugePages_Total:       0\nbogus line\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info["MemTotal"], uint64(4012345)<<10; got != want {
		t.Errorf("MemTotal = %d, want %d", got, want)
	}
	if got := info["HugePages_Total"]; got != 0 {
		t.Errorf("HugePages_Total = %d, want 0", got)
	}
	if _, err := parseMeminfo(strings.NewReader("MemFree: 1 kB\n")); err == nil {
		t.Error("parseMeminfo without MemTotal: want error")
	}
}

func TestReclaimTarget(t *testing.T) {
	t.Parallel()
	const gib = uint64(1) << 30
	tests := []struct {
		name           string
		active, inactv uint64
		want           uint64
	}{
		{"below floor", 256 << 20, 128 << 20, 0},
		{"step limited", 1 * gib, 1 * gib, gib / 2},
		{"floor limited", 300 << 20, 300 << 20, 88 << 20},
		{"too small", 512 << 20, 16 << 20, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := reclaimTarget(meminfo{"MemTotal": 4 * gib, "Active(file)": tt.active, "Inactive(file)": tt.inactv})
			if got != tt.want {
				t.Errorf("reclaimTarget = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSwapHeader(t *testing.T) {
	t.Parallel()
	page, err := swapHeader(4096, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 4096 || string(page[4086:]) != "SWAPSPACE2" {
		t.Fatalf("signature missing: len %d tail %q", len(page), page[4086:])
	}
	if v := binary.LittleEndian.Uint32(page[1024:]); v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
	if last := binary.LittleEndian.Uint32(page[1028:]); last != (1<<30)/4096-1 {
		t.Errorf("last_page = %d", last)
	}
	if _, err := swapHeader(4096, 4096); err == nil {
		t.Error("tiny swap: want error")
	}
}
