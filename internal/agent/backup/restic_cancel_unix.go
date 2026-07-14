//go:build !windows

package backup

import (
	"os/exec"
	"syscall"
)

func setGracefulCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
}
