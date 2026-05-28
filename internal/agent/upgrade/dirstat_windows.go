//go:build windows

package upgrade

import "os"

func dirGroupOwnedByRunningUID(fi os.FileInfo) bool {
	return true
}
