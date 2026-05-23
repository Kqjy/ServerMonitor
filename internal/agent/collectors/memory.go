package collectors

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/mem"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type memoryCollector struct{}

func init() { Register(&memoryCollector{}) }

func (c *memoryCollector) Name() string        { return "memory" }
func (c *memoryCollector) Platforms() []string { return []string{"all"} }

func (c *memoryCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	out := make([]wire.Point, 0, 11)

	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		out = append(out,
			point(now, metrics.MemTotal, nil, float64(v.Total)),
			point(now, metrics.MemUsed, nil, float64(v.Used)),
			point(now, metrics.MemFree, nil, float64(v.Free)),
			point(now, metrics.MemAvailable, nil, float64(v.Available)),
			point(now, metrics.MemBuffers, nil, float64(v.Buffers)),
			point(now, metrics.MemCached, nil, float64(v.Cached)),
			point(now, metrics.MemUsedPct, nil, v.UsedPercent),
		)
	}

	if s, err := mem.SwapMemoryWithContext(ctx); err == nil {
		out = append(out,
			point(now, metrics.SwapTotal, nil, float64(s.Total)),
			point(now, metrics.SwapUsed, nil, float64(s.Used)),
			point(now, metrics.SwapFree, nil, float64(s.Free)),
			point(now, metrics.SwapUsedPct, nil, s.UsedPercent),
		)
	}

	return out, nil
}
