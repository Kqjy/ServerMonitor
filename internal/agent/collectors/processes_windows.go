//go:build windows

package collectors

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	procMetaCacheEnabled = true
	procStatesSupported  = false
)

func threadCountsByPID(ctx context.Context) (map[int32]int32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil, err
	}

	counts := make(map[int32]int32, 256)
	for {
		if ctx.Err() != nil {
			return counts, ctx.Err()
		}
		counts[int32(entry.ProcessID)] = int32(entry.Threads)
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return counts, nil
}

func scanProcCounts(ctx context.Context) (procCountSnapshot, error) {
	counts, err := threadCountsByPID(ctx)
	if err != nil {
		return procCountSnapshot{}, err
	}
	snap := procCountSnapshot{pids: len(counts)}
	for _, n := range counts {
		snap.threads += int(n)
	}
	return snap, nil
}
