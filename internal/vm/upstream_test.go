package vm

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/guest"
)

// testCertPEM returns a self-signed CA certificate named cn as PEM.
func testCertPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// bundleNames returns the common names of the certificates in bundle.
func bundleNames(t *testing.T, bundle []byte) []string {
	t.Helper()
	var names []string
	for rest := bundle; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			t.Fatalf("bundle has a %q block", b.Type)
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, c.Subject.CommonName)
	}
	return names
}

func TestUpstreamPaths(t *testing.T) {
	t.Parallel()
	if got, want := UpstreamStatusFile("/state"), filepath.Join("/state", "upstream.json"); got != want {
		t.Errorf("UpstreamStatusFile = %q, want %q", got, want)
	}
	if got, want := UpstreamCAFile("/state"), filepath.Join("/state", "upstream-ca.pem"); got != want {
		t.Errorf("UpstreamCAFile = %q, want %q", got, want)
	}
	if want := guest.ReadOnlyHostDir + "/upstream-ca-bundle.pem"; GuestUpstreamBundle != want {
		t.Errorf("GuestUpstreamBundle = %q, want %q", GuestUpstreamBundle, want)
	}
}

func TestComposeGuestCABundle(t *testing.T) {
	t.Parallel()
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("secret")})
	crl := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: []byte("crl")})
	tests := []struct {
		name      string
		base      func(t *testing.T) []byte // nil: no base file
		baseUnset bool                      // empty hostBase path
		ca        func(t *testing.T) []byte // nil: no upstream CA file
		want      []string
		wantErr   string
	}{
		{name: "base and upstream CA", base: func(t *testing.T) []byte { return testCertPEM(t, "corp") }, ca: func(t *testing.T) []byte { return testCertPEM(t, "upstream") }, want: []string{"corp", "upstream"}},
		{name: "upstream CA only", baseUnset: true, ca: func(t *testing.T) []byte { return testCertPEM(t, "upstream") }, want: []string{"upstream"}},
		{name: "missing base file", ca: func(t *testing.T) []byte { return testCertPEM(t, "upstream") }, want: []string{"upstream"}},
		{name: "base only", base: func(t *testing.T) []byte { return testCertPEM(t, "corp") }, want: []string{"corp"}},
		{name: "nothing", baseUnset: true},
		{name: "blank files", base: func(*testing.T) []byte { return []byte("\n \n") }, ca: func(*testing.T) []byte { return nil }},
		{name: "non-certificate blocks are dropped", base: func(t *testing.T) []byte { return append(testCertPEM(t, "corp"), crl...) }, want: []string{"corp"}},
		{name: "private key in upstream CA", ca: func(t *testing.T) []byte { return append(testCertPEM(t, "upstream"), key...) }, wantErr: "private key"},
		{name: "private key in base", base: func(*testing.T) []byte { return key }, wantErr: "private key"},
		{name: "garbage in upstream CA", ca: func(*testing.T) []byte { return []byte("not pem") }, wantErr: "not valid PEM"},
		{name: "trailing garbage", ca: func(t *testing.T) []byte { return append(testCertPEM(t, "upstream"), "junk"...) }, wantErr: "not valid PEM"},
		{name: "unparsable certificate", ca: func(*testing.T) []byte {
			return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("nope")})
		}, wantErr: "parse certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			base := filepath.Join(t.TempDir(), "ca-bundle.pem")
			if tt.base != nil {
				if err := os.WriteFile(base, tt.base(t), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.baseUnset {
				base = ""
			}
			if tt.ca != nil {
				if err := os.WriteFile(UpstreamCAFile(stateDir), tt.ca(t), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(GuestReadOnlyDir(stateDir), "upstream-ca-bundle.pem")
			const previous = "previous bundle"
			if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(out, []byte(previous), 0o600); err != nil {
				t.Fatal(err)
			}
			err := ComposeGuestCABundle(stateDir, base)
			got, rerr := os.ReadFile(out) //nolint:gosec // G304: inside the test's temporary directory
			if rerr != nil {
				t.Fatal(rerr)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ComposeGuestCABundle = %v, want %q", err, tt.wantErr)
				}
				if string(got) != previous {
					t.Fatalf("failed compose changed the bundle to %q", got)
				}
				if bytes.Contains([]byte(err.Error()), []byte("secret")) {
					t.Fatalf("error leaks PEM content: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ComposeGuestCABundle: %v", err)
			}
			if names := bundleNames(t, got); fmt.Sprint(names) != fmt.Sprint(tt.want) {
				t.Fatalf("bundle certificates = %v, want %v", names, tt.want)
			}
			if len(tt.want) == 0 && len(got) != 0 {
				t.Fatalf("bundle without certificates = %q, want empty", got)
			}
		})
	}
}

