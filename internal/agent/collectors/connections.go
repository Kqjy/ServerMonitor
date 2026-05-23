package collectors

import (
	"context"
	"time"

	gopsnet "github.com/shirou/gopsutil/v4/net"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type connCollector struct{}

func init() { Register(&connCollector{}) }

func (c *connCollector) Name() string        { return "connections" }
func (c *connCollector) Platforms() []string { return []string{"linux", "darwin", "windows", "freebsd"} }

func (c *connCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	conns, err := gopsnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, err
	}
	var established, listen, timeWait, ports int
	for _, c := range conns {
		switch c.Status {
		case "ESTABLISHED":
			established++
		case "LISTEN":
			listen++
			ports++
		case "TIME_WAIT":
			timeWait++
		}
	}
	return []wire.Point{
		point(now, metrics.ConnEstab, nil, float64(established)),
		point(now, metrics.ConnListen, nil, float64(listen)),
		point(now, metrics.ConnTimeWait, nil, float64(timeWait)),
		point(now, metrics.PortOpen, nil, float64(ports)),
	}, nil
}
