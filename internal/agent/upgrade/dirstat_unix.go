//go:build !windows

package upgrade

import (
	"os"
	"syscall"
)

func dirGroupOwnedByRunningUID(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Gid) == os.Getegid()
}
