package collectors

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/net"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type netCollector struct {
	mu     sync.Mutex
	lastAt time.Time
	last   map[string]net.IOCountersStat
}

func init() { Register(&netCollector{}) }

func (c *netCollector) Name() string        { return "net" }
func (c *netCollector) Platforms() []string { return []string{"all"} }

func (c *netCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	stats, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	curr := make(map[string]net.IOCountersStat, len(stats))
	for _, s := range stats {
		curr[s.Name] = s
	}

	out := make([]wire.Point, 0, len(stats)*4)

	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.last
	prevAt := c.lastAt
	c.last = curr
	c.lastAt = now

	if prev == nil {
		return nil, nil
	}
	elapsed := now.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return nil, nil
	}

	for name, s := range curr {
		if skipIface(name) {
			continue
		}
		p, ok := prev[name]
		if !ok {
			continue
		}
		labels := map[string]string{"iface": name}
		out = append(out,
			point(now, metrics.NetRxBytes, labels, perSec(float64(s.BytesRecv), float64(p.BytesRecv), elapsed)),
			point(now, metrics.NetTxBytes, labels, perSec(float64(s.BytesSent), float64(p.BytesSent), elapsed)),
			point(now, metrics.NetRxPackets, labels, perSec(float64(s.PacketsRecv), float64(p.PacketsRecv), elapsed)),
			point(now, metrics.NetTxPackets, labels, perSec(float64(s.PacketsSent), float64(p.PacketsSent), elapsed)),
			point(now, metrics.NetRxErrors, labels, perSec(float64(s.Errin), float64(p.Errin), elapsed)),
			point(now, metrics.NetTxErrors, labels, perSec(float64(s.Errout), float64(p.Errout), elapsed)),
			point(now, metrics.NetRxDropped, labels, perSec(float64(s.Dropin), float64(p.Dropin), elapsed)),
			point(now, metrics.NetTxDropped, labels, perSec(float64(s.Dropout), float64(p.Dropout), elapsed)),
		)
	}
	return out, nil
}

func skipIface(name string) bool {
	low := strings.ToLower(name)
	if strings.HasPrefix(low, "lo") && len(low) <= 4 {
		return true
	}
	if strings.HasPrefix(low, "docker") || strings.HasPrefix(low, "br-") || strings.HasPrefix(low, "veth") {
		return true
	}
	return false
}
