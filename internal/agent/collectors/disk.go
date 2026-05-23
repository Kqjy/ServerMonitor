package collectors

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type diskCollector struct {
	mu     sync.Mutex
	lastAt time.Time
	last   map[string]disk.IOCountersStat
}

func init() { Register(&diskCollector{}) }

func (c *diskCollector) Name() string        { return "disk" }
func (c *diskCollector) Platforms() []string { return []string{"all"} }

func (c *diskCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	stats, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]wire.Point, 0, len(stats)*5)

	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.last
	prevAt := c.lastAt
	c.last = stats
	c.lastAt = now

	if prev == nil {
		return nil, nil
	}
	elapsed := now.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return nil, nil
	}

	for name, s := range stats {
		if shouldSkipDevice(name) {
			continue
		}
		p, ok := prev[name]
		if !ok {
			continue
		}
		labels := map[string]string{"device": name}
		out = append(out,
			point(now, metrics.DiskReadBytes, labels, perSec(float64(s.ReadBytes), float64(p.ReadBytes), elapsed)),
			point(now, metrics.DiskWriteBytes, labels, perSec(float64(s.WriteBytes), float64(p.WriteBytes), elapsed)),
			point(now, metrics.DiskReadOps, labels, perSec(float64(s.ReadCount), float64(p.ReadCount), elapsed)),
			point(now, metrics.DiskWriteOps, labels, perSec(float64(s.WriteCount), float64(p.WriteCount), elapsed)),
		)
		if s.IoTime > p.IoTime {
			busy := float64(s.IoTime-p.IoTime) / 1000.0 / elapsed * 100.0
			if busy > 100 {
				busy = 100
			}
			out = append(out, point(now, metrics.DiskBusyPct, labels, busy))
		}
	}

	return out, nil
}

func perSec(curr, prev, elapsed float64) float64 {
	if curr < prev {
		return 0
	}
	if elapsed <= 0 {
		return 0
	}
	return (curr - prev) / elapsed
}

func shouldSkipDevice(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "loop") || strings.HasPrefix(lower, "ram") {
		return true
	}
	if strings.HasPrefix(lower, "dm-") || strings.HasPrefix(lower, "sr") {
		return true
	}
	return false
}
