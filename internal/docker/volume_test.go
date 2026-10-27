package docker

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarOf(t *testing.T, entries ...tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "data:" + h.Name
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestExtractTar(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "out")
	archive := tarOf(t,
		tar.Header{Name: "volume/", Typeflag: tar.TypeDir, Mode: 0o755},
		tar.Header{Name: "volume/storage/", Typeflag: tar.TypeDir, Mode: 0o700},
		tar.Header{Name: "volume/storage/session.json", Typeflag: tar.TypeReg, Mode: 0o600},
		tar.Header{Name: "volume/link", Typeflag: tar.TypeSymlink, Linkname: "storage/session.json"},
		tar.Header{Name: "volume/fifo", Typeflag: tar.TypeFifo},
	)
	if err := extractTar(archive, dst); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "storage", "session.json")) //nolint:gosec // G304: test file
	if err != nil || string(got) != "data:volume/storage/session.json" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if link, err := os.Readlink(filepath.Join(dst, "link")); err != nil || link != "storage/session.json" {
		t.Fatalf("link = %q, %v", link, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "fifo")); !os.IsNotExist(err) {
		t.Fatalf("fifo should be skipped: %v", err)
	}
}

func TestExtractTarRejectsEscape(t *testing.T) {
	t.Parallel()
	archive := tarOf(t, tar.Header{Name: "volume/../../evil", Typeflag: tar.TypeReg, Mode: 0o600})
	err := extractTar(archive, filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("extractTar error = %v, want escape rejection", err)
	}
}
