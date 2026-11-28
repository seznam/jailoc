//go:build darwin || linux

package netbroker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func detachUpstreamHelper(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return fmt.Errorf("kill upstream helper process group: %w", err)
		}
		return nil
	}
	return nil
}
