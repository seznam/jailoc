//go:build vmintegration

// Command caserver is the HTTPS fixture of the builder CA bundle integration
// test. It runs in a container of the builder VM's Docker daemon and serves,
// over TLS with the certificate in TLS_CERT and TLS_KEY (PEM), a minimal
// anonymous OCI registry with a one-layer image for any repository and tag,
// and GET /add.txt for HTTPS remote ADD. It uses only the standard library
// so the test can build it as a static binary.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	addBody          = "from-https-add\n"
	registryFile     = "/from-registry.txt"
	registryFileBody = "hello-from-registry\n"

	mediaManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaConfig   = "application/vnd.oci.image.config.v1+json"
	mediaLayer    = "application/vnd.oci.image.layer.v1.tar+gzip"
)

type blob struct {
	data      []byte
	mediaType string
}

func (b blob) digest() string {
	sum := sha256.Sum256(b.data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// image is a one-layer OCI image: /etc/debian_version and /from-registry.txt.
type image struct {
	manifest blob
	blobs    map[string]blob
}

func newImage() (*image, error) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, f := range []struct{ name, body string }{
		{"etc/debian_version", "12\n"},
		{strings.TrimPrefix(registryFile, "/"), registryFileBody},
	} {
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("write tar header: %w", err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			return nil, fmt.Errorf("write tar file: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	diffSum := sha256.Sum256(raw.Bytes())

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return nil, fmt.Errorf("gzip layer: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	layer := blob{data: gz.Bytes(), mediaType: mediaLayer}

	cfgJSON, err := json.Marshal(map[string]any{
		"architecture": runtime.GOARCH,
		"os":           "linux",
		"config":       map[string]any{},
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + hex.EncodeToString(diffSum[:])}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	cfg := blob{data: cfgJSON, mediaType: mediaConfig}

	descriptor := func(b blob) map[string]any {
		return map[string]any{"mediaType": b.mediaType, "digest": b.digest(), "size": len(b.data)}
	}
	manifestJSON, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaManifest,
		"config":        descriptor(cfg),
		"layers":        []any{descriptor(layer)},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return &image{
		manifest: blob{data: manifestJSON, mediaType: mediaManifest},
		blobs:    map[string]blob{cfg.digest(): cfg, layer.digest(): layer},
	}, nil
}

func serveBlob(w http.ResponseWriter, b blob) {
	w.Header().Set("Content-Type", b.mediaType)
	w.Header().Set("Content-Length", fmt.Sprint(len(b.data)))
	w.Header().Set("Docker-Content-Digest", b.digest())
	_, _ = w.Write(b.data) // net/http drops the body of a HEAD response
}

func (img *image) registry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	if path == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if i := strings.LastIndex(path, "/manifests/"); i > 0 {
		serveBlob(w, img.manifest)
		return
	}
	if i := strings.LastIndex(path, "/blobs/"); i > 0 {
		b, ok := img.blobs[path[i+len("/blobs/"):]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		serveBlob(w, b)
		return
	}
	http.NotFound(w, r)
}

func main() {
	img, err := newImage()
	if err != nil {
		log.Fatalf("build image: %v", err)
	}
	cert, err := tls.X509KeyPair([]byte(os.Getenv("TLS_CERT")), []byte(os.Getenv("TLS_KEY")))
	if err != nil {
		log.Fatalf("load TLS key pair: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", img.registry)
	mux.HandleFunc("/add.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(addBody))
	})
	ln, err := tls.Listen("tcp", ":443", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}) //nolint:gosec // G102: listens on the container's bridge interface
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	// The test waits for this line before it lets a build fetch.
	fmt.Println("caserver listening")
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}
