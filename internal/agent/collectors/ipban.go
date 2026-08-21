package collectors

import (
	"context"
	"sync"
	"time"

	"servermonitor/pkg/wire"
)

type IPBanSource interface {
	Report() *wire.IPBanReport
	Status() wire.CollectorStatus
	Points(now time.Time) []wire.Point
}

type ipbanCollector struct {
	mu     sync.Mutex
	source IPBanSource
}

var defaultIPBanCollector = &ipbanCollector{}

func init() { Register(defaultIPBanCollector) }

func SetIPBanSource(src IPBanSource) {
	defaultIPBanCollector.mu.Lock()
	defaultIPBanCollector.source = src
	defaultIPBanCollector.mu.Unlock()
}

func (c *ipbanCollector) Name() string        { return "ipban" }
func (c *ipbanCollector) Platforms() []string { return []string{"linux"} }

func (c *ipbanCollector) current() IPBanSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.source
}

func (c *ipbanCollector) Collect(context.Context) ([]wire.Point, error) {
	src := c.current()
	if src == nil {
		return nil, nil
	}
	return src.Points(time.Now()), nil
}

func (c *ipbanCollector) Status() wire.CollectorStatus {
	src := c.current()
	if src == nil {
		return wire.CollectorStatus{State: "pending", Message: "ip ban manager not started"}
	}
	return src.Status()
}

func (c *ipbanCollector) CollectIPBan(context.Context) *wire.IPBanReport {
	src := c.current()
	if src == nil {
		return nil
	}
	return src.Report()
}
