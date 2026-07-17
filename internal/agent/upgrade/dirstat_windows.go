//go:build windows

package upgrade

import (
	"os"

	"golang.org/x/sys/windows"
)

func dirGroupOwnedByRunningUID(fi os.FileInfo) bool {
	return true
}

func replaceFileAtomic(oldPath, newPath string) error {
	return windows.MoveFileEx(
		windows.StringToUTF16Ptr(oldPath),
		windows.StringToUTF16Ptr(newPath),
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

func fileOwnedByRoot(os.FileInfo) bool { return true }

func privilegedRunningAsRoot() bool { return false }

func privilegedOwnerNeedsRepair(os.FileInfo) bool { return false }

func chownPrivilegedRoot(string) error { return nil }

func verifyRootOwnedPath(string) error { return nil }
