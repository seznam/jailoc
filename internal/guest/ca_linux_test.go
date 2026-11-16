package guest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// noCATool accepts any trust store tool run and ignores it.
func noCATool(t *testing.T) func([]string) error {
	t.Helper()
	return func([]string) error { return nil }
}

func sortedNames(names ...string) []string {
	slices.Sort(names)
	return names
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

	if err := installCABundle(root, bundle, noCATool(t)); err != nil {
		t.Fatalf("installCABundle: %v", err)
	}
	want := append(append(append([]byte{}, orig...), '\n'), readTestFile(t, bundle)...)
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, want) {
		t.Errorf("system bundle = %q, want %q", got, want)
	}
	local := filepath.Join(root, "/usr/local/share/ca-certificates/jailoc")
	if got, want := dirNames(t, local), sortedNames(caCertFileName(one), caCertFileName(two)); !slices.Equal(got, want) {
		t.Errorf("local certificates = %q", got)
	}
	if got := readTestFile(t, filepath.Join(local, caCertFileName(two))); !bytes.Equal(got, two) {
		t.Errorf("file of the second certificate = %q, want the certificate", got)
	}
	if _, err := os.Stat(filepath.Join(root, "/etc/pki/ca-trust/source/anchors")); !os.IsNotExist(err) {
		t.Errorf("anchors dir of another distribution was created: %v", err)
	}

	writeTestFile(t, bundle, one)
	if err := installCABundle(root, bundle, noCATool(t)); err != nil {
		t.Fatalf("installCABundle with one certificate: %v", err)
	}
	if got := dirNames(t, local); !slices.Equal(got, []string{caCertFileName(one)}) {
		t.Errorf("local certificates after shrinking = %q", got)
	}

	if err := installCABundle(root, "", noCATool(t)); err != nil {
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

	if err := installCABundle(root, bundle, noCATool(t)); err != nil {
		t.Fatalf("installCABundle: %v", err)
	}
	want := append(append(append([]byte{}, orig...), '\n'), cert...)
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, want) {
		t.Errorf("system bundle = %q, want the image's bundle plus the workspace bundle", got)
	}
	anchors := filepath.Join(root, "/etc/pki/ca-trust/source/anchors/jailoc")
	if got := dirNames(t, anchors); !slices.Equal(got, []string{caCertFileName(cert)}) {
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
			if err := installCABundle(root, path, noCATool(t)); err != nil {
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
		if err := installCABundle(root, p, noCATool(t)); err != nil {
			t.Fatalf("installCABundle %s: %v", p, err)
		}
	}
	want := []byte("-----ORIGINAL-----\n\n-----SECOND-----\n")
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, want) {
		t.Errorf("system bundle = %q, want %q", got, want)
	}
}

func TestInstallCABundleRefreshesGeneratedStores(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	orig := []byte("-----FEDORA-----\n")
	writeTestFile(t, filepath.Join(root, "/etc/pki/tls/certs/ca-bundle.crt"), orig)
	writeTestFile(t, filepath.Join(root, SystemCABundle), orig)
	if err := os.MkdirAll(filepath.Join(root, "/etc/pki/ca-trust/source/anchors"), 0o750); err != nil {
		t.Fatal(err)
	}
	runs := [][]string{}
	run := func(args []string) error {
		runs = append(runs, args)
		// The tool regenerates the stores, including SystemCABundle.
		writeTestFile(t, filepath.Join(root, SystemCABundle), []byte("-----REGENERATED-----\n"))
		return nil
	}
	bundle := filepath.Join(t.TempDir(), "ca-bundle.pem")
	old, rotated := testCertPEM(t, "old"), testCertPEM(t, "rotated")

	steps := []struct {
		name     string
		bundle   []byte
		wantRuns int
		want     []byte
	}{
		{name: "add", bundle: old, wantRuns: 0, want: append(append(append([]byte{}, orig...), '\n'), old...)},
		{name: "unchanged", bundle: old, wantRuns: 0, want: append(append(append([]byte{}, orig...), '\n'), old...)},
		{name: "rotate", bundle: rotated, wantRuns: 1, want: append(append(append([]byte{}, orig...), '\n'), rotated...)},
		{name: "remove", bundle: nil, wantRuns: 2, want: orig},
	}
	for _, step := range steps {
		path := ""
		if step.bundle != nil {
			writeTestFile(t, bundle, step.bundle)
			path = bundle
		}
		if err := installCABundle(root, path, run); err != nil {
			t.Fatalf("%s: installCABundle: %v", step.name, err)
		}
		if len(runs) != step.wantRuns {
			t.Errorf("%s: tool runs = %q, want %d", step.name, runs, step.wantRuns)
		}
		if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, step.want) {
			t.Errorf("%s: system bundle = %q, want %q", step.name, got, step.want)
		}
	}
	for _, args := range runs {
		if !slices.Equal(args, []string{"update-ca-trust", "extract"}) {
			t.Errorf("tool run %q, want update-ca-trust extract", args)
		}
	}
}

func TestInstallCABundleRefreshFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "/usr/local/share/ca-certificates"), 0o750); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "ca-bundle.pem")
	writeTestFile(t, bundle, testCertPEM(t, "corp"))
	if err := installCABundle(root, bundle, noCATool(t)); err != nil {
		t.Fatalf("installCABundle: %v", err)
	}
	fail := func([]string) error { return errors.New("boom") }
	err := installCABundle(root, "", fail)
	if err == nil || !strings.Contains(err.Error(), "/usr/local/share/ca-certificates: boom") {
		t.Fatalf("installCABundle error = %v, want the tool failure", err)
	}
}

func TestInstallCABundleRetriesFailedRefresh(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	orig := []byte("-----ORIGINAL-----\n")
	writeTestFile(t, filepath.Join(root, SystemCABundle), orig)
	if err := os.MkdirAll(filepath.Join(root, "/usr/local/share/ca-certificates"), 0o750); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "ca-bundle.pem")
	writeTestFile(t, bundle, testCertPEM(t, "corp"))
	if err := installCABundle(root, bundle, noCATool(t)); err != nil {
		t.Fatalf("installCABundle: %v", err)
	}

	runs := 0
	failing := true
	run := func(args []string) error {
		runs++
		if !slices.Equal(args, []string{"update-ca-certificates"}) {
			t.Errorf("tool run %q, want update-ca-certificates", args)
		}
		if failing {
			return errors.New("boom")
		}
		return nil
	}
	if err := installCABundle(root, "", run); err == nil {
		t.Fatal("installCABundle with a failing tool succeeded")
	}
	if got := readTestFile(t, filepath.Join(root, SystemCABundle)); !bytes.Equal(got, orig) {
		t.Errorf("system bundle after a failed refresh = %q, want the original", got)
	}
	if err := installCABundle(root, "", run); err == nil {
		t.Fatal("retry with a failing tool succeeded")
	}
	failing = false
	if err := installCABundle(root, "", run); err != nil {
		t.Fatalf("retry with a working tool: %v", err)
	}
	if runs != 3 {
		t.Errorf("tool runs = %d, want 3: one per call until it succeeds", runs)
	}
	if _, err := os.Stat(filepath.Join(root, caRefreshPending)); !os.IsNotExist(err) {
		t.Errorf("pending refresh kept after success: %v", err)
	}
	if err := installCABundle(root, "", run); err != nil {
		t.Fatalf("installCABundle after the refresh: %v", err)
	}
	if runs != 3 {
		t.Errorf("tool runs = %d after nothing changed, want 3", runs)
	}
}

func TestInstallCABundleRotationRenamesSourceFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	local := filepath.Join(root, "/usr/local/share/ca-certificates")
	if err := os.MkdirAll(local, 0o750); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(root, "/etc/ssl/certs")
	// update-ca-certificates links each source file into /etc/ssl/certs and
	// notices additions and removals by those links, not by content.
	run := func(args []string) error {
		if err := os.RemoveAll(links); err != nil {
			return err
		}
		if err := os.MkdirAll(links, 0o750); err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(local, caSourceSubdir))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, e := range entries {
			if err := os.Symlink(filepath.Join(local, caSourceSubdir, e.Name()), filepath.Join(links, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	bundle := filepath.Join(t.TempDir(), "ca-bundle.pem")
	old, rotated := testCertPEM(t, "old"), testCertPEM(t, "rotated")
	writeTestFile(t, bundle, old)
	if err := installCABundle(root, bundle, run); err != nil {
		t.Fatalf("installCABundle: %v", err)
	}
	// The session generates the stores from the source files.
	if err := run(nil); err != nil {
		t.Fatalf("generate stores: %v", err)
	}

	writeTestFile(t, bundle, rotated)
	if err := installCABundle(root, bundle, run); err != nil {
		t.Fatalf("installCABundle after rotation: %v", err)
	}
	if got := dirNames(t, filepath.Join(local, caSourceSubdir)); !slices.Equal(got, []string{caCertFileName(rotated)}) {
		t.Errorf("source files = %q, want only the rotated certificate", got)
	}
	names := dirNames(t, links)
	if slices.Contains(names, caCertFileName(old)) || !slices.Contains(names, caCertFileName(rotated)) {
		t.Errorf("generated links = %q, want the rotated certificate only", names)
	}
}

func TestFindCAToolIgnoresAgentPath(t *testing.T) {
	agentBin := t.TempDir()
	writeTestFile(t, filepath.Join(agentBin, "update-ca-certificates"), []byte("#!/bin/sh\n"))
	if err := os.Chmod(filepath.Join(agentBin, "update-ca-certificates"), 0o700); err != nil { //nolint:gosec // G302: test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", agentBin+":"+os.Getenv("PATH"))
	if got := findCATool("update-ca-certificates"); strings.HasPrefix(got, agentBin) {
		t.Errorf("findCATool = %q, want a system directory, not PATH", got)
	}
	for _, dir := range caToolPath {
		if strings.Contains(dir, "home") || strings.Contains(dir, "local") {
			t.Errorf("caToolPath has %q, which the agent may write", dir)
		}
	}
}
