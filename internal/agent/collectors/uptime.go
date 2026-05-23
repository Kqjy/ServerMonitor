package collectors

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type uptimeCollector struct{}

func init() { Register(&uptimeCollector{}) }

func (c *uptimeCollector) Name() string        { return "uptime" }
func (c *uptimeCollector) Platforms() []string { return []string{"all"} }

func (c *uptimeCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	up, err := host.UptimeWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return []wire.Point{point(time.Now(), metrics.UptimeSec, nil, float64(up))}, nil
}
