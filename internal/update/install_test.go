package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	hdr  tar.Header
	body []byte
}

func fileEntry(name string, body string) tarEntry {
	return tarEntry{
		hdr:  tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))},
		body: []byte(body),
	}
}

func linkEntry(name, target string, typeflag byte) tarEntry {
	return tarEntry{hdr: tar.Header{Name: name, Typeflag: typeflag, Linkname: target, Mode: 0o777}}
}

func tarGzBytes(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := e.hdr
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatalf("write tar header %q: %v", hdr.Name, err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatalf("write tar body %q: %v", hdr.Name, err)
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

func openArchive(t *testing.T, data []byte) *os.File {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "archive-*.tar.gz")
	if err != nil {
		t.Fatalf("create archive file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write archive file: %v", err)
	}
	// Leave the offset at the end: extractBinary must rewind.

	return f
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %q: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}

	return string(data)
}

func TestResolveExecutable(t *testing.T) {
	t.Parallel()

	t.Run("resolves symlink to absolute regular file", func(t *testing.T) {
		t.Parallel()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		real := filepath.Join(dir, "real")
		writeFile(t, real, "bin", 0o755)
		alias := filepath.Join(dir, "alias")
		if err := os.Symlink(real, alias); err != nil {
			t.Fatal(err)
		}

		got, err := resolveExecutable(func() (string, error) { return alias, nil })
		if err != nil {
			t.Fatalf("resolveExecutable: %v", err)
		}
		if got != real {
			t.Fatalf("got %q, want %q", got, real)
		}
	})

	t.Run("executable lookup error", func(t *testing.T) {
		t.Parallel()
		_, err := resolveExecutable(func() (string, error) { return "", errors.New("boom") })
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "boom")
	})

	t.Run("missing path", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "missing")
		if _, err := resolveExecutable(func() (string, error) { return missing, nil }); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("directory is not regular", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := resolveExecutable(func() (string, error) { return dir, nil })
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "not a regular file")
	})
}

func TestCheckInstallGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		current  string
		force    bool
		explicit bool
		wantErr  string
	}{
		{name: "plain install release", path: "/usr/local/bin/jailoc", current: "v1.2.3"},
		{name: "plain semver without v", path: "/usr/local/bin/jailoc", current: "1.2.3"},
		{name: "nix store refused", path: "/nix/store/abc-jailoc/bin/jailoc", current: "v1.2.3", wantErr: "Nix"},
		{name: "nix store refused with force", path: "/nix/store/abc-jailoc/bin/jailoc", current: "v1.2.3", force: true, wantErr: "Nix"},
		{name: "nix store prefix lookalike allowed", path: "/nix/storefront/jailoc", current: "v1.2.3"},
		{name: "caskroom refused", path: "/opt/homebrew/Caskroom/jailoc/1.2.3/jailoc", current: "v1.2.3", wantErr: "brew upgrade --cask jailoc"},
		{name: "cellar jailoc refused", path: "/opt/homebrew/Cellar/jailoc/1.2.3/bin/jailoc", current: "v1.2.3", wantErr: "brew upgrade --cask jailoc"},
		{name: "other cellar formula allowed", path: "/opt/homebrew/Cellar/other/1.0/bin/jailoc", current: "v1.2.3"},
		{name: "caskroom forced", path: "/opt/homebrew/Caskroom/jailoc/1.2.3/jailoc", current: "v1.2.3", force: true},
		{name: "caskroom explicit version still refused", path: "/opt/homebrew/Caskroom/jailoc/1.2.3/jailoc", current: "v1.2.3", explicit: true, wantErr: "Homebrew"},
		{name: "dev refused", path: "/usr/local/bin/jailoc", current: "dev", wantErr: "not a release build"},
		{name: "devel refused", path: "/usr/local/bin/jailoc", current: "(devel)", wantErr: "not a release build"},
		{name: "invalid refused", path: "/usr/local/bin/jailoc", current: "abc123", wantErr: "not a release build"},
		{name: "empty refused", path: "/usr/local/bin/jailoc", current: "", wantErr: "not a release build"},
		{name: "dev forced", path: "/usr/local/bin/jailoc", current: "dev", force: true},
		{name: "dev with explicit version", path: "/usr/local/bin/jailoc", current: "dev", explicit: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkInstallGuards(tt.path, tt.current, tt.force, tt.explicit)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			assertContains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestExtractBinary(t *testing.T) {
	t.Parallel()

	good := tarGzBytes(t, []tarEntry{
		fileEntry("README.md", "readme"),
		linkEntry("jl", "jailoc", tar.TypeSymlink),
		fileEntry("jailoc", "binary-bytes"),
		fileEntry("LICENSE", "license"),
	})

	t.Run("extracts single jailoc and skips the rest", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()

		path, err := extractBinary(openArchive(t, good), dest)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}
		if filepath.Dir(path) != dest || !strings.HasPrefix(filepath.Base(path), ".jailoc-update-") {
			t.Fatalf("unexpected temp path %q", path)
		}
		if got := readFile(t, path); got != "binary-bytes" {
			t.Fatalf("content %q", got)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode %v, want 0755", info.Mode().Perm())
		}
		if names := dirNames(t, dest); len(names) != 1 {
			t.Fatalf("dest dir has extra files: %v", names)
		}
	})

	hugeHeader := func(size int64) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		// Header only: extraction must reject it before reading any body.
		if err := tw.WriteHeader(&tar.Header{Name: "jailoc", Typeflag: tar.TypeReg, Mode: 0o755, Size: size}); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}

		return buf.Bytes()
	}

	manyEntries := make([]tarEntry, 0, maxArchiveHeaders+1)
	for i := range maxArchiveHeaders {
		manyEntries = append(manyEntries, fileEntry("f"+strings.Repeat("x", i+1), "x"))
	}
	manyEntries = append(manyEntries, fileEntry("jailoc", "bin"))

	trailingGarbage := append(append([]byte{}, good...), []byte("garbage")...)
	badCRC := append([]byte{}, good...)
	badCRC[len(badCRC)-8] ^= 0xff
	notGzip := []byte("not a gzip stream at all")

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{name: "missing jailoc", data: tarGzBytes(t, []tarEntry{fileEntry("README.md", "x"), fileEntry("./jailoc", "x")}), wantErr: "does not contain"},
		{name: "duplicate jailoc", data: tarGzBytes(t, []tarEntry{fileEntry("jailoc", "one"), fileEntry("jailoc", "two")}), wantErr: "more than one"},
		{name: "duplicate after other entries", data: tarGzBytes(t, []tarEntry{fileEntry("jailoc", "one"), fileEntry("x", "y"), fileEntry("jailoc", "two")}), wantErr: "more than one"},
		{name: "jailoc symlink", data: tarGzBytes(t, []tarEntry{linkEntry("jailoc", "/bin/sh", tar.TypeSymlink)}), wantErr: "not a regular file"},
		{name: "jailoc hardlink", data: tarGzBytes(t, []tarEntry{fileEntry("other", "x"), linkEntry("jailoc", "other", tar.TypeLink)}), wantErr: "not a regular file"},
		{name: "jailoc directory", data: tarGzBytes(t, []tarEntry{{hdr: tar.Header{Name: "jailoc", Typeflag: tar.TypeDir, Mode: 0o755}}}), wantErr: "not a regular file"},
		{name: "empty jailoc", data: tarGzBytes(t, []tarEntry{fileEntry("jailoc", "")}), wantErr: "size"},
		{name: "oversize jailoc", data: hugeHeader(maxBinarySize + 1), wantErr: "exceeds"},
		{name: "too many entries", data: tarGzBytes(t, manyEntries), wantErr: "more than"},
		{name: "truncated mid stream", data: good[:len(good)/2], wantErr: "read archive"},
		{name: "truncated trailer", data: good[:len(good)-4], wantErr: "read archive"},
		{name: "corrupt checksum", data: badCRC, wantErr: "checksum"},
		{name: "trailing garbage", data: trailingGarbage, wantErr: "read archive"},
		{name: "not gzip", data: notGzip, wantErr: "gzip"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dest := t.TempDir()

			path, err := extractBinary(openArchive(t, tt.data), dest)
			if err == nil {
				t.Fatalf("expected error, got path %q", path)
			}
			assertContains(t, err.Error(), tt.wantErr)
			if path != "" {
				t.Fatalf("path %q returned with error", path)
			}
			if names := dirNames(t, dest); len(names) != 0 {
				t.Fatalf("temp files left behind: %v", names)
			}
		})
	}

	t.Run("expansion bomb in skipped entry", func(t *testing.T) {
		t.Parallel()
		if testing.Short() {
			t.Skip("writes ~270 MiB of zeros through gzip")
		}

		var buf bytes.Buffer
		gz, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(gz)
		size := maxExpandedArchiveSize + 1<<20
		if err := tw.WriteHeader(&tar.Header{Name: "padding", Typeflag: tar.TypeReg, Mode: 0o644, Size: size}); err != nil {
			t.Fatal(err)
		}
		chunk := make([]byte, 1<<20)
		for written := int64(0); written < size; written += int64(len(chunk)) {
			if _, err := tw.Write(chunk); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: "jailoc", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}

		dest := t.TempDir()
		if _, err := extractBinary(openArchive(t, buf.Bytes()), dest); err == nil {
			t.Fatal("expected error")
		} else {
			assertContains(t, err.Error(), "expands to more than")
		}
		if names := dirNames(t, dest); len(names) != 0 {
			t.Fatalf("temp files left behind: %v", names)
		}
	})

	t.Run("destination directory missing", func(t *testing.T) {
		t.Parallel()
		_, err := extractBinary(openArchive(t, good), filepath.Join(t.TempDir(), "missing"))
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "create temporary file")
	})

	t.Run("read-only destination hints about permissions", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dest := t.TempDir()
		if err := os.Chmod(dest, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dest, 0o600) })

		_, err := extractBinary(openArchive(t, good), dest)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "write permission")
	})
}

