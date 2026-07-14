//go:build windows

package backup

import "os/exec"

func setGracefulCancel(_ *exec.Cmd) {}
