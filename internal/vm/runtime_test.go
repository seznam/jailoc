package vm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type tarEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	link     string
}

func makeTarball(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: e.typeflag, Linkname: e.link}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func serve(t *testing.T, files map[string][]byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

var (
	goodRuntime = []tarEntry{
		{name: "rt/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "rt/" + RunnerBinary, body: "runner", mode: 0o755},
		{name: "rt/libkrun.1.dylib", body: "libkrun", mode: 0o644},
	}
	goodFirmware = []tarEntry{
		{name: "fw/libkrunfw.5.dylib", body: "fw", mode: 0o644},
		{name: "fw/LICENSE-GPL", body: "GPL", mode: 0o644},
	}
)

func TestEnsureReleaseDownloadsOnce(t *testing.T) {
	t.Parallel()
	rt, fw := makeTarball(t, goodRuntime), makeTarball(t, goodFirmware)
	ts, hits := serve(t, map[string][]byte{"/rt.tar.gz": rt, "/fw.tar.gz": fw})
	rel := Release{
		Version:  "v1",
		Runtime:  Artifact{URL: ts.URL + "/rt.tar.gz", SHA256: sum(rt)},
		Firmware: Artifact{URL: ts.URL + "/fw.tar.gz", SHA256: sum(fw)},
	}
	cache := t.TempDir()
	var progress bytes.Buffer

	runner, libDir, err := ensureRelease(context.Background(), ts.Client(), cache, rel, &progress)
	if err != nil {
		t.Fatalf("ensureRelease: %v", err)
	}
	if want := filepath.Join(cache, "v1", "runtime", RunnerBinary); runner != want {
		t.Fatalf("runner = %q, want %q", runner, want)
	}
	wantLib := filepath.Join(cache, "v1", "runtime") + string(os.PathListSeparator) + filepath.Join(cache, "v1", "firmware")
	if libDir != wantLib {
		t.Fatalf("libDir = %q, want %q", libDir, wantLib)
	}
	info, err := os.Stat(runner)
	if err != nil {
		t.Fatalf("stat runner: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("runner mode %v is not executable", info.Mode())
	}
	if b, err := os.ReadFile(filepath.Join(cache, "v1", "firmware", "libkrunfw.5.dylib")); err != nil || string(b) != "fw" { //nolint:gosec // G304: test temp dir
		t.Fatalf("firmware = %q, %v", b, err)
	}
	if !strings.Contains(progress.String(), "Downloading VM runtime v1") {
		t.Fatalf("progress = %q", progress.String())
	}

	progress.Reset()
	if _, _, err := ensureRelease(context.Background(), ts.Client(), cache, rel, &progress); err != nil {
		t.Fatalf("second ensureRelease: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2 (second call must use the cache)", got)
	}
	if progress.Len() != 0 {
		t.Fatalf("cached call printed %q", progress.String())
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 1 {
		t.Fatalf("cache dir has %d entries, want only the release", len(entries))
	}
}

func TestEnsureReleaseRejects(t *testing.T) {
	t.Parallel()
	fw := makeTarball(t, goodFirmware)
	tests := []struct {
		name    string
		runtime []tarEntry
		badSum  bool
		want    string
	}{
		{name: "checksum mismatch", runtime: goodRuntime, badSum: true, want: "checksum mismatch"},
		{name: "path traversal", runtime: []tarEntry{{name: "../evil", body: "x", mode: 0o644}}, want: "unsafe path"},
		{name: "absolute path", runtime: []tarEntry{{name: "/etc/evil", body: "x", mode: 0o644}}, want: "unsafe path"},
		{name: "symlink", runtime: []tarEntry{{name: "rt/link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}}, want: "unsupported entry"},
		{name: "missing runner", runtime: []tarEntry{{name: "rt/libkrun.1.dylib", body: "x", mode: 0o644}}, want: "missing from the runtime tarball"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rt := makeTarball(t, tt.runtime)
			ts, _ := serve(t, map[string][]byte{"/rt.tar.gz": rt, "/fw.tar.gz": fw})
			rtSum := sum(rt)
			if tt.badSum {
				rtSum = strings.Repeat("0", 64)
			}
			rel := Release{
				Version:  "v1",
				Runtime:  Artifact{URL: ts.URL + "/rt.tar.gz", SHA256: rtSum},
				Firmware: Artifact{URL: ts.URL + "/fw.tar.gz", SHA256: sum(fw)},
			}
			cache := t.TempDir()
			_, _, err := ensureRelease(context.Background(), ts.Client(), cache, rel, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if _, statErr := os.Stat(filepath.Join(cache, "v1")); !os.IsNotExist(statErr) {
				t.Fatalf("release directory exists after failure: %v", statErr)
			}
		})
	}
}
