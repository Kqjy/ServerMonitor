package collectors

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/sensors"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type sensorsCollector struct{}

func init() { Register(&sensorsCollector{}) }

func (c *sensorsCollector) Name() string        { return "sensors" }
func (c *sensorsCollector) Platforms() []string { return []string{"linux", "darwin", "windows"} }

func (c *sensorsCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	out := make([]wire.Point, 0, 16)

	temps, _ := sensors.TemperaturesWithContext(ctx)
	for _, t := range temps {
		if t.Temperature == 0 {
			continue
		}
		out = append(out, point(now, metrics.SensorTempC, map[string]string{"sensor": t.SensorKey}, t.Temperature))
	}

	if info, err := host.InfoWithContext(ctx); err == nil && info != nil {
		_ = info
	}
	return out, nil
}
