//go:build !darwin && !linux

package netbroker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func detachUpstreamHelper(_ *exec.Cmd) error {
	return errors.New("upstream command helpers require a Linux or macOS host")
}

func openUpstreamCAFile(path string) (*os.File, error) {
	f, err := os.Open(path) //nolint:gosec // G304: host-configured or helper-returned CA path
	if err != nil {
		return nil, fmt.Errorf("open upstream ca: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat upstream ca: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("upstream ca is not a regular file")
	}
	return f, nil
}
