//go:build !windows

package collectors

import "golang.org/x/sys/unix"

func defaultExecutableAccess() func(string) error {
	return func(path string) error {
		return unix.Access(path, unix.X_OK)
	}
}