func TestComposeGuestCABundleDoesNotFollowLinks(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(GuestReadOnlyDir(stateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(GuestReadOnlyDir(stateDir), "upstream-ca-bundle.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UpstreamCAFile(stateDir), testCertPEM(t, "upstream"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = ComposeGuestCABundle(stateDir, "")
	if got, err := os.ReadFile(outside); err != nil || string(got) != "keep" { //nolint:gosec // G304: inside the test's temporary directory
		t.Fatalf("file behind the link = %q, %v; want it untouched", got, err)
	}
}

func TestBrokerArgsUpstreamFiles(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{KindWorkspace, KindBuilder} {
		for _, upstream := range []bool{false, true} {
			args := brokerArgs(Spec{Kind: kind, StateDir: "/state", Broker: []string{"jailoc", "netbroker"}, UpstreamProxy: upstream}, "/state/policy.json")
			for flag, want := range map[string]string{"-upstream-state": UpstreamStatusFile("/state"), "-upstream-ca": UpstreamCAFile("/state")} {
				var got []string
				for i, a := range args {
					if a == flag && i+1 < len(args) {
						got = append(got, args[i+1])
					}
				}
				if len(got) != 1 || got[0] != want {
					t.Errorf("%s upstream=%v: %s = %v, want [%s]", kind, upstream, flag, got, want)
				}
			}
		}
	}
}

func TestBrokerTimeouts(t *testing.T) {
	t.Parallel()
	if got := brokerReadyTimeout(Spec{}); got != 10*time.Second {
		t.Errorf("ready timeout = %s, want 10s", got)
	}
	if got := brokerReadyTimeout(Spec{UpstreamProxy: true}); got < 25*time.Second {
		t.Errorf("upstream ready timeout = %s, want at least 25s", got)
	}
	if got := brokerStartupGrace(Spec{}); got != 5*time.Second {
		t.Errorf("startup grace = %s, want 5s", got)
	}
	if got := brokerStartupGrace(Spec{UpstreamProxy: true}); got < 15*time.Second {
		t.Errorf("upstream startup grace = %s, want at least 15s", got)
	}
}

func TestSpecUpstreamJSON(t *testing.T) {
	t.Parallel()
	spec := Spec{UpstreamProxy: true, ReloadUpstreamCA: func(context.Context) error { return nil }}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec with a reload callback: %v", err)
	}
	if !bytes.Contains(data, []byte(`"upstream_proxy":true`)) || bytes.Contains(data, []byte("reload")) {
		t.Fatalf("spec JSON = %s", data)
	}
	data, err = json.Marshal(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("upstream_proxy")) {
		t.Fatalf("spec without upstream proxy = %s", data)
	}
}

func TestPrepareUpstreamBoot(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	if err := os.WriteFile(UpstreamCAFile(stateDir), testCertPEM(t, "upstream"), 0o600); err != nil {
		t.Fatal(err)
	}
	events := make(chan struct{}, 1)
	events <- struct{}{} // published by the broker before it was ready
	if err := prepareUpstreamBoot(Spec{Kind: KindWorkspace, StateDir: stateDir, UpstreamProxy: true}, events); err != nil {
		t.Fatalf("prepareUpstreamBoot: %v", err)
	}
	select {
	case <-events:
		t.Fatal("the startup event was left for the guest reload loop")
	default:
	}
	got, err := os.ReadFile(filepath.Join(GuestReadOnlyDir(stateDir), "upstream-ca-bundle.pem")) //nolint:gosec // G304: inside the test's temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if names := bundleNames(t, got); len(names) != 1 || names[0] != "upstream" {
		t.Fatalf("boot bundle = %v", names)
	}

	// Without the upstream proxy nothing is composed; a builder never
	// composes the guest bundle.
	for _, spec := range []Spec{{Kind: KindWorkspace}, {Kind: KindBuilder, UpstreamProxy: true}} {
		dir := t.TempDir()
		spec.StateDir = dir
		if err := prepareUpstreamBoot(spec, make(chan struct{}, 1)); err != nil {
			t.Fatalf("prepareUpstreamBoot(%s, upstream=%v): %v", spec.Kind, spec.UpstreamProxy, err)
		}
		if _, err := os.Stat(GuestReadOnlyDir(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s upstream=%v composed a guest bundle: %v", spec.Kind, spec.UpstreamProxy, err)
		}
	}
}

func TestResetUpstreamFiles(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	for _, p := range []string{UpstreamStatusFile(stateDir), UpstreamCAFile(stateDir)} {
		if err := os.WriteFile(p, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := resetUpstreamFiles(stateDir); err != nil {
		t.Fatalf("resetUpstreamFiles: %v", err)
	}
	for _, p := range []string{UpstreamStatusFile(stateDir), UpstreamCAFile(stateDir)} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived: %v", filepath.Base(p), err)
		}
	}
	if err := resetUpstreamFiles(stateDir); err != nil {
		t.Fatalf("resetUpstreamFiles without files: %v", err)
	}
}

func TestBuilderCABundle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := filepath.Join(dir, "base.pem")
	// A CRL is kept in the static copy and dropped from a composed bundle.
	raw := append(testCertPEM(t, "corp"), pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: []byte("crl")})...)
	if err := os.WriteFile(base, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UpstreamCAFile(dir), testCertPEM(t, "upstream"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		spec  Spec
		raw   bool
		names []string
	}{
		{name: "no bundle", spec: Spec{StateDir: dir}},
		{name: "static bundle copied unchanged", spec: Spec{StateDir: dir, CABundle: base}, raw: true},
		{name: "upstream CA appended", spec: Spec{StateDir: dir, CABundle: base, UpstreamProxy: true}, names: []string{"corp", "upstream"}},
		{name: "upstream CA alone", spec: Spec{StateDir: dir, UpstreamProxy: true}, names: []string{"upstream"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := builderCABundle(tt.spec)
			if err != nil {
				t.Fatalf("builderCABundle: %v", err)
			}
			switch {
			case tt.raw:
				if !bytes.Equal(got, raw) {
					t.Fatalf("bundle = %q, want the file unchanged", got)
				}
			case tt.names == nil:
				if got != nil {
					t.Fatalf("bundle = %q, want none", got)
				}
			default:
				if names := bundleNames(t, got); fmt.Sprint(names) != fmt.Sprint(tt.names) {
					t.Fatalf("bundle = %v, want %v", names, tt.names)
				}
			}
		})
	}
}

// logRecorder collects lifecycle log lines.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestWatchUpstreamCA(t *testing.T) {
	t.Parallel()
	events := make(chan struct{}, 1)
	calls := make(chan struct{}, 4)
	var n int
	reload := func(ctx context.Context) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n++
		calls <- struct{}{}
		if n == 1 {
			return errors.New("guest busy")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	var log logRecorder
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchUpstreamCA(ctx, events, reload, log.logf)
	}()
	for i := range 2 {
		events <- struct{}{}
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("reload %d not called", i+1)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchUpstreamCA did not return after cancellation")
	}
	if n != 2 {
		t.Fatalf("reload calls = %d, want 2", n)
	}
	if !strings.Contains(log.String(), "guest busy") {
		t.Fatalf("reload error not logged: %q", log.String())
	}
}
