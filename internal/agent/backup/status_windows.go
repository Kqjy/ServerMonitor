//go:build windows

package backup

import "golang.org/x/sys/windows"

func replaceFile(tmpPath, targetPath string) error {
	return windows.MoveFileEx(windows.StringToUTF16Ptr(tmpPath), windows.StringToUTF16Ptr(targetPath), windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
