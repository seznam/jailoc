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

	"github.com/moby/buildkit/session/secrets/secretsprovider"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
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

func TestMaterializeCABundleWhenDockerDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "company.pem"), "company")

	err := materializeCABundle(resolvedWorkspace(false, config.CustomCABundle(bundle.path)))
	if err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, bundle.data)
}

func TestMaterializeCABundleRejectsInvalidSourceWhenDockerDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := materializeCABundle(resolvedWorkspace(false, config.CustomCABundle("relative.pem")))
	if err == nil {
		t.Fatal("materializeCABundle() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("materializeCABundle() error = %q, want absolute-path context", err)
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
		{name: "automatic without environment", docker: true, policy: config.AutomaticCABundle()},
		{name: "automatic without environment and Docker disabled", docker: false, policy: config.AutomaticCABundle()},
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

func inspectingWorkspace(policy config.CABundle) *workspace.Resolved {
	ws := resolvedWorkspace(true, policy)
	ws.NetworkRules = []config.NetworkRule{{Name: "gh", Allow: []string{"api.github.com"}, HTTP: &config.HTTPMatch{}}}
	return ws
}

func TestMaterializeCABundleAppendsInspectionCA(t *testing.T) {
	tests := []struct {
		name string
		// source returns the CA bundle policy and the expected source bytes.
		source func(t *testing.T) (config.CABundle, []byte)
	}{
		{"custom bundle", func(t *testing.T) (config.CABundle, []byte) {
			b := writeTestCABundle(t, filepath.Join(t.TempDir(), "company.pem"), "company")
			return config.CustomCABundle(b.path), b.data
		}},
		{"custom bundle without trailing newline", func(t *testing.T) (config.CABundle, []byte) {
			data := bytes.TrimRight(newTestCertificate(t, "company"), "\n")
			return config.CustomCABundle(writeTestFile(t, filepath.Join(t.TempDir(), "company.pem"), data)), append(data, '\n')
		}},
		{"automatic bundle", func(t *testing.T) (config.CABundle, []byte) {
			b := writeTestCABundle(t, filepath.Join(t.TempDir(), "ssl.pem"), "ssl")
			t.Setenv("SSL_CERT_FILE", b.path)
			return config.AutomaticCABundle(), b.data
		}},
		{"automatic without source", func(*testing.T) (config.CABundle, []byte) { return config.AutomaticCABundle(), nil }},
		{"disabled", func(*testing.T) (config.CABundle, []byte) { return config.DisabledCABundle(), nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("SSL_CERT_FILE", "")
			t.Setenv("NIX_SSL_CERT_FILE", "")
			policy, source := tc.source(t)

			if err := materializeCABundle(inspectingWorkspace(policy)); err != nil {
				t.Fatalf("materializeCABundle() error = %v", err)
			}
			_, keyPath, caPEM, err := ensureInspectCA("test")
			if err != nil {
				t.Fatalf("ensureInspectCA() error = %v", err)
			}
			got := readTestFile(t, materializedBundlePath(home))
			if want := append(append([]byte{}, source...), caPEM...); !bytes.Equal(got, want) {
				t.Fatalf("bundle = %q, want source + inspection CA", got)
			}
			if err := validateCABundlePEM(got); err != nil {
				t.Fatalf("bundle does not validate: %v", err)
			}
			if bytes.Contains(got, readTestFile(t, keyPath)) || strings.Contains(string(got), "PRIVATE KEY") {
				t.Fatal("bundle contains the CA private key")
			}
			if workspaceCABundlePath := materializedBundlePath(home); filepath.Dir(workspaceCABundlePath) == filepath.Dir(keyPath) {
				t.Fatal("inspection CA key is stored next to the guest-synced bundle")
			}
		})
	}
}

func TestMaterializeCABundleInspectionCAStaysStable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("NIX_SSL_CERT_FILE", "")
	ws := inspectingWorkspace(config.DisabledCABundle())
	if err := materializeCABundle(ws); err != nil {
		t.Fatalf("first materializeCABundle() error = %v", err)
	}
	first := readTestFile(t, materializedBundlePath(home))
	if err := materializeCABundle(ws); err != nil {
		t.Fatalf("second materializeCABundle() error = %v", err)
	}
	if !bytes.Equal(first, readTestFile(t, materializedBundlePath(home))) {
		t.Fatal("bundle changed between runs")
	}

	// Dropping the http rules removes the CA from the bundle again.
	if err := materializeCABundle(resolvedWorkspace(true, config.DisabledCABundle())); err != nil {
		t.Fatalf("materializeCABundle() without http error = %v", err)
	}
	assertNoMaterializedBundle(t, home)
}

func TestMaterializeCABundleWithoutHTTPRulesCreatesNoCA(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "company.pem"), "company")
	ws := resolvedWorkspace(true, config.CustomCABundle(bundle.path))
	ws.NetworkRules = []config.NetworkRule{{Name: "plain", Allow: []string{"example.com"}}}
	if err := materializeCABundle(ws); err != nil {
		t.Fatalf("materializeCABundle() error = %v", err)
	}
	assertMaterializedBundle(t, home, bundle.data)
	if _, err := os.Stat(filepath.Join(home, ".cache", "jailoc-vm")); !os.IsNotExist(err) {
		t.Fatalf("VM state was created without http rules: %v", err)
	}
}

func TestMaterializeCABundleInspectionStillValidatesSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := materializeCABundle(inspectingWorkspace(config.CustomCABundle("relative.pem")))
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("materializeCABundle() error = %v, want source validation error", err)
	}
}

