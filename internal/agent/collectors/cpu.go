package collectors

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type cpuCollector struct {
	mu      sync.Mutex
	lastVal cpu.TimesStat
	hasLast bool
}

func init() { Register(&cpuCollector{}) }

func (c *cpuCollector) Name() string        { return "cpu" }
func (c *cpuCollector) Platforms() []string { return []string{"all"} }

func (c *cpuCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	out := make([]wire.Point, 0, 16)

	totals, err := cpu.TimesWithContext(ctx, false)
	if err == nil && len(totals) > 0 {
		curr := totals[0]
		c.mu.Lock()
		prev := c.lastVal
		had := c.hasLast
		c.lastVal = curr
		c.hasLast = true
		c.mu.Unlock()

		if had {
			dUser := curr.User - prev.User
			dSys := curr.System - prev.System
			dIdle := curr.Idle - prev.Idle
			dNice := curr.Nice - prev.Nice
			dIO := curr.Iowait - prev.Iowait
			dIrq := curr.Irq - prev.Irq
			dSI := curr.Softirq - prev.Softirq
			dSteal := curr.Steal - prev.Steal
			monotonic := dUser >= 0 && dSys >= 0 && dIdle >= 0 && dNice >= 0 && dIO >= 0 && dIrq >= 0 && dSI >= 0 && dSteal >= 0
			if monotonic {
				sum := dUser + dSys + dIdle + dNice + dIO + dIrq + dSI + dSteal
				if sum > 0 {
					out = append(out,
						point(now, metrics.CPUUserPct, nil, pct(dUser, sum)),
						point(now, metrics.CPUSystemPct, nil, pct(dSys, sum)),
						point(now, metrics.CPUIdlePct, nil, pct(dIdle, sum)),
						point(now, metrics.CPUIOWaitPct, nil, pct(dIO, sum)),
						point(now, metrics.CPUStealPct, nil, pct(dSteal, sum)),
						point(now, metrics.CPUTotalPct, nil, 100-pct(dIdle, sum)),
					)
				}
			}
		}
	}

	if perc, err := cpu.PercentWithContext(ctx, 0, true); err == nil {
		for i, p := range perc {
			out = append(out, point(now, metrics.CPUCorePct, map[string]string{"core": strconv.Itoa(i)}, p))
		}
	}

	if freqs, err := cpu.InfoWithContext(ctx); err == nil && len(freqs) > 0 {
		out = append(out, point(now, metrics.CPUFreqMHz, nil, freqs[0].Mhz))
	}

	return out, nil
}

func pct(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return part * 100.0 / total
}

func point(t time.Time, id metrics.ID, labels map[string]string, v float64) wire.Point {
	return wire.Point{Time: t, Metric: id, Labels: labels, Value: v}
}
