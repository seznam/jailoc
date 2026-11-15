package guest

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
	"slices"
	"testing"
	"time"
)

func testCertPEM(t *testing.T, name string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestInstallCABundleDebianLayout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	orig := []byte("-----ORIGINAL-----\n")
	writeTestFile(t, filepath.Join(root, SystemCABundle), orig)
	if err := os.MkdirAll(filepath.Join(root, "/usr/local/share/ca-certificates"), 0o750); err != nil {
		t.Fatal(err)
	}
	one, two := testCertPEM(t, "one"), testCertPEM(t, "two")
	bundle := filepath.Join(t.TempDir(), "ca-bundle.pem")
	writeTestFile(t, bundle, append(append([]byte{}, one...), two...))

	if err := installCABundle(root, bundle); err != nil {
		t.Fatalf("installCABundle: %v", err)
	}
	want := append(append(append([]byte{}, orig...), '\n'), readTestFile(t, bundle)...)
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, want) {
		t.Errorf("system bundle = %q, want %q", got, want)
	}
	local := filepath.Join(root, "/usr/local/share/ca-certificates/jailoc")
	if got := dirNames(t, local); !slices.Equal(got, []string{"jailoc-000.crt", "jailoc-001.crt"}) {
		t.Errorf("local certificates = %q", got)
	}
	if got := readTestFile(t, filepath.Join(local, "jailoc-001.crt")); !bytes.Equal(got, two) {
		t.Errorf("jailoc-001.crt = %q, want the second certificate", got)
	}
	if _, err := os.Stat(filepath.Join(root, "/etc/pki/ca-trust/source/anchors")); !os.IsNotExist(err) {
		t.Errorf("anchors dir of another distribution was created: %v", err)
	}

	writeTestFile(t, bundle, one)
	if err := installCABundle(root, bundle); err != nil {
		t.Fatalf("installCABundle with one certificate: %v", err)
	}
	if got := dirNames(t, local); !slices.Equal(got, []string{"jailoc-000.crt"}) {
		t.Errorf("local certificates after shrinking = %q", got)
	}

	if err := installCABundle(root, ""); err != nil {
		t.Fatalf("installCABundle without a bundle: %v", err)
	}
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, orig) {
		t.Errorf("system bundle after removal = %q, want the original", got)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Errorf("local certificates kept after removal: %v", err)
	}
}

func TestInstallCABundleFedoraLayout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	orig := []byte("-----FEDORA-----\n")
	writeTestFile(t, filepath.Join(root, "/etc/pki/tls/certs/ca-bundle.crt"), orig)
	if err := os.MkdirAll(filepath.Join(root, "/etc/pki/ca-trust/source/anchors"), 0o750); err != nil {
		t.Fatal(err)
	}
	cert := testCertPEM(t, "corp")
	bundle := filepath.Join(t.TempDir(), "ca-bundle.pem")
	writeTestFile(t, bundle, cert)

	if err := installCABundle(root, bundle); err != nil {
		t.Fatalf("installCABundle: %v", err)
	}
	want := append(append(append([]byte{}, orig...), '\n'), cert...)
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, want) {
		t.Errorf("system bundle = %q, want the image's bundle plus the workspace bundle", got)
	}
	anchors := filepath.Join(root, "/etc/pki/ca-trust/source/anchors/jailoc")
	if got := dirNames(t, anchors); !slices.Equal(got, []string{"jailoc-000.crt"}) {
		t.Errorf("anchors = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "/usr/local/share/ca-certificates")); !os.IsNotExist(err) {
		t.Errorf("Debian source dir was created: %v", err)
	}
}

func TestInstallCABundleWithoutImageBundle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		bundle []byte
		want   []byte
	}{
		{name: "no workspace bundle", bundle: nil, want: nil},
		{name: "blank workspace bundle", bundle: []byte("\n \n"), want: nil},
		{name: "workspace bundle", bundle: []byte("-----CORP-----\n"), want: []byte("\n-----CORP-----\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := ""
			if tt.bundle != nil {
				path = filepath.Join(t.TempDir(), "ca-bundle.pem")
				writeTestFile(t, path, tt.bundle)
			}
			if err := installCABundle(root, path); err != nil {
				t.Fatalf("installCABundle: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(root, SystemCABundle)) //nolint:gosec // G304: test temp dir
			if tt.want == nil {
				if !os.IsNotExist(err) {
					t.Errorf("system bundle created: %q, %v", got, err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, tt.want) {
				t.Errorf("system bundle = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestInstallCABundleKeepsOriginalAcrossReloads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	orig := []byte("-----ORIGINAL-----\n")
	writeTestFile(t, filepath.Join(root, SystemCABundle), orig)
	first := filepath.Join(t.TempDir(), "first.pem")
	writeTestFile(t, first, []byte("-----FIRST-----\n"))
	second := filepath.Join(t.TempDir(), "second.pem")
	writeTestFile(t, second, []byte("-----SECOND-----\n"))

	for _, p := range []string{first, second} {
		if err := installCABundle(root, p); err != nil {
			t.Fatalf("installCABundle %s: %v", p, err)
		}
	}
	want := []byte("-----ORIGINAL-----\n\n-----SECOND-----\n")
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, want) {
		t.Errorf("system bundle = %q, want %q", got, want)
	}
}
