//go:build !darwin && !linux

package netbroker

import (
	"errors"
	"os/exec"
)

func detachUpstreamHelper(_ *exec.Cmd) error {
	return errors.New("upstream command helpers require a Linux or macOS host")
}
