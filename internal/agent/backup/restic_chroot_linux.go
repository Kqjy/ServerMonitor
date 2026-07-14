//go:build linux

package backup

import (
	"os/exec"
	"syscall"
)

func setChroot(cmd *exec.Cmd, root string) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Chroot = root
	cmd.Dir = "/"
	return nil
}
