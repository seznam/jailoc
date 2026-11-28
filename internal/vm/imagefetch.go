package vm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stacklok/go-microvm/image"
)

// caImageFetcher returns the image fetcher of a builder VM whose bundle is
// caPath: the local Docker daemon first, then registry pulls that trust the
// system roots and the certificates in caPath. The trust is scoped to the
// returned fetcher's transport, so the host trust store and
// http.DefaultTransport stay unchanged. It fails closed when caPath holds no
// certificate.
func caImageFetcher(caPath string) (image.ImageFetcher, error) {
	tr, err := caTransport(caPath)
	if err != nil {
		return nil, err
	}
	return image.NewFallbackFetcher(image.DaemonFetcher{}, caRemoteFetcher{transport: tr}), nil
}

// caTransport returns a clone of http.DefaultTransport that trusts the system
// roots and the certificates in caPath.
func caTransport(caPath string) (*http.Transport, error) {
	pem, err := os.ReadFile(caPath) //nolint:gosec // G304: jailoc-owned bundle path
	if err != nil {
		return nil, fmt.Errorf("read CA bundle: %w", err)
	}
	return caTransportFromPEM(pem, caPath)
}

func caTransportFromPEM(pem []byte, label string) (*http.Transport, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	} else {
		pool = pool.Clone()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA bundle %q contains no certificates", label)
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport is %T, not *http.Transport", http.DefaultTransport)
	}
	tr := base.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr, nil
}

func caImageFetcherFromPEM(pem []byte, label string) (image.ImageFetcher, error) {
	tr, err := caTransportFromPEM(pem, label)
	if err != nil {
		return nil, err
	}
	return image.NewFallbackFetcher(image.DaemonFetcher{}, caRemoteFetcher{transport: tr}), nil
}

// caRemoteFetcher pulls images from registries over its own transport.
type caRemoteFetcher struct {
	transport http.RoundTripper
}

// Pull fetches ref from a remote registry.
func (f caRemoteFetcher) Pull(ctx context.Context, ref string) (v1.Image, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}
	return remote.Image(parsed,
		remote.WithContext(ctx),
		remote.WithTransport(f.transport),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: runtime.GOARCH}),
	)
}
