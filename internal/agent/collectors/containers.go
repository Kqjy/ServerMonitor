package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

type containerCollector struct {
	mu         sync.Mutex
	cli        *client.Client
	probedOnce bool
	available  bool
}

func init() { Register(&containerCollector{}) }

func (c *containerCollector) Name() string        { return "containers" }
func (c *containerCollector) Platforms() []string { return []string{"all"} }

func (c *containerCollector) probe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probedOnce {
		return
	}
	c.probedOnce = true
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		_ = cli.Close()
		return
	}
	c.cli = cli
	c.available = true
}

func (c *containerCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	c.probe()
	if !c.available {
		return nil, nil
	}
	now := time.Now()
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	var running, stopped int
	for _, cnt := range containers {
		if strings.EqualFold(cnt.State, "running") {
			running++
		} else {
			stopped++
		}
	}
	return []wire.Point{
		point(now, metrics.ContainerCount, nil, float64(len(containers))),
		point(now, metrics.ContainerRunning, nil, float64(running)),
		point(now, metrics.ContainerStopped, nil, float64(stopped)),
	}, nil
}

func (c *containerCollector) CollectContainers(ctx context.Context) ([]wire.Container, error) {
	c.probe()
	if !c.available {
		return nil, nil
	}
	now := time.Now()
	list, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	out := make([]wire.Container, 0, len(list))
	for _, cnt := range list {
		entry := wire.Container{
			Time:  now,
			ID:    shortID(cnt.ID),
			Name:  primaryName(cnt.Names),
			Image: cnt.Image,
			State: cnt.State,
		}
		if strings.EqualFold(cnt.State, "running") {
			if s, err := readStats(ctx, c.cli, cnt.ID); err == nil {
				entry.CPUPct = float32(s.cpuPct)
				entry.MemUsed = s.memUsed
				entry.MemLimit = s.memLimit
				entry.RxBytes = s.rx
				entry.TxBytes = s.tx
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

type contStats struct {
	cpuPct   float64
	memUsed  int64
	memLimit int64
	rx, tx   int64
}

func readStats(ctx context.Context, cli *client.Client, id string) (contStats, error) {
	resp, err := cli.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return contStats{}, err
	}
	defer resp.Body.Close()

	var v statsView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return contStats{}, err
	}

	cpuDelta := float64(v.CPU.Usage.Total - v.PreCPU.Usage.Total)
	systemDelta := float64(v.CPU.SystemUsage - v.PreCPU.SystemUsage)
	cpuPct := 0.0
	if systemDelta > 0 && cpuDelta > 0 {
		cpuPct = (cpuDelta / systemDelta) * float64(max(v.CPU.OnlineCPUs, 1)) * 100.0
	}

	var rx, tx int64
	for _, n := range v.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}

	if cpuPct < 0 {
		cpuPct = 0
	}
	if !isFinite(cpuPct) {
		cpuPct = 0
	}
	if cpuPct > 10000 {
		return contStats{}, errors.New("implausible cpu pct")
	}

	return contStats{
		cpuPct:   cpuPct,
		memUsed:  int64(v.Memory.Usage),
		memLimit: int64(v.Memory.Limit),
		rx:       rx,
		tx:       tx,
	}, nil
}

type statsView struct {
	CPU    cpuStats `json:"cpu_stats"`
	PreCPU cpuStats `json:"precpu_stats"`
	Memory struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes int64 `json:"rx_bytes"`
		TxBytes int64 `json:"tx_bytes"`
	} `json:"networks"`
}

type cpuStats struct {
	Usage       cpuUsage `json:"cpu_usage"`
	SystemUsage uint64   `json:"system_cpu_usage"`
	OnlineCPUs  int      `json:"online_cpus"`
}

type cpuUsage struct {
	Total uint64 `json:"total_usage"`
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func primaryName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	n := names[0]
	return strings.TrimPrefix(n, "/")
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
