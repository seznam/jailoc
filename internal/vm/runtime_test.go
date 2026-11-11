package vm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestUnpackRuntimeOnce(t *testing.T) {
	t.Parallel()
	rt, fw := makeTarball(t, goodRuntime), makeTarball(t, goodFirmware)
	cache := t.TempDir()
	var progress bytes.Buffer

	runner, libDir, err := unpackRuntime(cache, rt, fw, &progress)
	if err != nil {
		t.Fatalf("unpackRuntime: %v", err)
	}
	dir := filepath.Dir(filepath.Dir(runner))
	if filepath.Dir(dir) != cache || filepath.Base(runner) != RunnerBinary {
		t.Fatalf("runner = %q, want <cache>/<key>/runtime/%s", runner, RunnerBinary)
	}
	wantLib := filepath.Join(dir, "runtime") + string(os.PathListSeparator) + filepath.Join(dir, "firmware")
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
	if b, err := os.ReadFile(filepath.Join(dir, "firmware", "libkrunfw.5.dylib")); err != nil || string(b) != "fw" { //nolint:gosec // G304: test temp dir
		t.Fatalf("firmware = %q, %v", b, err)
	}
	if !strings.Contains(progress.String(), "Unpacking VM runtime") {
		t.Fatalf("progress = %q", progress.String())
	}

	progress.Reset()
	again, _, err := unpackRuntime(cache, rt, fw, &progress)
	if err != nil {
		t.Fatalf("second unpackRuntime: %v", err)
	}
	if again != runner || progress.Len() != 0 {
		t.Fatalf("second call: runner %q, progress %q; want the cached runtime", again, progress.String())
	}

	// Removing the runner binary forces a re-unpack even when .complete matches.
	if err := os.Remove(runner); err != nil {
		t.Fatal(err)
	}
	repaired, _, err := unpackRuntime(cache, rt, fw, &progress)
	if err != nil {
		t.Fatalf("repair unpackRuntime: %v", err)
	}
	if repaired != runner || !strings.Contains(progress.String(), "Unpacking VM runtime") {
		t.Fatalf("repair call: runner %q, progress %q; want re-unpacked runtime", repaired, progress.String())
	}
	if _, err := os.Stat(runner); err != nil {
		t.Fatalf("stat repaired runner: %v", err)
	}

	other := makeTarball(t, append(goodFirmware[:1:1], tarEntry{name: "fw/VERSION", body: "v2", mode: 0o644}))
	runner2, _, err := unpackRuntime(cache, rt, other, io.Discard)
	if err != nil {
		t.Fatalf("unpackRuntime with other firmware: %v", err)
	}
	if runner2 == runner {
		t.Fatalf("different firmware reused runtime directory %q", runner)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 2 {
		t.Fatalf("cache dir has %d entries, want one per runtime", len(entries))
	}
}

func TestUnpackRuntimeRejects(t *testing.T) {
	t.Parallel()
	fw := makeTarball(t, goodFirmware)
	tests := []struct {
		name    string
		runtime []byte
		want    string
	}{
		{name: "not gzip", runtime: []byte("plain"), want: "open gzip"},
		{name: "path traversal", runtime: makeTarball(t, []tarEntry{{name: "../evil", body: "x", mode: 0o644}}), want: "unsafe path"},
		{name: "absolute path", runtime: makeTarball(t, []tarEntry{{name: "/etc/evil", body: "x", mode: 0o644}}), want: "unsafe path"},
		{name: "symlink", runtime: makeTarball(t, []tarEntry{{name: "rt/link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}}), want: "unsupported entry"},
		{name: "missing runner", runtime: makeTarball(t, []tarEntry{{name: "rt/libkrun.1.dylib", body: "x", mode: 0o644}}), want: "missing from the runtime tarball"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cache := t.TempDir()
			_, _, err := unpackRuntime(cache, tt.runtime, fw, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if entries, _ := os.ReadDir(cache); len(entries) != 0 {
				t.Fatalf("cache has %d entries after failure, want none", len(entries))
			}
		})
	}
}

func TestWriteSpecAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spec := Spec{
		Workspace: "test-ws",
		StateDir:  dir,
		Image:     "jailoc-test:latest",
		CPUs:      2,
		MemoryMiB: 1024,
	}
	if err := WriteSpec(spec); err != nil {
		t.Fatalf("WriteSpec: %v", err)
	}
	read, err := ReadSpec(dir)
	if err != nil {
		t.Fatalf("ReadSpec: %v", err)
	}
	if read.Workspace != spec.Workspace || read.Image != spec.Image {
		t.Fatalf("read = %+v, want %+v", read, spec)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "spec.json" {
		t.Fatalf("entries = %v, want only spec.json without leftover temp files", entries)
	}
}
