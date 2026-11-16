package cmd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	inspectCACertFilename = "inspect-ca.pem"
	inspectCAKeyFilename  = "inspect-ca.key"

	inspectCAValidity = 10 * 365 * 24 * time.Hour
	// inspectCARenewBefore regenerates a CA that is about to expire, so a
	// guest never trusts a CA the broker is about to stop signing under.
	inspectCARenewBefore = 30 * 24 * time.Hour
)

// ensureInspectCA returns the workspace's HTTP inspection CA, creating it on
// first use. The CA lives only in the VM state directory on the host: the
// broker signs leaf certificates with the private key, and only the
// certificate is trusted in the guest (see materializeCABundle). It is rotated
// by deleting the state directory (jailoc down --purge).
func ensureInspectCA(wsName string) (certPath, keyPath string, certPEM []byte, err error) {
	dir := vmStateDir(wsName)
	certPath = filepath.Join(dir, inspectCACertFilename)
	keyPath = filepath.Join(dir, inspectCAKeyFilename)

	if certPEM, ok := loadInspectCA(certPath, keyPath); ok {
		return certPath, keyPath, certPEM, nil
	}

	certPEM, keyPEM, err := generateInspectCA(wsName)
	if err != nil {
		return "", "", nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", nil, fmt.Errorf("create inspection CA directory %q: %w", dir, err)
	}
	// The key first: a loader needs both files and a matching pair, so a
	// partial write is regenerated rather than served.
	if err := writeFileAtomically(keyPath, keyPEM, 0o600); err != nil {
		return "", "", nil, fmt.Errorf("write inspection CA key: %w", err)
	}
	if err := writeFileAtomically(certPath, certPEM, 0o644); err != nil { //nolint:gosec // G302: the CA certificate is public and trusted in the guest
		return "", "", nil, fmt.Errorf("write inspection CA certificate: %w", err)
	}
	return certPath, keyPath, certPEM, nil
}

// loadInspectCA reads an existing CA pair. It reports false when either file
// is missing or unusable (not a CA, mismatched key, about to expire), in which
// case the caller generates a new one.
func loadInspectCA(certPath, keyPath string) ([]byte, bool) {
	certPEM, err := os.ReadFile(certPath) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, false
	}
	keyPEM, err := os.ReadFile(keyPath) //nolint:gosec // G304: jailoc-owned state path
	if err != nil {
		return nil, false
	}
	certBlock, rest := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, false
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || !cert.IsCA || time.Now().Add(inspectCARenewBefore).After(cert.NotAfter) {
		return nil, false
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		return nil, false
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, false
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, false
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, false
	}
	return certPEM, true
}

func generateInspectCA(wsName string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate inspection CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate inspection CA serial: %w", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, nil, fmt.Errorf("generate inspection CA name: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("encode inspection CA public key: %w", err)
	}
	keyDigest := sha256.Sum256(pubDER)
	keyID := keyDigest[:20]

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("jailoc inspection CA %s %s", wsName, hex.EncodeToString(suffix)),
			Organization: []string{"jailoc"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(inspectCAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          keyID,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create inspection CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encode inspection CA key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// writeFileAtomically writes content to a temporary file beside path and
// renames it into place with the given mode.
func writeFileAtomically(path string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
