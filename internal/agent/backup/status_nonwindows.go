//go:build !windows

package backup

import "os"

func replaceFile(tmpPath, targetPath string) error {
	return os.Rename(tmpPath, targetPath)
}
