//go:build !linux

package backup

import (
	"fmt"
	"os/exec"
)

func setChroot(_ *exec.Cmd, root string) error {
	return fmt.Errorf("chrooted backup into %q is only supported on Linux", root)
}
