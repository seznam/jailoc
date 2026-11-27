package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	goversion "github.com/hashicorp/go-version"
)

const (
	// maxBinarySize bounds the extracted jailoc binary.
	maxBinarySize int64 = 256 << 20

	// maxExpandedArchiveSize bounds every byte the tar stream expands to,
	// including skipped entries and headers, so a decompression bomb cannot
	// hide in entries that are never written.
	maxExpandedArchiveSize = maxBinarySize + 8<<20

	// maxArchiveHeaders bounds the number of tar entries inspected.
	maxArchiveHeaders = 64

	binaryName    = "jailoc"
	tempPattern   = ".jailoc-update-*"
	installedMode = fs.FileMode(0o755)
)

// ErrInstalledSyncFailed marks a replaceExecutable failure that happened after
// the rename: the new executable is already installed, only the parent
// directory sync failed, so the install is complete but not yet durable.
var ErrInstalledSyncFailed = errors.New("executable installed but directory sync failed")

// resolveExecutable returns the absolute, symlink-free path of the running
// executable and verifies it is a regular file.
func resolveExecutable(executable func() (string, error)) (string, error) {
	path, err := executable()
	if err != nil {
		return "", fmt.Errorf("locate current executable: %w", err)
	}

	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make executable path %q absolute: %w", path, err)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks of executable %q: %w", path, err)
	}

	info, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat executable %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable %q is not a regular file", resolved)
	}

	return resolved, nil
}

// checkInstallGuards refuses to self-update installs that another tool owns or
// builds whose version cannot be compared against a release.
func checkInstallGuards(path, current string, force, explicitVersion bool) error {
	clean := filepath.Clean(path)

	if clean == "/nix/store" || strings.HasPrefix(clean, "/nix/store/") {
		return fmt.Errorf("refusing to replace %q: the Nix store is read-only; update jailoc through Nix instead", path)
	}

	if !force && isHomebrewPath(clean) {
		return fmt.Errorf("%q is managed by Homebrew; run `brew upgrade --cask jailoc` instead (or use --force to replace it anyway)", path)
	}

	if !force && !explicitVersion && !isReleaseVersion(current) {
		return fmt.Errorf("current version %q is not a release build; specify a version to install or use --force", current)
	}

	return nil
}

func isHomebrewPath(path string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts {
		if part == "Caskroom" {
			return true
		}
		if part == "Cellar" && i+1 < len(parts) && parts[i+1] == binaryName {
			return true
		}
	}

	return false
}

func isReleaseVersion(version string) bool {
	if version == "dev" || version == "(devel)" {
		return false
	}

	_, err := goversion.NewVersion(version)

	return err == nil
}

// errExpandedArchiveTooLarge is reported when the tar stream expands beyond
// maxExpandedArchiveSize.
var errExpandedArchiveTooLarge = fmt.Errorf("archive expands to more than %d bytes", maxExpandedArchiveSize)

// expansionLimit fails reads once more than n bytes were produced.
type expansionLimit struct {
	r io.Reader
	n int64
}

func (l *expansionLimit) Read(p []byte) (int, error) {
	if l.n <= 0 {
		var probe [1]byte
		if _, err := io.ReadFull(l.r, probe[:]); err != nil {
			return 0, err
		}

		return 0, errExpandedArchiveTooLarge
	}

	if int64(len(p)) > l.n {
		p = p[:l.n]
	}

	n, err := l.r.Read(p)
	l.n -= int64(n)

	return n, err
}

