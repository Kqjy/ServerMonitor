//go:build !linux && !windows

package collectors

import (
	"context"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

func scanProcCounts(ctx context.Context) (procCountSnapshot, error) {
	var snap procCountSnapshot
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return snap, err
	}
	snap.pids = len(pids)
	for _, pid := range pids {
		p, err := process.NewProcessWithContext(ctx, pid)
		if err != nil {
			continue
		}
		if st, err := p.StatusWithContext(ctx); err == nil {
			for _, s := range st {
				switch strings.ToUpper(s) {
				case "R", "RUNNING", "D", "U", "BLOCKED":
					snap.running++
				case "S", "SLEEP", "I", "IDLE":
					snap.sleep++
				case "Z", "ZOMBIE":
					snap.zombie++
				case "T", "STOP", "STOPPED":
					snap.stopped++
				}
			}
		}
		if n, err := p.NumThreadsWithContext(ctx); err == nil {
			snap.threads += int(n)
		}
	}
	return snap, nil
}
