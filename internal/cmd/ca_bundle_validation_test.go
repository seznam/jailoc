package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
)

func TestMaterializeCABundleTreatsWhitespaceAutomaticSourceAsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	nixBundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "nix.pem"), "nix")
	t.Setenv("SSL_CERT_FILE", "  ")
	t.Setenv("NIX_SSL_CERT_FILE", nixBundle.path)

	err := materializeCABundle(resolvedWorkspace(true, config.AutomaticCABundle()))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, nixBundle.data)
}

func TestMaterializeCABundleInvalidAutomaticSourceExplainsRecovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	t.Setenv("NIX_SSL_CERT_FILE", "")

	err := materializeCABundle(resolvedWorkspace(true, config.AutomaticCABundle()))
	if err == nil || !strings.Contains(err.Error(), "SSL_CERT_FILE") || !strings.Contains(err.Error(), "ca_bundle = false") {
		t.Fatalf("materializeCABundle error = %v, want source and recovery hint", err)
	}
}

func TestMaterializeCABundleRejectsUnsupportedTildeUserPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle("~other/ca.pem")))

	if err == nil {
		t.Fatal("materializeCABundle() error = nil, want unsupported tilde-user path error")
	}
	if !strings.Contains(err.Error(), "~other/ca.pem") {
		t.Fatalf("materializeCABundle() error = %q, want original path context", err)
	}
	assertNoMaterializedBundle(t, home)
}

func TestMaterializeCABundleAcceptsCustomPathContainingSpaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "company bundle.pem"), "company")

	err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle(bundle.path)))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, bundle.data)
}

func TestMaterializeCABundleRejectsMalformedPEMBeforeCertificate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	malformed := []byte("-----BEGIN CERTIFICATE-----\nnot-base64\n-----END CERTIFICATE-----\n")
	data := append([]byte(nil), malformed...)
	data = append(data, newTestCertificate(t, "valid")...)
	path := writeTestFile(t, filepath.Join(t.TempDir(), "malformed.pem"), data)

	err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle(path)))

	if err == nil {
		t.Fatal("materializeCABundle() error = nil, want malformed PEM error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "valid pem") {
		t.Fatalf("materializeCABundle() error = %q, want PEM context", err)
	}
	assertNoMaterializedBundle(t, home)
}

func TestMaterializeCABundleAtomicallyReplacesDestinationAtMode0600(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "replacement.pem"), "replacement")
	destination := materializedBundlePath(home)
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle(bundle.path)))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, bundle.data)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".ca-bundle.pem-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary bundle files = %v, want none", matches)
	}
}
