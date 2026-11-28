package brokerbin

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := extract(dir, []byte("broker-v1"))
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
	if filepath.Base(first) != FileName {
		t.Fatalf("file name = %q, want %q", filepath.Base(first), FileName)
	}
	again, err := extract(dir, []byte("broker-v1"))
	if err != nil || again != first {
		t.Fatalf("second extract = %q, %v; want %q", again, err, first)
	}
	if err := os.WriteFile(first, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	repaired, err := extract(dir, []byte("broker-v1"))
	if err != nil || repaired != first {
		t.Fatalf("repair extract = %q, %v", repaired, err)
	}
	if got, _ := os.ReadFile(repaired); !bytes.Equal(got, []byte("broker-v1")) { //nolint:gosec // G304: path returned by extract in t.TempDir
		t.Fatalf("content = %q, want broker-v1", got)
	}
	other, err := extract(dir, []byte("broker-v2"))
	if err != nil || other == first || filepath.Dir(other) == filepath.Dir(first) {
		t.Fatalf("new version path = %q, %v; want a different directory than %q", other, err, first)
	}
}

func TestBytesAndPathMatchEmbedding(t *testing.T) {
	t.Parallel()
	data, err := Bytes()
	path, pathErr := Path(t.TempDir())
	if len(embedded()) == 0 {
		if !errors.Is(err, ErrNotEmbedded) {
			t.Fatalf("Bytes error = %v, want ErrNotEmbedded", err)
		}
		if !errors.Is(pathErr, ErrNotEmbedded) || path != "" {
			t.Fatalf("Path = %q, %v; want empty path and ErrNotEmbedded", path, pathErr)
		}
		return
	}
	if err != nil || len(data) == 0 {
		t.Fatalf("Bytes = %d bytes, %v; want embedded data", len(data), err)
	}
	if pathErr != nil {
		t.Fatalf("Path: %v", pathErr)
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304: path returned by Path in t.TempDir
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("extracted content differs from embedded data: %v", err)
	}
}
