//go:build !windows

package backup

import (
	"os"
	"syscall"
)

func replaceFile(tmpPath, targetPath string) error {
	return os.Rename(tmpPath, targetPath)
}

func preserveOwner(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if cur, err := os.Stat(path); err == nil {
		if cst, ok := cur.Sys().(*syscall.Stat_t); ok && cst.Uid == st.Uid && cst.Gid == st.Gid {
			return nil
		}
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}
