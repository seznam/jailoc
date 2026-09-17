package cmd

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
)

func TestMaterializeCABundleSelectsAutomaticSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sslBundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "ssl.pem"), "ssl")
	nixBundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "nix.pem"), "nix")
	t.Setenv("SSL_CERT_FILE", sslBundle.path)
	t.Setenv("NIX_SSL_CERT_FILE", nixBundle.path)

	err := materializeCABundle(resolvedWorkspace(true, config.AutomaticCABundle()))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, sslBundle.data)
}

func TestMaterializeCABundleFallsBackFromEmptySSLSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	nixBundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "nix.pem"), "nix")
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("NIX_SSL_CERT_FILE", nixBundle.path)

	err := materializeCABundle(resolvedWorkspace(true, config.AutomaticCABundle()))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, nixBundle.data)
}

func TestMaterializeCABundleDoesNotFallBackFromInvalidSelectedSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	nixBundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "nix.pem"), "nix")
	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	t.Setenv("NIX_SSL_CERT_FILE", nixBundle.path)

	err := materializeCABundle(resolvedWorkspace(true, config.AutomaticCABundle()))

	if err == nil {
		t.Fatal("materializeCABundle() error = nil, want invalid SSL_CERT_FILE error")
	}
	if !strings.Contains(err.Error(), "SSL_CERT_FILE") {
		t.Fatalf("materializeCABundle() error = %q, want SSL_CERT_FILE context", err)
	}
	assertNoMaterializedBundle(t, home)
}

func TestMaterializeCABundleAcceptsCustomTildeAndSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundle := writeTestCABundle(t, filepath.Join(home, "company.pem"), "company")
	link := filepath.Join(home, "linked.pem")
	if err := os.Symlink(bundle.path, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle("~/linked.pem")))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, bundle.data)
}

func TestMaterializeCABundleRejectsInvalidCustomSources(t *testing.T) {
	certificate := newTestCertificate(t, "valid")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	tests := []struct {
		name    string
		path    func(t *testing.T) string
		wantErr string
	}{
		{name: "dollar", path: func(t *testing.T) string { return "/tmp/$CA.pem" }, wantErr: "must not contain"},
		{name: "relative", path: func(t *testing.T) string { return "relative.pem" }, wantErr: "absolute"},
		{name: "surrounding whitespace is literal", path: func(t *testing.T) string { return " /tmp/ca.pem " }, wantErr: "absolute"},
		{name: "directory", path: func(t *testing.T) string { return t.TempDir() }, wantErr: "regular file"},
		{name: "unreadable file", path: func(t *testing.T) string {
			path := writeTestCABundle(t, filepath.Join(t.TempDir(), "unreadable.pem"), "unreadable").path
			if err := os.Chmod(path, 0o000); err != nil {
				t.Fatalf("Chmod() error = %v", err)
			}
			return path
		}, wantErr: "not readable"},
		{name: "dangling symlink", path: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "dangling.pem")
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing.pem"), path); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
			return path
		}, wantErr: "evaluate symlinks"},
		{name: "non PEM", path: func(t *testing.T) string {
			return writeTestFile(t, filepath.Join(t.TempDir(), "bad.pem"), []byte("not PEM"))
		}, wantErr: "valid PEM"},
		{name: "malformed certificate", path: func(t *testing.T) string {
			data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid")})
			return writeTestFile(t, filepath.Join(t.TempDir(), "bad-cert.pem"), data)
		}, wantErr: "parse certificate"},
		{name: "private key only", path: func(t *testing.T) string {
			return writeTestFile(t, filepath.Join(t.TempDir(), "key.pem"), privateKey)
		}, wantErr: "private key"},
		{name: "certificate with private key", path: func(t *testing.T) string {
			return writeTestFile(t, filepath.Join(t.TempDir(), "mixed.pem"), append(bytes.Clone(certificate), privateKey...))
		}, wantErr: "private key"},
		{name: "lowercase private key label", path: func(t *testing.T) string {
			data := pem.EncodeToMemory(&pem.Block{Type: "private key", Bytes: x509.MarshalPKCS1PrivateKey(key)})
			return writeTestFile(t, filepath.Join(t.TempDir(), "lowercase-key.pem"), data)
		}, wantErr: "private key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle(tt.path(t))))

			if err == nil {
				t.Fatal("materializeCABundle() error = nil, want validation error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantErr)) {
				t.Fatalf("materializeCABundle() error = %q, want %q", err, tt.wantErr)
			}
			assertNoMaterializedBundle(t, home)
		})
	}
}

func TestMaterializeCABundleAcceptsMultipleCertificatesAndPreservesBytes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	data := append(newTestCertificate(t, "first"), newTestCertificate(t, "second")...)
	path := writeTestFile(t, filepath.Join(t.TempDir(), "bundle.pem"), data)

	err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle(path)))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, data)
	if source := readTestFile(t, path); !bytes.Equal(source, data) {
		t.Fatal("source bundle was modified")
	}
}

func TestMaterializeCABundleRemovesStaleFileWhenUnused(t *testing.T) {
	tests := []struct {
		name       string
		docker     bool
		policy     config.CABundle
		sslCertEnv string
	}{
		{name: "disabled policy", docker: true, policy: config.DisabledCABundle()},
		{name: "docker disabled skips invalid source", docker: false, policy: config.CustomCABundle("relative.pem")},
		{name: "docker disabled skips invalid automatic environment", docker: false, policy: config.AutomaticCABundle(), sslCertEnv: "relative.pem"},
		{name: "automatic without environment", docker: true, policy: config.AutomaticCABundle()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("SSL_CERT_FILE", tt.sslCertEnv)
			t.Setenv("NIX_SSL_CERT_FILE", "")
			path := materializedBundlePath(home)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			err := materializeCABundle(resolvedWorkspace(tt.docker, tt.policy))
			if err != nil {
				t.Fatalf("materializeCABundle() error = %v", err)
			}
			assertNoMaterializedBundle(t, home)
		})
	}
}

func TestMaterializeCABundleLeavesExistingFileUntouchedOnValidationFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := materializedBundlePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	want := []byte("existing")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := materializeCABundle(resolvedWorkspace(true, config.CustomCABundle(filepath.Join(home, "missing.pem"))))

	if err == nil {
		t.Fatal("materializeCABundle() error = nil, want validation error")
	}
	got := readTestFile(t, path)
	if !bytes.Equal(got, want) {
		t.Fatalf("materialized data = %q, want existing data %q", got, want)
	}
}

func TestCABundleMaterializationPrecedesGeneratedWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file       string
		laterCalls []string
	}{
		{file: "up.go", laterCalls: []string{"ensureOCConfigGitignore", "config.WriteAllowedFiles", "writeEntrypoint", "writeTUIConfig", "compose.WriteComposeFile"}},
		{file: "add.go", laterCalls: []string{"config.WriteAllowedFiles", "writeEntrypoint", "compose.WriteComposeFile", "writeTUIConfig", "ensureOCConfigGitignore"}},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			data := readTestFile(t, tt.file)
			materializeAt := bytes.Index(data, []byte("materializeCABundle("))
			if materializeAt < 0 {
				t.Fatalf("%s does not call materializeCABundle", tt.file)
			}
			for _, call := range tt.laterCalls {
				callAt := bytes.Index(data, []byte(call+"("))
				if callAt < 0 || callAt < materializeAt {
					t.Fatalf("%s must call materializeCABundle before %s", tt.file, call)
				}
			}
		})
	}
}
