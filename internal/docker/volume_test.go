package docker

import (
	"archive/tar"
	"bytes"
	"io"
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
	if err := extractTarAt(t, archive, dst); err != nil {
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

func TestExtractTarRejectsEntryOutsideDestination(t *testing.T) {
	t.Parallel()
	archive := tarOf(t, tar.Header{Name: "volume/../../evil", Typeflag: tar.TypeReg, Mode: 0o600})
	err := extractTarAt(t, archive, filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "outside the destination") {
		t.Fatalf("extractTar error = %v, want outside-destination rejection", err)
	}
}

func TestExtractTarDoesNotFollowLinkOutsideDestination(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dst := filepath.Join(base, "dst")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := tarOf(t,
		tar.Header{Name: "volume/out", Typeflag: tar.TypeSymlink, Linkname: outside},
		tar.Header{Name: "volume/out/pwned", Typeflag: tar.TypeReg, Mode: 0o600},
	)
	if err := extractTarAt(t, archive, dst); err == nil {
		t.Fatal("extractTar error = nil, want an error for a file under a link leaving the destination")
	}
	if _, err := os.Lstat(filepath.Join(outside, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not exist: %v", err)
	}
}

func TestExtractTarKeepsAbsoluteLink(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "out")
	archive := tarOf(t, tar.Header{Name: "volume/link", Typeflag: tar.TypeSymlink, Linkname: "/home/agent/data"})
	if err := extractTarAt(t, archive, dst); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	got, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil || got != "/home/agent/data" {
		t.Fatalf("link = %q, %v; want /home/agent/data", got, err)
	}
}

// extractTarAt extracts archive into the absolute path dst.
func extractTarAt(t *testing.T, archive io.Reader, dst string) error {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return extractTar(archive, root, filepath.Base(dst))
}