func newInstallFixture(t *testing.T, oldContent, newContent string) (target, tmp string) {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(dir, "jailoc")
	writeFile(t, target, oldContent, 0o755)
	tmp = filepath.Join(dir, ".jailoc-update-test")
	writeFile(t, tmp, newContent, 0o600)

	return target, tmp
}

func TestReplaceExecutable(t *testing.T) {
	t.Parallel()

	t.Run("replaces target atomically with a new inode", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		old, err := os.Open(filepath.Clean(target))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = old.Close() }()

		if err := replaceExecutable(context.Background(), target, tmp); err != nil {
			t.Fatalf("replaceExecutable: %v", err)
		}
		if got := readFile(t, target); got != "new" {
			t.Fatalf("target content %q", got)
		}
		// The running (opened) old file keeps its bytes: no in-place truncation.
		oldBytes, err := io.ReadAll(old)
		if err != nil {
			t.Fatal(err)
		}
		if string(oldBytes) != "old" {
			t.Fatalf("old inode content changed to %q", oldBytes)
		}
		if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tmp should be gone, got %v", err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("mode %v, want 0755", info.Mode().Perm())
		}
	})

	t.Run("strips setuid bits", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		if err := os.Chmod(tmp, 0o755|os.ModeSetuid|os.ModeSetgid); err != nil {
			t.Fatal(err)
		}

		if err := replaceExecutable(context.Background(), target, tmp); err != nil {
			t.Fatalf("replaceExecutable: %v", err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Fatalf("special bits survived: %v", info.Mode())
		}
	})

	t.Run("symlink alias is preserved when installing to the resolved target", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		aliasDir := t.TempDir()
		alias := filepath.Join(aliasDir, "jailoc")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}

		resolved, err := resolveExecutable(func() (string, error) { return alias, nil })
		if err != nil {
			t.Fatal(err)
		}
		if err := replaceExecutable(context.Background(), resolved, tmp); err != nil {
			t.Fatalf("replaceExecutable: %v", err)
		}

		info, err := os.Lstat(alias)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("alias is no longer a symlink")
		}
		if got := readFile(t, alias); got != "new" {
			t.Fatalf("alias content %q", got)
		}
		if names := dirNames(t, aliasDir); len(names) != 1 {
			t.Fatalf("alias dir polluted: %v", names)
		}
	})

	t.Run("cancelled context leaves old bytes and removes tmp", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := replaceExecutable(ctx, target, tmp)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		if got := readFile(t, target); got != "old" {
			t.Fatalf("target changed to %q", got)
		}
		if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tmp not cleaned up: %v", err)
		}
	})

	t.Run("directory sync failure reports installed", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		syncErr := errors.New("sync boom")

		err := replaceExecutableWith(context.Background(), target, tmp, func(string) error { return syncErr })
		if !errors.Is(err, ErrInstalledSyncFailed) || !errors.Is(err, syncErr) {
			t.Fatalf("got %v, want ErrInstalledSyncFailed wrapping sync error", err)
		}
		if got := readFile(t, target); got != "new" {
			t.Fatalf("target content %q", got)
		}
	})

	t.Run("tmp equal to target is rejected and target kept", func(t *testing.T) {
		t.Parallel()
		target, _ := newInstallFixture(t, "old", "new")

		err := replaceExecutable(context.Background(), target, target)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "install target")
		if got := readFile(t, target); got != "old" {
			t.Fatalf("target changed to %q", got)
		}
	})

	t.Run("tmp hardlinked to target is rejected", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		if err := os.Remove(tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, tmp); err != nil {
			t.Fatal(err)
		}

		if err := replaceExecutable(context.Background(), target, tmp); err == nil {
			t.Fatal("expected error")
		}
		if got := readFile(t, target); got != "old" {
			t.Fatalf("target changed to %q", got)
		}
	})

	t.Run("tmp in another directory is rejected and untouched", func(t *testing.T) {
		t.Parallel()
		target, _ := newInstallFixture(t, "old", "new")
		other := filepath.Join(t.TempDir(), ".jailoc-update-x")
		writeFile(t, other, "new", 0o600)

		err := replaceExecutable(context.Background(), target, other)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "not next to target")
		if got := readFile(t, target); got != "old" {
			t.Fatalf("target changed to %q", got)
		}
		if got := readFile(t, other); got != "new" {
			t.Fatal("foreign tmp must be left untouched")
		}
	})

	t.Run("symlink target is rejected", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		alias := filepath.Join(filepath.Dir(target), "alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}

		err := replaceExecutable(context.Background(), alias, tmp)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "not a regular file")
		if got := readFile(t, target); got != "old" {
			t.Fatalf("target changed to %q", got)
		}
	})

	t.Run("missing target", func(t *testing.T) {
		t.Parallel()
		target, tmp := newInstallFixture(t, "old", "new")
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}

		if err := replaceExecutable(context.Background(), target, tmp); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("non-regular tmp is rejected", func(t *testing.T) {
		t.Parallel()
		target, _ := newInstallFixture(t, "old", "new")
		tmpDir := filepath.Join(filepath.Dir(target), ".jailoc-update-dir")
		if err := os.Mkdir(tmpDir, 0o750); err != nil {
			t.Fatal(err)
		}

		err := replaceExecutable(context.Background(), target, tmpDir)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "not a regular file")
	})
}
