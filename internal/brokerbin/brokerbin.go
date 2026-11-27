// Package brokerbin embeds jailoc-listener-broker, the host-side listener
// broker process, into release builds of jailoc.
//
// The binary for the host OS and architecture is built by `go generate`
// before a release build and embedded from bin/<os>-<arch>/. Builds without
// it (plain `go build` or `go install`) carry no broker binary, and Bytes and
// Path report ErrNotEmbedded.
package brokerbin

//go:generate env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o bin/darwin-arm64/jailoc-listener-broker ../../cmd/jailoc-listener-broker
//go:generate env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o bin/linux-amd64/jailoc-listener-broker ../../cmd/jailoc-listener-broker
//go:generate env CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o bin/linux-arm64/jailoc-listener-broker ../../cmd/jailoc-listener-broker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// FileName is the file name of the extracted listener broker binary.
const FileName = "jailoc-listener-broker"

// ErrNotEmbedded reports a jailoc build without an embedded listener broker binary.
var ErrNotEmbedded = errors.New("jailoc-listener-broker is not embedded in this build")

// Bytes returns the embedded listener broker binary, or ErrNotEmbedded when
// this build carries none.
func Bytes() ([]byte, error) {
	data := embedded()
	if len(data) == 0 {
		return nil, ErrNotEmbedded
	}
	return data, nil
}

// Path extracts the embedded listener broker binary into cacheDir, keyed by
// its SHA-256, and returns its path. An existing extraction with the same
// content is reused.
func Path(cacheDir string) (string, error) {
	data, err := Bytes()
	if err != nil {
		return "", err
	}
	return extract(cacheDir, data)
}

func extract(cacheDir string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	dir := filepath.Join(cacheDir, hex.EncodeToString(sum[:])[:16])
	path := filepath.Join(dir, FileName)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create listener broker binary cache: %w", err)
	}
	lock, err := os.Open(cacheDir) //nolint:gosec // G304: jailoc-owned cache directory
	if err != nil {
		return "", fmt.Errorf("open listener broker binary cache: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: file descriptors fit in int
		return "", fmt.Errorf("lock listener broker binary cache: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) { //nolint:gosec // G304: path inside jailoc's cache directory
		now := time.Now()
		_ = os.Chtimes(dir, now, now)
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create listener broker binary directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return "", fmt.Errorf("create listener broker binary: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write listener broker binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write listener broker binary: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // G302: the listener broker binary is executed by jailoc
		return "", fmt.Errorf("chmod listener broker binary: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("install listener broker binary: %w", err)
	}
	return path, nil
}
