package docker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
)

// optionalBuildCA unpacks the optional trailing *BuildCA parameter of the
// preparatory fetch functions (loadDockerfile, fetchDockerfile,
// resolveRelease). No argument and a nil argument both mean no CA bundle.
//
// The variadic form only keeps existing callers compiling; once every caller
// passes its CA explicitly, replace it with a plain ca *BuildCA parameter.
func optionalBuildCA(cas []*BuildCA) (*BuildCA, error) {
	switch len(cas) {
	case 0:
		return nil, nil
	case 1:
		return cas[0], nil
	default:
		return nil, fmt.Errorf("at most one build CA is supported, got %d", len(cas))
	}
}

// buildcaTLSRoots returns the roots to verify TLS servers with when ca is
// active: a private snapshot of fetchRootCAs (or of the system roots when it
// is nil) plus the certificates of ca. It returns nil for a nil ca, which
// leaves the caller's roots unchanged. The package pool, the system pool and ca
// are never mutated.
func buildcaTLSRoots(ca *BuildCA) (*x509.CertPool, error) {
	if ca == nil {
		return nil, nil
	}
	var pool *x509.CertPool
	if fetchRootCAs != nil {
		pool = fetchRootCAs.Clone()
	} else {
		system, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system root CAs: %w", err)
		}
		pool = system.Clone()
	}
	// The bundle was checked when the BuildCA was made; check again that it
	// yields roots rather than silently trusting nothing extra.
	if !pool.AppendCertsFromPEM(ca.pem) {
		return nil, errors.New("build CA bundle contains no certificates")
	}
	return pool, nil
}

// buildcaHTTPTransport returns a transport that trusts the system roots plus
// ca, cloned from http.DefaultTransport so proxy, timeout and HTTP/2 settings
// stay as they are. It returns nil for a nil ca. The caller owns the transport
// and closes its idle connections; http.DefaultTransport is not modified.
func buildcaHTTPTransport(ca *BuildCA) (*http.Transport, error) {
	roots, err := buildcaTLSRoots(ca)
	if err != nil || roots == nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("http.DefaultTransport is not an *http.Transport")
	}
	transport := base.Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	return transport, nil
}
