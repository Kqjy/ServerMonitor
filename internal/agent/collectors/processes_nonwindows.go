//go:build !windows

package collectors

import "context"

const (
	procMetaCacheEnabled = false
	procStatesSupported  = true
)

func threadCountsByPID(_ context.Context) (map[int32]int32, error) {
	return nil, nil
}
