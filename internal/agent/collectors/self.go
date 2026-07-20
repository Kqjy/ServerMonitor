package collectors

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type selfCollector struct {
	mu      sync.Mutex
	process *process.Process
}

func init() { Register(&selfCollector{}) }

func (c *selfCollector) Name() string        { return "agent" }
func (c *selfCollector) Platforms() []string { return []string{"all"} }

func (c *selfCollector) Collect(context.Context) ([]wire.Point, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process == nil {
		proc, err := process.NewProcess(int32(os.Getpid()))
		if err != nil {
			return nil, err
		}
		c.process = proc
	}
	cpuPct, err := c.process.Percent(0)
	if err != nil {
		return nil, err
	}
	memory, err := c.process.MemoryInfo()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return []wire.Point{
		point(now, metrics.AgentCPUPct, nil, cpuPct),
		point(now, metrics.AgentRSSBytes, nil, float64(memory.RSS)),
	}, nil
}
