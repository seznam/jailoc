//go:build darwin || linux

package proxyhost

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortTempDir returns a short directory so AF_UNIX paths stay under the
// sun_path limit on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base) > 60 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "jlt-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove temporary directory: %v", err)
		}
	})
	return dir
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temp root %s not cleaned up, left %d entries", dir, len(entries))
	}
}

func TestProbeTLS(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ca, err := newAuthority("test CA", now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := newAuthority("other CA", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.issueLeaf(proofHost, now)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newProofServer(ln, leaf)
	t.Cleanup(func() {
		if err := srv.stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	port := ln.Addr().(*net.TCPAddr).Port

	isHostname := func(err error) bool { var e x509.HostnameError; return errors.As(err, &e) }
	isUnknown := func(err error) bool { var e x509.UnknownAuthorityError; return errors.As(err, &e) }
	tests := []struct {
		name    string
		sni     string
		roots   *x509.CertPool
		reject  func(error) bool
		wantErr bool
	}{
		{name: "exact name and trusted CA", sni: proofHost, roots: ca.pool()},
		{name: "wrong hostname", sni: wrongHost, roots: ca.pool(), reject: isHostname},
		{name: "untrusted CA", sni: proofHost, roots: other.pool(), reject: isUnknown},
		{name: "reject expected but handshake succeeds", sni: proofHost, roots: ca.pool(), reject: isHostname, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tt.reject == nil {
				err = httpsGet(t.Context(), port, tt.roots)
			} else {
				err = expectReject(t.Context(), port, tt.sni, tt.roots, tt.reject)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLeafProperties(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ca, err := newAuthority("test CA", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.issueLeaf(proofHost, now)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != proofHost {
		t.Errorf("DNSNames = %v, want exactly %s", cert.DNSNames, proofHost)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ExtKeyUsage = %v, want serverAuth only", cert.ExtKeyUsage)
	}
	if cert.NotAfter.Sub(now) > time.Hour {
		t.Errorf("leaf lifetime too long: %v", cert.NotAfter.Sub(now))
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.pool(), DNSName: proofHost, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("verify against signing CA: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial []byte
		data    []byte
	}{
		{name: "create", data: []byte("a")},
		{name: "replace", initial: []byte("old content"), data: []byte("new")},
		{name: "empty", initial: []byte("old"), data: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "hosts")
			if tt.initial != nil {
				if err := os.WriteFile(path, tt.initial, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeFileAtomic(path, tt.data); err != nil {
				t.Fatalf("writeFileAtomic: %v", err)
			}
			got, err := os.ReadFile(path) //nolint:gosec // test fixture path.
			if err != nil || !bytes.Equal(got, tt.data) {
				t.Errorf("content = %q (%v), want %q", got, err, tt.data)
			}
			fi, err := os.Stat(path)
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v (%v), want 0600", fi.Mode().Perm(), err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("directory has %d entries, want only the target", len(entries))
			}
		})
	}
	t.Run("stale temp blocks write and is not clobbered", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "hosts")
		if err := os.WriteFile(path+".tmp", []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(path, []byte("y")); err == nil {
			t.Fatal("want error for pre-existing temp file")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("target must not exist, stat err = %v", err)
		}
	})
}

func TestCheckPrivate(t *testing.T) {
	t.Parallel()
	uid := os.Geteuid()
	tests := []struct {
		name              string
		dirMode, fileMode os.FileMode
		uid               int
		wantErr           bool
	}{
		{name: "private", dirMode: 0o700, fileMode: 0o600, uid: uid},
		{name: "dir too open", dirMode: 0o755, fileMode: 0o600, uid: uid, wantErr: true},
		{name: "file too open", dirMode: 0o700, fileMode: 0o644, uid: uid, wantErr: true},
		{name: "other owner", dirMode: 0o700, fileMode: 0o600, uid: uid + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := filepath.Join(dir, "f")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, tt.fileMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tt.dirMode); err != nil {
				t.Fatal(err)
			}
			err := checkPrivate(dir, file, 0o700, 0o600, tt.uid)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTamperBlock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		from    int
		want    string
		wantErr bool
	}{
		{name: "first record", content: "keep\n# BEGIN\n127.0.0.1 a\n# END\n", from: 4, want: "keep\n# BEGIN\n127.0.0.1 a tampered.invalid\n# END\n"},
		{name: "leading separator skipped", content: "keep\n# BEGIN\n127.0.0.1 a\n", from: 4, want: "keep\n# BEGIN\n127.0.0.1 a tampered.invalid\n"},
		{name: "comments only", content: "keep\n# BEGIN\n# END\n", from: 4, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tamperBlock([]byte(tt.content), tt.from)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostsProof(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := hostsProof(path); err != nil {
		t.Fatalf("hostsProof: %v", err)
	}
}

// RunProof tests change TMPDIR, so they cannot run in parallel.
func TestRunProof(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("proof refuses to run as root; see TestRunProofRefusesRoot")
	}
	tmp := shortTempDir(t)
	t.Setenv("TMPDIR", tmp)

	res, err := RunProof(t.Context())
	if err != nil {
		t.Fatalf("RunProof: %v\nchecks: %+v", err, res.Checks)
	}
	want := []string{
		"unprivileged", "memory-ca", "private-socket", "broker-listener",
		"gateway-a-tls", "wrong-hostname-rejected", "untrusted-ca-rejected",
		"second-gateway-refused", "gateway-b-replacement-tls", "hosts-plan", "cleanup",
	}
	if len(res.Checks) != len(want) {
		t.Fatalf("got %d checks, want %d: %+v", len(res.Checks), len(want), res.Checks)
	}
	for i, c := range res.Checks {
		if c.Name != want[i] || !c.Passed {
			t.Errorf("check %d = %+v, want passing %q", i, c, want[i])
		}
		if strings.HasPrefix(c.Name, "gateway-") && !strings.Contains(c.Detail, "same process") {
			t.Errorf("check %q must disclose that gateways run in the same process: %q", c.Name, c.Detail)
		}
		for _, obsolete := range []string{"handoff", "retainer", "retained listener"} {
			if strings.Contains(c.Detail, obsolete) {
				t.Errorf("check %q still describes the removed %q design", c.Name, obsolete)
			}
		}
		for _, bad := range []string{"PRIVATE KEY", "BEGIN CERTIFICATE", "token"} {
			if strings.Contains(c.Detail, bad) {
				t.Errorf("check %q detail leaks %q", c.Name, bad)
			}
		}
	}
	assertEmptyDir(t, tmp)
}

func TestRunProofContext(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("proof refuses to run as root")
	}
	tests := []struct {
		name string
		ctx  func(*testing.T) context.Context
		want error
	}{
		{name: "canceled before start", want: context.Canceled, ctx: func(t *testing.T) context.Context {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return ctx
		}},
		{name: "deadline already passed", want: context.DeadlineExceeded, ctx: func(t *testing.T) context.Context {
			ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := shortTempDir(t)
			t.Setenv("TMPDIR", tmp)
			res, err := RunProof(tt.ctx(t))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var failed, cleanedUp bool
			for _, c := range res.Checks {
				failed = failed || !c.Passed && c.Name != "cleanup"
				cleanedUp = cleanedUp || c.Name == "cleanup" && c.Passed
			}
			if !failed || !cleanedUp {
				t.Errorf("want a failed row and a passing cleanup row, got %+v", res.Checks)
			}
			assertEmptyDir(t, tmp)
		})
	}
}

func TestRunProofCanceledMidway(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("proof refuses to run as root")
	}
	tmp := shortTempDir(t)
	t.Setenv("TMPDIR", tmp)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := RunProof(ctx); done <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want nil or context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunProof did not return after cancellation")
	}
	assertEmptyDir(t, tmp)
}

func TestRunProofRefusesRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires euid 0")
	}
	res, err := RunProof(t.Context())
	if err == nil || len(res.Checks) != 1 || res.Checks[0].Name != "unprivileged" || res.Checks[0].Passed || !strings.Contains(err.Error(), "uid 0") {
		t.Fatalf("want one failed check and an error, got %+v, %v", res.Checks, err)
	}
}
