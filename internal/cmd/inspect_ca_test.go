package cmd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parseTestCACert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("certificate PEM = %q", certPEM)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	return cert
}

func TestEnsureInspectCAGeneratesContractCA(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	certPath, keyPath, certPEM, err := ensureInspectCA("api")
	if err != nil {
		t.Fatalf("ensureInspectCA() error = %v", err)
	}
	stateDir := filepath.Join(home, ".cache", "jailoc-vm", "workspaces", "api")
	if certPath != filepath.Join(stateDir, "inspect-ca.pem") || keyPath != filepath.Join(stateDir, "inspect-ca.key") {
		t.Fatalf("paths = %q, %q", certPath, keyPath)
	}
	for _, c := range []struct {
		path string
		mode os.FileMode
	}{{certPath, 0o644}, {keyPath, 0o600}, {stateDir, 0o700}} {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", c.path, err)
		}
		if info.Mode().Perm() != c.mode {
			t.Fatalf("mode of %q = %04o, want %04o", c.path, info.Mode().Perm(), c.mode)
		}
	}
	if got := readTestFile(t, certPath); !bytes.Equal(got, certPEM) {
		t.Fatal("returned certificate differs from the file")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(stateDir, ".inspect-ca*")); len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}

	cert := parseTestCACert(t, certPEM)
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.MaxPathLen != 0 || !cert.MaxPathLenZero {
		t.Fatalf("basic constraints: IsCA=%v MaxPathLen=%d zero=%v", cert.IsCA, cert.MaxPathLen, cert.MaxPathLenZero)
	}
	if cert.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Fatalf("KeyUsage = %v", cert.KeyUsage)
	}
	if len(cert.SubjectKeyId) == 0 {
		t.Fatal("SubjectKeyId is empty")
	}
	if cert.SerialNumber.BitLen() < 64 {
		t.Fatalf("serial %v is too small", cert.SerialNumber)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("public key = %T, want ECDSA P-256", cert.PublicKey)
	}
	assertContains(t, cert.Subject.CommonName, "jailoc inspection CA api ")
	if len(strings.TrimPrefix(cert.Subject.CommonName, "jailoc inspection CA api ")) != 8 {
		t.Fatalf("CommonName = %q, want 8 hex suffix", cert.Subject.CommonName)
	}
	if len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != "jailoc" {
		t.Fatalf("Organization = %v", cert.Subject.Organization)
	}
	now := time.Now()
	if !cert.NotBefore.Before(now) || now.Sub(cert.NotBefore) > 2*time.Hour {
		t.Fatalf("NotBefore = %v", cert.NotBefore)
	}
	if got := cert.NotAfter.Sub(now); got < 9*365*24*time.Hour || got > 11*365*24*time.Hour {
		t.Fatalf("NotAfter-now = %v, want about 10y", got)
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Fatalf("not self-signed: %v", err)
	}

	keyBlock, _ := pem.Decode(readTestFile(t, keyPath))
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatalf("key PEM block = %+v", keyBlock)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey() error = %v", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		t.Fatal("key does not match the certificate")
	}
}

func TestEnsureInspectCAReusesExistingPair(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	certPath, keyPath, first, err := ensureInspectCA("api")
	if err != nil {
		t.Fatalf("first ensureInspectCA() error = %v", err)
	}
	firstKey := readTestFile(t, keyPath)
	_, _, second, err := ensureInspectCA("api")
	if err != nil {
		t.Fatalf("second ensureInspectCA() error = %v", err)
	}
	if !bytes.Equal(first, second) || !bytes.Equal(firstKey, readTestFile(t, keyPath)) {
		t.Fatal("existing CA was regenerated")
	}
	if !bytes.Equal(readTestFile(t, certPath), first) {
		t.Fatal("certificate file changed")
	}

	_, _, other, err := ensureInspectCA("other")
	if err != nil {
		t.Fatalf("other ensureInspectCA() error = %v", err)
	}
	if bytes.Equal(first, other) {
		t.Fatal("workspaces share a CA")
	}
}

func TestEnsureInspectCARegeneratesUnusablePair(t *testing.T) {
	tests := []struct {
		name   string
		damage func(t *testing.T, certPath, keyPath string)
	}{
		{"missing key", func(t *testing.T, _, keyPath string) {
			t.Helper()
			if err := os.Remove(keyPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing certificate", func(t *testing.T, certPath, _ string) {
			t.Helper()
			if err := os.Remove(certPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"garbage certificate", func(t *testing.T, certPath, _ string) {
			t.Helper()
			writeTestFile(t, certPath, []byte("not pem"))
		}},
		{"garbage key", func(t *testing.T, _, keyPath string) {
			t.Helper()
			writeTestFile(t, keyPath, []byte("not pem"))
		}},
		{"mismatched key", func(t *testing.T, _, keyPath string) {
			t.Helper()
			_, otherPEM, err := generateInspectCA("other")
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, keyPath, otherPEM)
		}},
		{"not a CA", func(t *testing.T, certPath, _ string) {
			t.Helper()
			writeTestFile(t, certPath, newTestCertificateNotCA(t))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			certPath, keyPath, before, err := ensureInspectCA("api")
			if err != nil {
				t.Fatalf("ensureInspectCA() error = %v", err)
			}
			tc.damage(t, certPath, keyPath)

			_, _, after, err := ensureInspectCA("api")
			if err != nil {
				t.Fatalf("ensureInspectCA() after damage error = %v", err)
			}
			if bytes.Equal(before, after) {
				t.Fatal("unusable CA was reused")
			}
			if got := readTestFile(t, certPath); !bytes.Equal(got, after) {
				t.Fatal("regenerated certificate was not written")
			}
			if _, ok := loadInspectCA(certPath, keyPath); !ok {
				t.Fatal("regenerated pair does not load")
			}
		})
	}
}

// newTestCertificateNotCA is a well-formed self-signed leaf certificate that
// the CA load check must reject.
func newTestCertificateNotCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
