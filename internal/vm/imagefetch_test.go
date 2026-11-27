package vm

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stacklok/go-microvm/image"
)

// tlsRegistry serves an in-memory registry over TLS with a certificate that
// only the returned bundle file trusts. It returns the image reference.
func tlsRegistry(t *testing.T) (ref, caPath string) {
	t.Helper()
	srv := httptest.NewTLSServer(registry.New())
	t.Cleanup(srv.Close)
	caPath = filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caPath, data, 0o600); err != nil {
		t.Fatalf("write CA bundle: %v", err)
	}
	ref = strings.TrimPrefix(srv.URL, "https://") + "/builder:latest"
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	if err := remote.Write(parsed, img, remote.WithTransport(srv.Client().Transport)); err != nil {
		t.Fatalf("push image: %v", err)
	}
	return ref, caPath
}

func TestCAImageFetcherTrustsBundle(t *testing.T) {
	t.Parallel()
	ref, caPath := tlsRegistry(t)

	if _, err := (image.RemoteFetcher{}).Pull(context.Background(), ref); err == nil {
		t.Fatal("default fetcher pulled from a registry with an untrusted certificate")
	}
	fetcher, err := caImageFetcher(caPath)
	if err != nil {
		t.Fatalf("caImageFetcher: %v", err)
	}
	if _, err := fetcher.Pull(context.Background(), ref); err != nil {
		t.Fatalf("Pull with CA bundle: %v", err)
	}
}

func TestCATransportKeepsSystemRootsAndDefaultTransport(t *testing.T) {
	t.Parallel()
	_, caPath := tlsRegistry(t)
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is %T", http.DefaultTransport)
	}
	system, err := x509.SystemCertPool()
	if err != nil {
		t.Fatalf("SystemCertPool: %v", err)
	}
	systemBefore := system.Clone()

	tr, err := caTransport(caPath)
	if err != nil {
		t.Fatalf("caTransport: %v", err)
	}
	if tr == defaultTransport {
		t.Fatal("caTransport returned http.DefaultTransport")
	}
	// The default transport may gain a TLS config lazily (HTTP/2); it must
	// not gain the bundle's roots.
	if cfg := defaultTransport.TLSClientConfig; cfg != nil && cfg.RootCAs != nil {
		t.Error("http.DefaultTransport trust was changed")
	}
	if !system.Equal(systemBefore) {
		t.Error("system certificate pool was mutated")
	}
	data, err := os.ReadFile(caPath) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatal(err)
	}
	want := systemBefore.Clone()
	if !want.AppendCertsFromPEM(data) {
		t.Fatal("append bundle to expected pool")
	}
	if !tr.TLSClientConfig.RootCAs.Equal(want) {
		t.Error("transport roots are not the system roots plus the bundle")
	}
}

func TestCAImageFetcherFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
	}{
		{name: "missing file", path: filepath.Join(dir, "missing.pem")},
		{name: "no certificates", path: empty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := caImageFetcher(tt.path); err == nil {
				t.Fatal("caImageFetcher() error = nil, want error")
			}
		})
	}
}
