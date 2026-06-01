package collectors

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/load"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type loadCollector struct{}

func init() { Register(&loadCollector{}) }

func (c *loadCollector) Name() string { return "load" }
func (c *loadCollector) Platforms() []string {
	return []string{"linux", "darwin", "freebsd", "openbsd"}
}

func (c *loadCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	la, err := load.AvgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return []wire.Point{
		point(now, metrics.LoadAvg1, nil, la.Load1),
		point(now, metrics.LoadAvg5, nil, la.Load5),
		point(now, metrics.LoadAvg15, nil, la.Load15),
	}, nil
}
