// Package guestbin embeds jailoc-guest, the init process and SSH server of
// workspace VMs, into release builds of jailoc.
//
// The binary for the host architecture is built by `go generate` before a
// release build and embedded from bin/<arch>/. Builds without it (plain
// `go build` or `go install`) carry no guest binary, and Path reports
// ErrNotEmbedded.
package guestbin

//go:generate env CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o bin/arm64/jailoc-guest ../../cmd/jailoc-guest
//go:generate env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o bin/amd64/jailoc-guest ../../cmd/jailoc-guest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the file name of the extracted guest binary.
const FileName = "jailoc-guest"

// ErrNotEmbedded reports a jailoc build without an embedded guest binary.
var ErrNotEmbedded = errors.New("jailoc-guest is not embedded in this build")

// Path extracts the embedded guest binary into cacheDir, keyed by its
// SHA-256, and returns its path. An existing extraction with the same
// content is reused.
func Path(cacheDir string) (string, error) {
	data := embedded()
	if len(data) == 0 {
		return "", ErrNotEmbedded
	}
	return extract(cacheDir, data)
}

func extract(cacheDir string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	dir := filepath.Join(cacheDir, hex.EncodeToString(sum[:])[:16])
	path := filepath.Join(dir, FileName)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) { //nolint:gosec // G304: path inside jailoc's cache directory
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create guest binary directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return "", fmt.Errorf("create guest binary: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write guest binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write guest binary: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // G302: the guest binary is executed inside the VM
		return "", fmt.Errorf("chmod guest binary: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("install guest binary: %w", err)
	}
	return path, nil
}