func TestCABundleMaterializationPrecedesGeneratedWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file       string
		laterCalls []string
	}{
		{file: "vmflow_unix.go", laterCalls: []string{"writeAgentFiles", "vmShares"}},
		{file: "vmreload_unix.go", laterCalls: []string{"writeAgentFiles", "vmReloadShares"}},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			data := readTestFile(t, tt.file)
			materializeAt := bytes.Index(data, []byte("prepareCABundle("))
			if materializeAt < 0 {
				t.Fatalf("%s does not call prepareCABundle", tt.file)
			}
			for _, call := range tt.laterCalls {
				callAt := bytes.Index(data, []byte(call+"("))
				if callAt < 0 || callAt < materializeAt {
					t.Fatalf("%s must call prepareCABundle before %s", tt.file, call)
				}
			}
		})
	}
}

func TestWorkspaceBuildCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "company.pem"), "company")
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("NIX_SSL_CERT_FILE", "")

	ca, err := workspaceBuildCA(resolvedWorkspace(true, config.CustomCABundle(bundle.path)))
	if err != nil {
		t.Fatalf("workspaceBuildCA() error = %v", err)
	}
	if ca == nil || !bytes.Equal(ca.PEM(), bundle.data) {
		t.Fatalf("workspaceBuildCA() = %v, want the bundle", ca)
	}
	if got, want := buildCAState(ca), "sha256:"+ca.Digest(); got != want {
		t.Errorf("buildCAState = %q, want %q", got, want)
	}

	// The workspace's inspection CA is not an external bundle.
	if ca, err := workspaceBuildCA(inspectingWorkspace(config.DisabledCABundle())); err != nil || ca != nil {
		t.Errorf("workspaceBuildCA(inspection only) = %v, %v; want nil", ca, err)
	}
	for name, policy := range map[string]config.CABundle{
		"disabled":                  config.DisabledCABundle(),
		"automatic without sources": config.AutomaticCABundle(),
	} {
		ca, err := workspaceBuildCA(resolvedWorkspace(true, policy))
		if err != nil || ca != nil {
			t.Errorf("%s: workspaceBuildCA() = %v, %v; want nil", name, ca, err)
		}
		if got := buildCAState(ca); got != "none" {
			t.Errorf("%s: buildCAState = %q, want none", name, got)
		}
	}
	if _, err := workspaceBuildCA(resolvedWorkspace(true, config.CustomCABundle("relative.pem"))); err == nil {
		t.Error("workspaceBuildCA() with an invalid bundle: error = nil, want error")
	}
}

func TestWorkspaceBuildCAAboveSecretLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("NIX_SSL_CERT_FILE", "")
	cert := newTestCertificate(t, "company")
	data := bytes.Repeat(cert, secretsprovider.MaxSecretSize/len(cert)+2)
	if len(data) <= secretsprovider.MaxSecretSize {
		t.Fatalf("test bundle is %d bytes, want more than %d", len(data), secretsprovider.MaxSecretSize)
	}
	path := writeTestFile(t, filepath.Join(t.TempDir(), "big.pem"), data)

	ca, err := workspaceBuildCA(resolvedWorkspace(true, config.CustomCABundle(path)))
	if err != nil {
		t.Fatalf("workspaceBuildCA() error = %v", err)
	}
	if ca == nil || buildCAState(ca) != "sha256:"+ca.Digest() {
		t.Fatalf("workspaceBuildCA() = %v, want a BuildCA with state", ca)
	}
	if err := ca.ValidateSecretSize(); err == nil || !strings.Contains(err.Error(), "500 KiB") {
		t.Errorf("ValidateSecretSize() = %v, want 500 KiB error", err)
	}
}

func TestPrepareCABundleSharesOneSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("NIX_SSL_CERT_FILE", "")
	bundle := writeTestCABundle(t, filepath.Join(t.TempDir(), "company.pem"), "company")

	buildCA, err := prepareCABundle(inspectingWorkspace(config.CustomCABundle(bundle.path)))
	if err != nil {
		t.Fatalf("prepareCABundle() error = %v", err)
	}
	if buildCA == nil || !bytes.Equal(buildCA.PEM(), bundle.data) {
		t.Fatalf("prepareCABundle() BuildCA = %v, want only the external bundle", buildCA)
	}
	_, _, caPEM, err := ensureInspectCA("test")
	if err != nil {
		t.Fatalf("ensureInspectCA() error = %v", err)
	}
	if bytes.Contains(buildCA.PEM(), caPEM) {
		t.Fatal("BuildCA contains the inspection CA")
	}
	guest := readTestFile(t, materializedBundlePath(home))
	if want := append(append([]byte{}, bundle.data...), caPEM...); !bytes.Equal(guest, want) {
		t.Fatalf("guest bundle = %q, want external bundle + inspection CA", guest)
	}

	// Without inspection the guest copy and BuildCA carry the same bytes.
	buildCA, err = prepareCABundle(resolvedWorkspace(true, config.CustomCABundle(bundle.path)))
	if err != nil {
		t.Fatalf("prepareCABundle() without inspection error = %v", err)
	}
	if guest := readTestFile(t, materializedBundlePath(home)); !bytes.Equal(guest, buildCA.PEM()) {
		t.Fatalf("guest bundle = %q, want %q", guest, buildCA.PEM())
	}

	// Inspection only: a guest file, but no BuildCA.
	buildCA, err = prepareCABundle(inspectingWorkspace(config.DisabledCABundle()))
	if err != nil || buildCA != nil {
		t.Fatalf("prepareCABundle(inspection only) = %v, %v; want nil", buildCA, err)
	}
	if guest := readTestFile(t, materializedBundlePath(home)); !bytes.Equal(guest, caPEM) {
		t.Fatalf("guest bundle = %q, want inspection CA", guest)
	}
}
