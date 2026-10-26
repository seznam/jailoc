package vm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// File names inside a runtime bundle: the VM runner and the guest agent
// injected into workspace VMs.
const (
	RunnerBinary = "go-microvm-runner"
	GuestBinary  = "jailoc-guest"
)

const (
	maxArtifactSize  = 256 << 20
	downloadTimeout  = 10 * time.Minute
	runtimeCompleted = ".complete"
)

// Artifact is a pinned tarball: its URL and expected SHA-256.
type Artifact struct {
	URL    string
	SHA256 string
}

// Release is a pinned VM runtime for one platform: the runtime tarball
// (runner and libkrun) and the firmware tarball (libkrunfw, GPL-2.0).
// Both use the go-microvm release layout: one top-level directory holding
// the files.
type Release struct {
	Version  string
	Runtime  Artifact
	Firmware Artifact
}

// runtimeMirror replaces the scheme and host of every pinned URL when set at
// build time (-ldflags -X), so release candidates can be tested against a
// local server. The checksums still apply.
var runtimeMirror string

// releases pins the runtime per GOOS/GOARCH. The runtime tarball is built by
// .github/workflows/vm-runtime.yml from the go-microvm revision in go.mod
// (block disks and simplified virtio-fs ownership) with libkrun from the
// go-microvm v0.0.41 release; the firmware is that release's artifact.
var releases = map[string]Release{
	"darwin/arm64": {
		Version: "v0.0.41-jailoc.1",
		Runtime: Artifact{
			URL:    "https://github.com/seznam/jailoc/releases/download/vm-runtime-v0.0.41-jailoc.1/go-microvm-runtime-darwin-arm64.tar.gz",
			SHA256: "71dd368b6cc819863f4d2d6825e351cbe559d6eae280830b080e1ca5e8c9ebca",
		},
		Firmware: Artifact{
			URL:    "https://github.com/stacklok/go-microvm/releases/download/v0.0.41/go-microvm-firmware-darwin-arm64.tar.gz",
			SHA256: "02ff1ca992c3b104cf6c4d0c2995fac5aa8646e7f7cafa0ee72b734b69fc5216",
		},
	},
}

// EnsureRuntime returns the runner and library directory of the pinned VM
// runtime for this platform, downloading and unpacking it into cacheDir on
// first use. Progress messages go to progress.
func EnsureRuntime(ctx context.Context, cacheDir string, progress io.Writer) (runner, libDir string, err error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	rel, ok := releases[platform]
	if !ok {
		return "", "", fmt.Errorf("no VM runtime available for %s; set runner and lib_dir in the [vm] section of the config", platform)
	}
	return ensureRelease(ctx, http.DefaultClient, cacheDir, rel, progress)
}

func ensureRelease(ctx context.Context, client *http.Client, cacheDir string, rel Release, progress io.Writer) (string, string, error) {
	dir := filepath.Join(cacheDir, rel.Version)
	runner := filepath.Join(dir, "runtime", RunnerBinary)
	libDir := filepath.Join(dir, "runtime") + string(os.PathListSeparator) + filepath.Join(dir, "firmware")
	stamp := rel.Runtime.SHA256 + " " + rel.Firmware.SHA256 + "\n"
	if b, err := os.ReadFile(filepath.Join(dir, runtimeCompleted)); err == nil && string(b) == stamp { //nolint:gosec // G304: path inside the jailoc cache directory
		return runner, libDir, nil
	}

	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return "", "", fmt.Errorf("create runtime cache: %w", err)
	}
	tmp, err := os.MkdirTemp(cacheDir, ".download-")
	if err != nil {
		return "", "", fmt.Errorf("create runtime staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	_, _ = fmt.Fprintf(progress, "Downloading VM runtime %s...\n", rel.Version)
	for _, part := range []struct {
		name string
		art  Artifact
	}{{"runtime", rel.Runtime}, {"firmware", rel.Firmware}} {
		if err := fetchArtifact(ctx, client, part.art, filepath.Join(tmp, part.name)); err != nil {
			return "", "", fmt.Errorf("VM runtime %s %s: %w", rel.Version, part.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "runtime", RunnerBinary)); err != nil {
		return "", "", fmt.Errorf("VM runtime %s: %s missing from the runtime tarball", rel.Version, RunnerBinary)
	}
	if err := os.WriteFile(filepath.Join(tmp, runtimeCompleted), []byte(stamp), 0o600); err != nil {
		return "", "", fmt.Errorf("write runtime stamp: %w", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		return "", "", fmt.Errorf("remove stale runtime: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		// A concurrent jailoc may have installed the same release first.
		if b, rerr := os.ReadFile(filepath.Join(dir, runtimeCompleted)); rerr == nil && string(b) == stamp { //nolint:gosec // G304: path inside the jailoc cache directory
			return runner, libDir, nil
		}
		return "", "", fmt.Errorf("install VM runtime: %w", err)
	}
	return runner, libDir, nil
}

// fetchArtifact downloads a tarball, verifies its checksum and unpacks its
// single top-level directory into dest.
func fetchArtifact(ctx context.Context, client *http.Client, art Artifact, dest string) error {
	u, err := artifactURL(art.URL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req) //nolint:gosec // G107: URL is pinned in the binary
	if err != nil {
		return fmt.Errorf("download %s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", u, resp.StatusCode)
	}

	f, err := os.CreateTemp(filepath.Dir(dest), ".artifact-")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxArtifactSize+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", u, err)
	}
	if n > maxArtifactSize {
		return fmt.Errorf("download %s: larger than %d bytes", u, maxArtifactSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != art.SHA256 {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", u, got, art.SHA256)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind %s: %w", f.Name(), err)
	}
	return untar(f, dest)
}

func artifactURL(raw string) (string, error) {
	if runtimeMirror == "" {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", raw, err)
	}
	m, err := url.Parse(runtimeMirror)
	if err != nil {
		return "", fmt.Errorf("parse runtime mirror: %w", err)
	}
	u.Scheme, u.Host = m.Scheme, m.Host
	return u.String(), nil
}

// untar unpacks a gzip tarball whose entries share one top-level directory,
// stripping that directory. Only regular files and directories are accepted.
func untar(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tarball: %w", err)
		}
		name := path.Clean(hdr.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe path %q in tarball", hdr.Name)
		}
		_, rel, found := strings.Cut(name, "/")
		if !found || rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxArtifactSize {
				return fmt.Errorf("tarball expands beyond %d bytes", maxArtifactSize)
			}
			if err := writeFile(target, tr, hdr.Size, os.FileMode(hdr.Mode).Perm()&0o755); err != nil { //nolint:gosec // G115: tar mode bits fit in FileMode
				return err
			}
		default:
			return fmt.Errorf("unsupported entry %q in tarball", hdr.Name)
		}
	}
}

func writeFile(target string, r io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o400) //nolint:gosec // G304: target is inside the staging directory
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	if _, err := io.CopyN(f, r, size); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	return nil
}
