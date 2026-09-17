package cmd

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

const dindCABundleFilename = "dind-ca-bundle.pem"

type dindCABundleSource struct {
	path   string
	origin string
}

func materializeDindCABundle(ws *workspace.Resolved) error {
	destination := filepath.Join(config.ConfigDir(), "workspaces", ws.Name, dindCABundleFilename)
	if !ws.EnableDocker || !ws.DindCABundle.Enabled() {
		return removeDindCABundle(destination)
	}

	source, selected, err := selectDindCABundleSource(ws.DindCABundle)
	if err != nil {
		return err
	}
	if !selected {
		return removeDindCABundle(destination)
	}

	content, err := readDindCABundle(source)
	if err != nil {
		return err
	}
	if err := validateDindCABundlePEM(content); err != nil {
		return fmt.Errorf("validate DinD CA bundle from %s %q: %w", source.origin, source.path, err)
	}
	if err := writeDindCABundleAtomically(destination, content); err != nil {
		return fmt.Errorf("materialize DinD CA bundle: %w", err)
	}
	return nil
}

func selectDindCABundleSource(policy config.DindCABundle) (dindCABundleSource, bool, error) {
	if path, custom := policy.Path(); custom {
		return newDindCABundleSource(path, "dind_ca_bundle")
	}
	if !policy.Automatic() {
		return dindCABundleSource{}, false, nil
	}
	if path := strings.TrimSpace(os.Getenv("SSL_CERT_FILE")); path != "" {
		return newDindCABundleSource(path, "SSL_CERT_FILE")
	}
	if path := strings.TrimSpace(os.Getenv("NIX_SSL_CERT_FILE")); path != "" {
		return newDindCABundleSource(path, "NIX_SSL_CERT_FILE")
	}
	return dindCABundleSource{}, false, nil
}

func newDindCABundleSource(path, origin string) (dindCABundleSource, bool, error) {
	expanded, err := expandDindCABundlePath(path)
	if err != nil {
		return dindCABundleSource{}, false, fmt.Errorf("expand DinD CA bundle path from %s: %w", origin, err)
	}
	return dindCABundleSource{path: expanded, origin: origin}, true, nil
}

func expandDindCABundlePath(path string) (string, error) {
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

func readDindCABundle(source dindCABundleSource) ([]byte, error) {
	if strings.Contains(source.path, "$") {
		return nil, fmt.Errorf("DinD CA bundle path from %s %q must not contain \"$\"", source.origin, source.path)
	}
	if !filepath.IsAbs(source.path) {
		return nil, fmt.Errorf("DinD CA bundle path from %s %q must be absolute", source.origin, source.path)
	}
	resolved, err := filepath.EvalSymlinks(source.path)
	if err != nil {
		return nil, fmt.Errorf("evaluate symlinks for DinD CA bundle from %s %q: %w", source.origin, source.path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat DinD CA bundle from %s %q: %w", source.origin, resolved, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("DinD CA bundle from %s %q is not a regular file", source.origin, resolved)
	}
	if info.Mode().Perm()&0o444 == 0 {
		return nil, fmt.Errorf("DinD CA bundle from %s %q is not readable", source.origin, resolved)
	}
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return nil, fmt.Errorf("open DinD CA bundle directory from %s %q: %w", source.origin, filepath.Dir(resolved), err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.Base(resolved))
	if err != nil {
		return nil, fmt.Errorf("open DinD CA bundle from %s %q: %w", source.origin, resolved, err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read DinD CA bundle from %s %q: %w", source.origin, resolved, err)
	}
	return content, nil
}

func validateDindCABundlePEM(content []byte) error {
	rest := content
	certificates := 0
	for len(bytes.TrimSpace(rest)) > 0 {
		trimmed := bytes.TrimSpace(rest)
		block, next, err := decodeFirstPEMBlock(trimmed)
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToUpper(block.Type), "PRIVATE KEY") {
			return fmt.Errorf("bundle contains private-key PEM block %q", block.Type)
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
		return fmt.Errorf("bundle must contain at least one CERTIFICATE PEM block")
	}
	return nil
}

func decodeFirstPEMBlock(content []byte) (*pem.Block, []byte, error) {
	headerEnd := bytes.IndexByte(content, '\n')
	if headerEnd < 0 {
		return nil, nil, fmt.Errorf("bundle contains data that is not valid PEM")
	}
	header := strings.TrimSpace(string(content[:headerEnd]))
	if !strings.HasPrefix(header, "-----BEGIN ") || !strings.HasSuffix(header, "-----") {
		return nil, nil, fmt.Errorf("bundle contains data that is not valid PEM")
	}
	blockType := strings.TrimSuffix(strings.TrimPrefix(header, "-----BEGIN "), "-----")
	if blockType == "" {
		return nil, nil, fmt.Errorf("bundle contains data that is not valid PEM")
	}
	footer := []byte("-----END " + blockType + "-----")
	footerOffset := bytes.Index(content[headerEnd+1:], footer)
	if footerOffset < 0 {
		return nil, nil, fmt.Errorf("bundle contains data that is not valid PEM")
	}
	blockEnd := headerEnd + 1 + footerOffset + len(footer)
	block, leftover := pem.Decode(content[:blockEnd])
	if block == nil || len(bytes.TrimSpace(leftover)) != 0 {
		return nil, nil, fmt.Errorf("bundle contains data that is not valid PEM")
	}
	return block, content[blockEnd:], nil
}

func writeDindCABundleAtomically(destination string, content []byte) error {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create workspace config directory %q: %w", dir, err)
	}
	temporary, err := os.CreateTemp(dir, ".dind-ca-bundle.pem-*")
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

func removeDindCABundle(destination string) error {
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale DinD CA bundle %q: %w", destination, err)
	}
	return nil
}
