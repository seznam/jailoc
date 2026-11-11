package vm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RunnerBinary is the file name of the VM runner inside a runtime bundle.
const RunnerBinary = "jailoc-vm-runner"

const (
	maxArtifactSize  = 256 << 20
	runtimeCompleted = ".complete"
	// runtimeTarball and firmwareTarball are the file names of the embedded
	// runtime (runner and libkrun) and firmware (libkrunfw, GPL-2.0)
	// tarballs inside runtimebin/<goos>-<goarch>/.
	runtimeTarball  = "runtime.tar.gz"
	firmwareTarball = "firmware.tar.gz"
)

// ErrRuntimeNotEmbedded reports a jailoc build without the VM runtime.
var ErrRuntimeNotEmbedded = errors.New("this jailoc build does not include the VM runtime")

// EnsureRuntime returns the runner and library directory of the VM runtime
// embedded in this build, unpacking it into cacheDir on first use. Release
// builds embed the runtime that .github/workflows/vm-runtime.yml builds for
// darwin/arm64, linux/amd64 and linux/arm64; other builds report
// ErrRuntimeNotEmbedded. Progress messages go to progress.
func EnsureRuntime(cacheDir string, progress io.Writer) (runner, libDir string, err error) {
	rt, fw := embeddedRuntime()
	if len(rt) == 0 || len(fw) == 0 {
		return "", "", fmt.Errorf("%w for %s/%s; use a release build of jailoc or set runner and lib_dir in the [vm] section of the config", ErrRuntimeNotEmbedded, runtime.GOOS, runtime.GOARCH)
	}
	return unpackRuntime(cacheDir, rt, fw, progress)
}

// embeddedRuntime returns the runtime and firmware tarballs embedded for
// this platform, or nil when the build has none.
func embeddedRuntime() (rt, fw []byte) {
	if runtimeDir == "" {
		return nil, nil
	}
	// A missing file means the build carries no runtime; the caller reports it.
	rt, _ = runtimeFiles.ReadFile(path.Join(runtimeDir, runtimeTarball))
	fw, _ = runtimeFiles.ReadFile(path.Join(runtimeDir, firmwareTarball))
	return rt, fw
}

// unpackRuntime unpacks the runtime and firmware tarballs into a cacheDir
// subdirectory keyed by their SHA-256, so every runtime is unpacked once and
// is shared by jailoc builds that embed the same one.
func unpackRuntime(cacheDir string, rt, fw []byte, progress io.Writer) (string, string, error) {
	rtSum, fwSum := sha256.Sum256(rt), sha256.Sum256(fw)
	stamp := hex.EncodeToString(rtSum[:]) + " " + hex.EncodeToString(fwSum[:]) + "\n"
	key := sha256.Sum256([]byte(stamp))
	dir := filepath.Join(cacheDir, hex.EncodeToString(key[:])[:16])
	runner := filepath.Join(dir, "runtime", RunnerBinary)
	libDir := filepath.Join(dir, "runtime") + string(os.PathListSeparator) + filepath.Join(dir, "firmware")
	if b, err := os.ReadFile(filepath.Join(dir, runtimeCompleted)); err == nil && string(b) == stamp { //nolint:gosec // G304: path inside the jailoc cache directory
		if _, err := os.Stat(runner); err == nil {
			now := time.Now()
			_ = os.Chtimes(dir, now, now)
			return runner, libDir, nil
		}
	}

	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return "", "", fmt.Errorf("create runtime cache: %w", err)
	}
	tmp, err := os.MkdirTemp(cacheDir, ".unpack-")
	if err != nil {
		return "", "", fmt.Errorf("create runtime staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	_, _ = fmt.Fprintln(progress, "Unpacking VM runtime...")
	for _, part := range []struct {
		name string
		data []byte
	}{{"runtime", rt}, {"firmware", fw}} {
		if err := untar(bytes.NewReader(part.data), filepath.Join(tmp, part.name)); err != nil {
			return "", "", fmt.Errorf("VM runtime %s: %w", part.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "runtime", RunnerBinary)); err != nil {
		return "", "", fmt.Errorf("VM runtime: %s missing from the runtime tarball", RunnerBinary)
	}
	if err := os.WriteFile(filepath.Join(tmp, runtimeCompleted), []byte(stamp), 0o600); err != nil {
		return "", "", fmt.Errorf("write runtime stamp: %w", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		return "", "", fmt.Errorf("remove stale runtime: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		// A concurrent jailoc may have installed the same runtime first.
		if b, rerr := os.ReadFile(filepath.Join(dir, runtimeCompleted)); rerr == nil && string(b) == stamp { //nolint:gosec // G304: path inside the jailoc cache directory
			if _, rerr := os.Stat(runner); rerr == nil {
				return runner, libDir, nil
			}
		}
		return "", "", fmt.Errorf("install VM runtime: %w", err)
	}
	return runner, libDir, nil
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
