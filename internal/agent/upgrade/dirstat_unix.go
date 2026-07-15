//go:build !windows

package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func dirGroupOwnedByRunningUID(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Gid) == os.Getegid()
}

func replaceFileAtomic(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func fileOwnedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func verifyRootOwnedPath(dir string) error {
	cur := filepath.Clean(dir)
	for {
		info, err := os.Stat(cur)
		if err != nil {
			return fmt.Errorf("stat %q: %w", cur, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", cur)
		}
		if !fileOwnedByRoot(info) {
			return fmt.Errorf("%q is not root-owned", cur)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%q is group- or world-writable (mode %#o)", cur, info.Mode().Perm())
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}
