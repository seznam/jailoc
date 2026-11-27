package cmd

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

const caBundleFilename = "ca-bundle.pem"

type caBundleSource struct {
	path   string
	origin string
}

func materializeCABundle(ws *workspace.Resolved) error {
	_, err := prepareCABundle(ws)
	return err
}

// prepareCABundle reads the external CA bundle of ws once and uses that single
// snapshot for both the guest file and the returned image-build CA, so a bundle
// rotating mid-startup cannot leave them disagreeing. The workspace's HTTP
// inspection CA is appended to the guest file only; the returned BuildCA is nil
// when no external bundle is selected.
func prepareCABundle(ws *workspace.Resolved) (*docker.BuildCA, error) {
	destination := filepath.Join(config.ConfigDir(), "workspaces", ws.Name, caBundleFilename)
	content, selected, err := sourceCABundle(ws)
	if err != nil {
		return nil, err
	}
	var buildCA *docker.BuildCA
	if selected {
		buildCA, err = docker.NewBuildCA(content)
		if err != nil {
			return nil, fmt.Errorf("prepare CA bundle for image builds: %w", err)
		}
	}
	if ws.InspectsHTTP() {
		// The broker signs inspected connections with the workspace CA, so
		// the guest must trust it, also when no other bundle is forwarded.
		_, _, caPEM, err := ensureInspectCA(ws.Name)
		if err != nil {
			return nil, fmt.Errorf("prepare inspection CA: %w", err)
		}
		content = appendCABundle(content, caPEM)
		selected = true
	}
	if !selected {
		return buildCA, removeCABundle(destination)
	}
	if err := writeCABundleAtomically(destination, content); err != nil {
		return nil, fmt.Errorf("materialize CA bundle: %w", err)
	}
	return buildCA, nil
}

// sourceCABundle reads and validates the host CA bundle the workspace forwards
// to the guest. It reports false when no bundle is forwarded.
func sourceCABundle(ws *workspace.Resolved) ([]byte, bool, error) {
	if !ws.CABundle.Enabled() {
		return nil, false, nil
	}
	source, selected, err := selectCABundleSource(ws.CABundle)
	if err != nil {
		return nil, false, err
	}
	if !selected {
		return nil, false, nil
	}
	content, err := readCABundle(source)
	if err != nil {
		if ws.CABundle.Automatic() {
			return nil, false, fmt.Errorf("%w; set ca_bundle = false to disable automatic forwarding, or set ca_bundle to a valid bundle path", err)
		}
		return nil, false, err
	}
	if err := validateCABundlePEM(content); err != nil {
		if ws.CABundle.Automatic() {
			return nil, false, fmt.Errorf("validate CA bundle from %s %q: %w; set ca_bundle = false to disable automatic forwarding, or set ca_bundle to a valid bundle path", source.origin, source.path, err)
		}
		return nil, false, fmt.Errorf("validate CA bundle from %s %q: %w", source.origin, source.path, err)
	}
	return content, true, nil
}

// workspaceBuildCA snapshots the external CA bundle of ws for image builds. It
// is nil when no bundle is selected: the workspace's HTTP inspection CA never
// belongs here, jailoc's own builds do not go through the broker.
func workspaceBuildCA(ws *workspace.Resolved) (*docker.BuildCA, error) {
	content, selected, err := sourceCABundle(ws)
	if err != nil || !selected {
		return nil, err
	}
	ca, err := docker.NewBuildCA(content)
	if err != nil {
		return nil, fmt.Errorf("prepare CA bundle for image builds: %w", err)
	}
	return ca, nil
}

// buildCAState is the value of vm.Spec.BuildCA for ca: "none" without a
// bundle, else the bundle's digest.
func buildCAState(ca *docker.BuildCA) string {
	if ca == nil {
		return "none"
	}
	return "sha256:" + ca.Digest()
}

// appendCABundle appends extra PEM certificates to bundle, separated by a
// newline when bundle does not end in one.
func appendCABundle(bundle, extra []byte) []byte {
	if len(bundle) == 0 {
		return slices.Clone(extra)
	}
	out := slices.Clone(bundle)
	if out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, extra...)
}

func selectCABundleSource(policy config.CABundle) (caBundleSource, bool, error) {
	if path, custom := policy.Path(); custom {
		return newCABundleSource(path, "ca_bundle")
	}
	if !policy.Automatic() {
		return caBundleSource{}, false, nil
	}
	if path := strings.TrimSpace(os.Getenv("SSL_CERT_FILE")); path != "" {
		return newCABundleSource(path, "SSL_CERT_FILE")
	}
	if path := strings.TrimSpace(os.Getenv("NIX_SSL_CERT_FILE")); path != "" {
		return newCABundleSource(path, "NIX_SSL_CERT_FILE")
	}
	return caBundleSource{}, false, nil
}

