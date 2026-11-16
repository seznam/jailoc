package guest

import (
	"bytes"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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

// caSource is a directory where a distribution tool that regenerates the
// system trust stores reads local certificates. Certificates kept there
// survive a regeneration in a session.
type caSource struct {
	dir string
	// tool regenerates the stores from dir and the distribution roots.
	tool []string
}

// caSources are the CA source directories of update-ca-certificates (Debian,
// Ubuntu, Alpine) and update-ca-trust (Fedora, RHEL).
var caSources = []caSource{
	{dir: "/usr/local/share/ca-certificates", tool: []string{"update-ca-certificates"}},
	{dir: "/etc/pki/ca-trust/source/anchors", tool: []string{"update-ca-trust", "extract"}},
}

// setCABundle makes the system CA bundle the image's original bundle plus
// the workspace bundle at path, or the original alone when path is empty or
// missing.
func setCABundle(path string) error {
	return installCABundle("/", path, runCATool)
}

// runCATool runs a distribution trust store tool from the image's PATH. An
// image without the tool has no generated stores to refresh.
func runCATool(args []string) error {
	bin, err := exec.LookPath(args[0])
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find %s: %w", args[0], err)
	}
	if out, err := exec.Command(bin, args[1:]...).CombinedOutput(); err != nil { //nolint:gosec // G204: fixed tool from the image PATH
		return fmt.Errorf("%s: %w: %s", args[0], err, bytes.TrimSpace(out))
	}
	return nil
}

// installCABundle is setCABundle for the file system at root. It also keeps
// one file per workspace certificate in each CA source directory of the
// image. When certificates there are removed or replaced, run regenerates
// the distribution's stores, which a session may have generated from them,
// so they stop trusting the old certificates. SystemCABundle is written
// last and keeps its content.
func installCABundle(root, path string, run func(args []string) error) error {
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
	stale, err := syncCASources(root, pemCertificates(extra))
	if err != nil {
		return err
	}
	for _, src := range stale {
		if err := run(src.tool); err != nil {
			return fmt.Errorf("refresh trust stores from %s: %w", src.dir, err)
		}
	}
	return writeSystemCABundle(root, content)
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
// lacks are skipped; without certs the jailoc directories are removed. It
// returns the sources whose earlier certificates were removed or replaced.
func syncCASources(root string, certs [][]byte) ([]caSource, error) {
	stale := []caSource{}
	for _, src := range caSources {
		base := filepath.Join(root, src.dir)
		if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("stat %s: %w", src.dir, err)
		}
		dir := filepath.Join(base, caSourceSubdir)
		changed, err := writeCASourceDir(dir, certs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(src.dir, caSourceSubdir), err)
		}
		if changed {
			stale = append(stale, src)
		}
	}
	return stale, nil
}

// writeCASourceDir makes dir hold certs as jailoc-NNN.crt, or removes it
// without certs. It reports whether a file dir held before was removed or
// replaced.
func writeCASourceDir(dir string, certs [][]byte) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("list: %w", err)
	}
	if len(certs) == 0 {
		if err := os.RemoveAll(dir); err != nil {
			return false, fmt.Errorf("remove: %w", err)
		}
		return len(entries) > 0, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: system directory
		return false, fmt.Errorf("create: %w", err)
	}
	changed := false
	names := make([]string, 0, len(certs))
	for i, cert := range certs {
		name := fmt.Sprintf("jailoc-%03d.crt", i)
		names = append(names, name)
		path := filepath.Join(dir, name)
		current, err := os.ReadFile(path) //nolint:gosec // G304: path under a fixed guest directory
		if err == nil && bytes.Equal(current, cert) {
			continue
		}
		changed = changed || err == nil
		if err := os.WriteFile(path, cert, 0o644); err != nil { //nolint:gosec // G306: public certificates
			return false, fmt.Errorf("write %s: %w", name, err)
		}
	}
	for _, e := range entries {
		if slices.Contains(names, e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return false, fmt.Errorf("remove %s: %w", e.Name(), err)
		}
		changed = true
	}
	return changed, nil
}
