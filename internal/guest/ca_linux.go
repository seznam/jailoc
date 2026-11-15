package guest

import (
	"bytes"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

const (
	// originalCA keeps the image's system CA bundle so a changed or removed
	// workspace bundle can be applied on top of the original.
	originalCA = "/run/jailoc-system-ca.crt"
	// caSourceSubdir is the directory of the workspace certificates in each
	// CA source directory of the image.
	caSourceSubdir = "jailoc"
)

// imageCABundles are the CA bundles an image may ship, in the order Go's
// crypto/x509 looks for them on Linux. The first one found is the original
// bundle, so SystemCABundle holds the image's roots also on distributions
// that keep their bundle elsewhere.
var imageCABundles = []string{
	SystemCABundle,
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// caSourceDirs are where the distribution tools that regenerate the system
// bundle read local certificates: update-ca-certificates (Debian, Ubuntu,
// Alpine) and update-ca-trust (Fedora, RHEL). Certificates kept there survive
// a regeneration in a session.
var caSourceDirs = []string{
	"/usr/local/share/ca-certificates",
	"/etc/pki/ca-trust/source/anchors",
}

// setCABundle makes the system CA bundle the image's original bundle plus
// the workspace bundle at path, or the original alone when path is empty or
// missing.
func setCABundle(path string) error {
	return installCABundle("/", path)
}

// installCABundle is setCABundle for the file system at root. It also keeps
// one file per workspace certificate in each CA source directory of the
// image.
func installCABundle(root, path string) error {
	orig, err := originalCABundle(root)
	if err != nil {
		return err
	}
	extra, err := readWorkspaceCABundle(path)
	if err != nil {
		return err
	}
	content := orig
	if len(extra) > 0 {
		content = append(append(append([]byte{}, orig...), '\n'), extra...)
	}
	if err := writeSystemCABundle(root, content); err != nil {
		return err
	}
	return syncCASources(root, pemCertificates(extra))
}

// originalCABundle returns the image's CA bundle, saving it on the first
// call of the boot. An image without a bundle has an empty original.
func originalCABundle(root string) ([]byte, error) {
	saved := filepath.Join(root, originalCA)
	orig, err := os.ReadFile(saved) //nolint:gosec // G304: fixed guest path
	if err == nil {
		return orig, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", originalCA, err)
	}
	orig = []byte{}
	for _, p := range imageCABundles {
		b, err := os.ReadFile(filepath.Join(root, p)) //nolint:gosec // G304: fixed guest paths
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		orig = b
		break
	}
	if err := os.MkdirAll(filepath.Dir(saved), 0o755); err != nil { //nolint:gosec // G301: system directory
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(originalCA), err)
	}
	if err := os.WriteFile(saved, orig, 0o644); err != nil { //nolint:gosec // G306: public certificates
		return nil, fmt.Errorf("save original CA bundle: %w", err)
	}
	return orig, nil
}

// readWorkspaceCABundle returns the workspace bundle at path, or nil when
// path is empty, missing or blank.
func readWorkspaceCABundle(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	extra, err := os.ReadFile(path) //nolint:gosec // G304: path from the host-written guest config
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read CA bundle %s: %w", path, err)
	case len(bytes.TrimSpace(extra)) == 0:
		return nil, nil
	}
	return extra, nil
}

// writeSystemCABundle replaces SystemCABundle under root with content. It
// does not create an empty bundle in an image that has none.
func writeSystemCABundle(root string, content []byte) error {
	target := filepath.Join(root, SystemCABundle)
	current, err := os.ReadFile(target) //nolint:gosec // G304: fixed guest path
	switch {
	case err == nil && bytes.Equal(current, content):
		return nil
	case errors.Is(err, fs.ErrNotExist) && len(content) == 0:
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301: system directory
		return fmt.Errorf("create %s: %w", filepath.Dir(SystemCABundle), err)
	}
	tmp := target + ".jailoc"
	if err := os.WriteFile(tmp, content, 0o644); err != nil { //nolint:gosec // G306: public certificates
		return fmt.Errorf("write CA bundle: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("install CA bundle: %w", err)
	}
	return nil
}

// pemCertificates returns each CERTIFICATE block of bundle as its own PEM.
func pemCertificates(bundle []byte) [][]byte {
	certs := [][]byte{}
	for {
		var block *pem.Block
		block, bundle = pem.Decode(bundle)
		if block == nil {
			return certs
		}
		if block.Type == "CERTIFICATE" {
			certs = append(certs, pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: block.Bytes}))
		}
	}
}

// syncCASources makes the jailoc directory of each CA source directory
// under root hold exactly certs, one per file. Source directories the image
// lacks are skipped; without certs the jailoc directories are removed.
func syncCASources(root string, certs [][]byte) error {
	for _, d := range caSourceDirs {
		base := filepath.Join(root, d)
		if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("stat %s: %w", d, err)
		}
		dir := filepath.Join(base, caSourceSubdir)
		if len(certs) == 0 {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("remove %s: %w", filepath.Join(d, caSourceSubdir), err)
			}
			continue
		}
		if err := writeCASourceDir(dir, certs); err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(d, caSourceSubdir), err)
		}
	}
	return nil
}

// writeCASourceDir writes certs to dir as jailoc-NNN.crt and removes the
// files of an earlier, longer bundle.
func writeCASourceDir(dir string, certs [][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: system directory
		return fmt.Errorf("create: %w", err)
	}
	names := make([]string, 0, len(certs))
	for i, cert := range certs {
		name := fmt.Sprintf("jailoc-%03d.crt", i)
		names = append(names, name)
		path := filepath.Join(dir, name)
		if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, cert) { //nolint:gosec // G304: path under a fixed guest directory
			continue
		}
		if err := os.WriteFile(path, cert, 0o644); err != nil { //nolint:gosec // G306: public certificates
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	for _, e := range entries {
		if slices.Contains(names, e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("remove %s: %w", e.Name(), err)
		}
	}
	return nil
}
