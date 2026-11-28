package vm

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/guest"
)

// UpstreamStatusFile is where the network broker publishes the state of the
// workspace's upstream proxy.
func UpstreamStatusFile(stateDir string) string {
	return stateDir + string(os.PathSeparator) + "upstream.json"
}

// UpstreamCAFile is where the network broker publishes the public CA
// certificates of the workspace's upstream proxy. It never holds keys or
// credentials.
func UpstreamCAFile(stateDir string) string { return filepath.Join(stateDir, "upstream-ca.pem") }

// upstreamBundleName is the composed guest CA bundle in the read-only guest
// data directory.
const upstreamBundleName = "upstream-ca-bundle.pem"

// GuestUpstreamBundle is the guest path of the composed CA bundle: the
// workspace's host CA bundle plus the upstream proxy's public CA.
const GuestUpstreamBundle = guest.ReadOnlyHostDir + "/" + upstreamBundleName

// maxCABundle bounds the CA files jailoc composes.
const maxCABundle = 4 << 20

// ComposeGuestCABundle writes the guest CA bundle of a workspace VM: the
// certificates of the optional host bundle hostBase followed by the public
// CA the broker published in UpstreamCAFile. Missing or blank sources add
// nothing, so the bundle may be empty, which the guest treats as no extra
// CA. A source with private keys or data other than PEM is refused and the
// previous bundle is kept.
//
// The bundle is rewritten in place, like the other files of the read-only
// guest data directory: a renamed file is a new directory entry the guest's
// virtio-fs cache may not see yet. The guest reads it only while it boots or
// applies a reload, both of which jailoc starts after the write.
func ComposeGuestCABundle(stateDir, hostBase string) error {
	content, err := composeCABundle(hostBase, UpstreamCAFile(stateDir))
	if err != nil {
		return err
	}
	root, err := OpenGuestReadOnly(stateDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if old, err := root.ReadFile(upstreamBundleName); err == nil && bytes.Equal(old, content) {
		return nil
	}
	if err := root.WriteFile(upstreamBundleName, content, 0o600); err != nil {
		return fmt.Errorf("write guest CA bundle: %w", err)
	}
	return nil
}

// composeCABundle returns the certificates of the PEM files at paths, in
// order. Empty paths and missing files are skipped.
func composeCABundle(paths ...string) ([]byte, error) {
	var out []byte
	for _, p := range paths {
		if p == "" {
			continue
		}
		data, err := readCAFile(p)
		if err != nil {
			return nil, err
		}
		certs, err := pemCertificates(data)
		if err != nil {
			return nil, fmt.Errorf("CA bundle %s: %w", p, err)
		}
		out = append(out, certs...)
	}
	return out, nil
}

// readCAFile reads the regular file at p, or nothing when it does not exist.
func readCAFile(p string) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // G304: jailoc-owned CA bundle paths
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open CA bundle: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat CA bundle %s: %w", p, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CA bundle %s is not a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCABundle+1))
	if err != nil {
		return nil, fmt.Errorf("read CA bundle %s: %w", p, err)
	}
	if len(data) > maxCABundle {
		return nil, fmt.Errorf("CA bundle %s is larger than %d bytes", p, maxCABundle)
	}
	return data, nil
}

// pemCertificates re-encodes the certificates of the PEM data. Private keys
// and data that is not PEM are errors; other blocks such as CRLs are
// dropped, so the result holds certificates only. Errors never quote the
// data.
func pemCertificates(data []byte) ([]byte, error) {
	var out []byte
	rest := bytes.TrimSpace(data)
	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN ")) {
			return nil, errors.New("data that is not valid PEM")
		}
		block, next := pem.Decode(rest)
		if block == nil {
			return nil, errors.New("data that is not valid PEM")
		}
		rest = bytes.TrimSpace(next)
		switch {
		case strings.Contains(strings.ToUpper(block.Type), "PRIVATE KEY"):
			return nil, errors.New("contains a private key")
		case block.Type != "CERTIFICATE":
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})...)
	}
	return out, nil
}

// builderCABundle is the CA bundle the builder's Docker daemon trusts: the
// spec's bundle as it is, or, with the upstream proxy, its certificates plus
// the upstream proxy's public CA as published when the builder boots. The
// builder is not reloaded, so a later CA change applies on its next boot.
func builderCABundle(spec Spec) ([]byte, error) {
	if spec.UpstreamProxy {
		if spec.CABundle != "" {
			if _, err := os.Stat(spec.CABundle); err != nil {
				return nil, fmt.Errorf("read CA bundle: %w", err)
			}
		}
		content, err := composeCABundle(spec.CABundle, UpstreamCAFile(spec.StateDir))
		if err != nil || len(content) == 0 {
			return nil, err
		}
		return content, nil
	}
	if spec.CABundle == "" {
		return nil, nil
	}
	content, err := os.ReadFile(spec.CABundle)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle: %w", err)
	}
	return content, nil
}

// resetUpstreamFiles removes what the broker of a previous boot published,
// so the new broker's files are never mistaken for stale ones.
func resetUpstreamFiles(stateDir string) error {
	for _, p := range []string{UpstreamStatusFile(stateDir), UpstreamCAFile(stateDir)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale upstream proxy file: %w", err)
		}
	}
	return nil
}

// prepareUpstreamBoot composes the guest CA bundle of a workspace VM with
// the upstream proxy once its broker is ready and before the VM boots. The
// CA events the broker reported while starting are covered by it, so they
// are dropped instead of reloading a guest that is not running yet.
func prepareUpstreamBoot(spec Spec, events <-chan struct{}) error {
	if spec.Kind != KindWorkspace || !spec.UpstreamProxy {
		return nil
	}
	select {
	case <-events:
	default:
	}
	if err := ComposeGuestCABundle(spec.StateDir, spec.CABundle); err != nil {
		return fmt.Errorf("compose guest CA bundle: %w", err)
	}
	return nil
}

// watchUpstreamCA calls reload for every upstream CA event until ctx ends.
// A failed reload is logged; the next event retries.
func watchUpstreamCA(ctx context.Context, events <-chan struct{}, reload func(context.Context) error, logf func(string, ...any)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			if err := reload(ctx); err != nil && ctx.Err() == nil {
				logf("reload upstream proxy CA: %v", err)
			} else if err == nil {
				logf("upstream proxy CA reloaded")
			}
		}
	}
}

// brokerReadyTimeout bounds the broker's start. With the upstream proxy the
// broker first runs the credential helper (up to 10 seconds) and fetches the
// proxy's CA.
func brokerReadyTimeout(spec Spec) time.Duration {
	if spec.UpstreamProxy {
		return 30 * time.Second
	}
	return 10 * time.Second
}

// brokerStartupGrace is how long a broker stopped during startup gets to
// exit cleanly; with the upstream proxy it also stops its credential helper.
func brokerStartupGrace(spec Spec) time.Duration {
	if spec.UpstreamProxy {
		return 15 * time.Second
	}
	return 5 * time.Second
}
