package cmd

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

type testCABundle struct {
	path string
	data []byte
}

func writeTestCABundle(t *testing.T, path, commonName string) testCABundle {
	t.Helper()
	data := newTestCertificate(t, commonName)
	return testCABundle{path: writeTestFile(t, path, data), data: data}
}

func newTestCertificate(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Unix(3600, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeTestFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func resolvedWorkspace(enableDocker bool, policy config.CABundle) *workspace.Resolved {
	return &workspace.Resolved{Name: "test", EnableDocker: enableDocker, CABundle: policy}
}

func materializedBundlePath(home string) string {
	return filepath.Join(home, ".config", "jailoc", "workspaces", "test", "ca-bundle.pem")
}

func assertMaterializedBundle(t *testing.T, home string, want []byte) {
	t.Helper()
	path := materializedBundlePath(home)
	got := readTestFile(t, path)
	if !bytes.Equal(got, want) {
		t.Fatalf("materialized data differs from source")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized mode = %04o, want 0600", info.Mode().Perm())
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ca-bundle.pem-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary bundle files remain: %v", temporaryFiles)
	}
}

func assertNoMaterializedBundle(t *testing.T, home string) {
	t.Helper()
	_, err := os.Stat(materializedBundlePath(home))
	if !os.IsNotExist(err) {
		t.Fatalf("Stat(materialized bundle) error = %v, want not exist", err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatalf("OpenRoot(%q) error = %v", filepath.Dir(path), err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		t.Fatalf("Open(%q) error = %v", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll(%q) error = %v", path, err)
	}
	return data
}
