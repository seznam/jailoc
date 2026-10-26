package guestbin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := extract(dir, []byte("guest-v1"))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	info, err := os.Stat(first)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
	again, err := extract(dir, []byte("guest-v1"))
	if err != nil || again != first {
		t.Fatalf("second extract = %q, %v; want %q", again, err, first)
	}
	if err := os.WriteFile(first, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	repaired, err := extract(dir, []byte("guest-v1"))
	if err != nil || repaired != first {
		t.Fatalf("repair extract = %q, %v", repaired, err)
	}
	if got, _ := os.ReadFile(repaired); !bytes.Equal(got, []byte("guest-v1")) { //nolint:gosec // G304: path returned by extract in t.TempDir
		t.Fatalf("content = %q, want guest-v1", got)
	}
	other, err := extract(dir, []byte("guest-v2"))
	if err != nil || other == first || filepath.Dir(other) == filepath.Dir(first) {
		t.Fatalf("new version path = %q, %v; want a different directory than %q", other, err, first)
	}
}