func newCABundleSource(path, origin string) (caBundleSource, bool, error) {
	expanded, err := expandCABundlePath(path)
	if err != nil {
		return caBundleSource{}, false, fmt.Errorf("expand CA bundle path from %s: %w", origin, err)
	}
	return caBundleSource{path: expanded, origin: origin}, true, nil
}

func expandCABundlePath(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return "", fmt.Errorf("path %q uses unsupported home-directory syntax", path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, path[1:]), nil
}

func readCABundle(source caBundleSource) ([]byte, error) {
	if strings.Contains(source.path, "$") {
		return nil, fmt.Errorf("CA bundle path from %s %q must not contain \"$\"", source.origin, source.path)
	}
	if !filepath.IsAbs(source.path) {
		return nil, fmt.Errorf("CA bundle path from %s %q must be absolute", source.origin, source.path)
	}
	resolved, err := filepath.EvalSymlinks(source.path)
	if err != nil {
		return nil, fmt.Errorf("evaluate symlinks for CA bundle from %s %q: %w", source.origin, source.path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat CA bundle from %s %q: %w", source.origin, resolved, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CA bundle from %s %q is not a regular file", source.origin, resolved)
	}
	if info.Mode().Perm()&0o444 == 0 {
		return nil, fmt.Errorf("CA bundle from %s %q is not readable", source.origin, resolved)
	}
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return nil, fmt.Errorf("open CA bundle directory from %s %q: %w", source.origin, filepath.Dir(resolved), err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.Base(resolved))
	if err != nil {
		return nil, fmt.Errorf("open CA bundle from %s %q: %w", source.origin, resolved, err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle from %s %q: %w", source.origin, resolved, err)
	}
	return content, nil
}

func validateCABundlePEM(content []byte) error {
	rest := content
	certificates := 0
	for len(bytes.TrimSpace(rest)) > 0 {
		trimmed := bytes.TrimSpace(rest)
		block, next, err := decodeFirstPEMBlock(trimmed)
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToUpper(block.Type), "PRIVATE KEY") {
			return fmt.Errorf("CA bundle contains private-key PEM block %q", block.Type)
		}
		if block.Type == "CERTIFICATE" {
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return fmt.Errorf("parse CERTIFICATE block: %w", err)
			}
			certificates++
		}
		rest = next
	}
	if certificates == 0 {
		return fmt.Errorf("CA bundle must contain at least one CERTIFICATE PEM block")
	}
	return nil
}

func decodeFirstPEMBlock(content []byte) (*pem.Block, []byte, error) {
	headerEnd := bytes.IndexByte(content, '\n')
	if headerEnd < 0 {
		return nil, nil, fmt.Errorf("CA bundle contains data that is not valid PEM")
	}
	header := strings.TrimSpace(string(content[:headerEnd]))
	if !strings.HasPrefix(header, "-----BEGIN ") || !strings.HasSuffix(header, "-----") {
		return nil, nil, fmt.Errorf("CA bundle contains data that is not valid PEM")
	}
	blockType := strings.TrimSuffix(strings.TrimPrefix(header, "-----BEGIN "), "-----")
	if blockType == "" {
		return nil, nil, fmt.Errorf("CA bundle contains data that is not valid PEM")
	}
	footer := []byte("-----END " + blockType + "-----")
	footerOffset := bytes.Index(content[headerEnd+1:], footer)
	if footerOffset < 0 {
		return nil, nil, fmt.Errorf("CA bundle contains data that is not valid PEM")
	}
	blockEnd := headerEnd + 1 + footerOffset + len(footer)
	block, leftover := pem.Decode(content[:blockEnd])
	if block == nil || len(bytes.TrimSpace(leftover)) != 0 {
		return nil, nil, fmt.Errorf("CA bundle contains data that is not valid PEM")
	}
	return block, content[blockEnd:], nil
}

func writeCABundleAtomically(destination string, content []byte) error {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create workspace config directory %q: %w", dir, err)
	}
	temporary, err := os.CreateTemp(dir, ".ca-bundle.pem-*")
	if err != nil {
		return fmt.Errorf("create temporary bundle: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary bundle permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary bundle: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary bundle: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary bundle: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("replace bundle %q: %w", destination, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open workspace config root %q for sync: %w", dir, err)
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open workspace config directory %q for sync: %w", dir, err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync workspace config directory %q: %w", dir, err)
	}
	return nil
}

func removeCABundle(destination string) error {
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale CA bundle %q: %w", destination, err)
	}
	return nil
}