// extractBinary extracts the single regular file named "jailoc" from a
// gzip-compressed tar archive into a new temporary file in destDir and returns
// its path. The caller must already have verified the archive digest. The whole
// archive is read, so duplicates, truncation and gzip corruption are detected.
// All other entries are skipped without being written. On error no temporary
// file is left behind; on success the caller owns the returned file.
func extractBinary(archive *os.File, destDir string) (path string, err error) {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind archive: %w", err)
	}

	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "", fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()

	limit := &expansionLimit{r: gz, n: maxExpandedArchiveSize}
	tr := tar.NewReader(limit)

	var tmp *os.File
	defer func() {
		if err != nil && tmp != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	for headers := 0; ; headers++ {
		if headers >= maxArchiveHeaders {
			return "", fmt.Errorf("archive has more than %d entries", maxArchiveHeaders)
		}

		hdr, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", fmt.Errorf("read archive: %w", nextErr)
		}

		if hdr.Name != binaryName {
			continue
		}
		if tmp != nil {
			return "", fmt.Errorf("archive contains more than one %q entry", binaryName)
		}
		if hdr.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("archive entry %q is not a regular file", binaryName)
		}
		if hdr.Size <= 0 || hdr.Size > maxBinarySize {
			return "", fmt.Errorf("archive entry %q size %d is outside 1..%d bytes (exceeds limit)", binaryName, hdr.Size, maxBinarySize)
		}

		tmp, err = createTemp(destDir)
		if err != nil {
			return "", err
		}

		// The tar reader already stops at hdr.Size; the explicit limit keeps the copy bounded regardless.
		written, copyErr := io.Copy(tmp, io.LimitReader(tr, hdr.Size))
		if copyErr != nil {
			return "", fmt.Errorf("extract %q: %w", binaryName, copyErr)
		}
		if written != hdr.Size {
			return "", fmt.Errorf("extract %q: wrote %d of %d bytes", binaryName, written, hdr.Size)
		}
	}

	// Drain the rest so the gzip trailer (CRC, length) is verified and any
	// trailing garbage or oversized padding is rejected.
	if _, err := io.Copy(io.Discard, limit); err != nil {
		return "", fmt.Errorf("read archive trailer: %w", err)
	}

	if tmp == nil {
		return "", fmt.Errorf("archive does not contain %q", binaryName)
	}

	if err := tmp.Chmod(installedMode); err != nil {
		return "", fmt.Errorf("chmod temporary file %q: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary file %q: %w", tmp.Name(), err)
	}

	name := tmp.Name()
	closing := tmp
	tmp = nil // the file is committed; close errors below clean up explicitly
	if err := closing.Close(); err != nil {
		_ = os.Remove(name)

		return "", fmt.Errorf("close temporary file %q: %w", name, err)
	}

	return name, nil
}

func createTemp(destDir string) (*os.File, error) {
	tmp, err := os.CreateTemp(destDir, tempPattern)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("create temporary file in %q: %w (check write permission on the directory containing the executable, or reinstall with the tool that installed it)", destDir, err)
		}

		return nil, fmt.Errorf("create temporary file in %q: %w", destDir, err)
	}

	return tmp, nil
}

// replaceExecutable atomically replaces target with the extracted binary tmp.
// target must be a resolved regular file (see resolveExecutable) and tmp a
// regular file in the same directory (see extractBinary).
//
// Cleanup contract: if tmp fails validation it is left untouched; once it is
// validated, replaceExecutable removes it on every failure before the rename.
// After a successful rename tmp no longer exists. If only the parent directory
// sync fails, the returned error wraps ErrInstalledSyncFailed and the new
// executable is installed. Callers may still unconditionally remove tmp and
// ignore fs.ErrNotExist.
func replaceExecutable(ctx context.Context, target, tmp string) error {
	return replaceExecutableWith(ctx, target, tmp, syncDir)
}

func replaceExecutableWith(ctx context.Context, target, tmp string, syncParent func(dir string) error) error {
	targetInfo, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("stat target %q: %w", target, err)
	}
	if !targetInfo.Mode().IsRegular() {
		return fmt.Errorf("target %q is not a regular file (resolve symlinks first)", target)
	}

	tmpInfo, err := os.Lstat(tmp)
	if err != nil {
		return fmt.Errorf("stat temporary file %q: %w", tmp, err)
	}
	if !tmpInfo.Mode().IsRegular() {
		return fmt.Errorf("temporary file %q is not a regular file", tmp)
	}
	if filepath.Clean(tmp) == filepath.Clean(target) || os.SameFile(tmpInfo, targetInfo) {
		return fmt.Errorf("temporary file %q is the install target", tmp)
	}

	targetDir := filepath.Dir(target)
	if err := requireSameDir(targetDir, filepath.Dir(tmp)); err != nil {
		return fmt.Errorf("temporary file %q is not next to target %q: %w", tmp, target, err)
	}

	// tmp is validated: from here every failure removes it.
	if err := prepareReplacement(ctx, tmp); err != nil {
		_ = os.Remove(tmp)

		return err
	}

	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("replace %q: %w", target, err)
	}

	if err := syncParent(targetDir); err != nil {
		return fmt.Errorf("%w: sync %q: %w", ErrInstalledSyncFailed, targetDir, err)
	}

	return nil
}

func prepareReplacement(ctx context.Context, tmp string) error {
	// Chmod explicitly so setuid/setgid/sticky bits can never be installed.
	if err := os.Chmod(tmp, installedMode); err != nil {
		return fmt.Errorf("chmod %q: %w", tmp, err)
	}
	if err := removeQuarantine(tmp); err != nil {
		return err
	}

	return ctx.Err()
}

func requireSameDir(a, b string) error {
	aInfo, err := os.Stat(a)
	if err != nil {
		return fmt.Errorf("stat %q: %w", a, err)
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return fmt.Errorf("stat %q: %w", b, err)
	}
	if !os.SameFile(aInfo, bInfo) {
		return errors.New("different directories")
	}

	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the parent of the resolved executable
	if err != nil {
		return err
	}

	syncErr := d.Sync()
	closeErr := d.Close()

	return errors.Join(syncErr, closeErr)
}
