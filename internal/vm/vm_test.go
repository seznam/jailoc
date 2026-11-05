package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDisk(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "disk.img")
	size := func() int64 {
		t.Helper()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat disk: %v", err)
		}
		return st.Size()
	}
	if err := ensureDisk(path, 2<<30); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := size(); got != 2<<30 {
		t.Fatalf("created size = %d, want %d", got, int64(2<<30))
	}
	if err := ensureDisk(path, 3<<30); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if got := size(); got != 3<<30 {
		t.Fatalf("grown size = %d, want %d", got, int64(3<<30))
	}
	err := ensureDisk(path, 1<<30)
	if err == nil || !strings.Contains(err.Error(), "cannot shrink") || !strings.Contains(err.Error(), "at least 3072m") {
		t.Fatalf("shrink: got %v, want a cannot shrink error naming 3072m", err)
	}
	if got := size(); got != 3<<30 {
		t.Fatalf("size after refused shrink = %d, want %d", got, int64(3<<30))
	}
}
