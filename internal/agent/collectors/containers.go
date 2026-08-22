package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

const dockerProbeBackoff = 30 * time.Second

const (
	dockerStateUnknown     = "unknown"
	dockerStateOK          = "ok"
	dockerStateNoPerm      = "permission_denied"
	dockerStateAbsent      = "absent"
	dockerStateUnreachable = "unreachable"
)

type cpuSample struct {
	total  uint64
	system uint64
}

type containerCollector struct {
	mu        sync.Mutex
	cli       *client.Client
	nextProbe time.Time
	state     string
	stateMsg  string
	prevMu    sync.Mutex
	prev      map[string]cpuSample
}

func init() { Register(&containerCollector{}) }

func (c *containerCollector) Name() string        { return "containers" }
func (c *containerCollector) Platforms() []string { return []string{"all"} }

func (c *containerCollector) Status() wire.CollectorStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state
	if state == "" {
		state = dockerStateUnknown
	}
	return wire.CollectorStatus{State: state, Message: c.stateMsg}
}

func classifyDockerErr(err error) (string, string) {
	if err == nil {
		return dockerStateOK, ""
	}
	msg := truncateProbeMessage(err.Error(), 200)
	low := strings.ToLower(msg)
	switch {
	case errors.Is(err, fs.ErrPermission) || strings.Contains(low, "permission denied") || strings.Contains(low, "access is denied"):
		return dockerStateNoPerm, "docker endpoint is present but the agent may not read it: " + msg
	case errors.Is(err, fs.ErrNotExist) || strings.Contains(low, "no such file or directory") || strings.Contains(low, "cannot find the file"):
		return dockerStateAbsent, "no docker endpoint on this host"
	}
	return dockerStateUnreachable, msg
}

func (c *containerCollector) connect(ctx context.Context) *client.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli != nil {
		return c.cli
	}
	if time.Now().Before(c.nextProbe) {
		return nil
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.nextProbe = time.Now().Add(dockerProbeBackoff)
		c.state, c.stateMsg = classifyDockerErr(err)
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := cli.Ping(pctx); err != nil {
		_ = cli.Close()
		c.nextProbe = time.Now().Add(dockerProbeBackoff)
		c.state, c.stateMsg = classifyDockerErr(err)
		return nil
	}
	c.cli = cli
	c.state, c.stateMsg = dockerStateOK, ""
	return cli
}

func (c *containerCollector) drop(cli *client.Client, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli == cli {
		_ = c.cli.Close()
		c.cli = nil
	}
	c.state, c.stateMsg = classifyDockerErr(err)
}

func (c *containerCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	cli := c.connect(ctx)
	if cli == nil {
		return nil, nil
	}
	now := time.Now()
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		c.drop(cli, err)
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
	cli := c.connect(ctx)
	if cli == nil {
		return nil, nil
	}
	now := time.Now()
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		c.drop(cli, err)
		return nil, err
	}

	seen := make(map[string]cpuSample, len(list))
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
			if s, err := readStats(ctx, cli, cnt.ID); err == nil {
				cur := cpuSample{total: s.cpuTotal, system: s.cpuSystem}
				seen[cnt.ID] = cur
				c.prevMu.Lock()
				prev, ok := c.prev[cnt.ID]
				c.prevMu.Unlock()
				if ok {
					entry.CPUPct = float32(cpuPercent(prev, cur, s.onlineCPUs))
				}
				entry.MemUsed = s.memUsed
				entry.MemLimit = s.memLimit
				entry.RxBytes = s.rx
				entry.TxBytes = s.tx
			}
		}
		out = append(out, entry)
	}
	c.prevMu.Lock()
	c.prev = seen
	c.prevMu.Unlock()
	return out, nil
}

type contStats struct {
	cpuTotal   uint64
	cpuSystem  uint64
	onlineCPUs int
	memUsed    int64
	memLimit   int64
	rx, tx     int64
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

	var rx, tx int64
	for _, n := range v.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}

	online := v.CPU.OnlineCPUs
	if online < 1 {
		online = 1
	}

	return contStats{
		cpuTotal:   v.CPU.Usage.Total,
		cpuSystem:  v.CPU.SystemUsage,
		onlineCPUs: online,
		memUsed:    int64(v.Memory.Usage),
		memLimit:   int64(v.Memory.Limit),
		rx:         rx,
		tx:         tx,
	}, nil
}

func cpuPercent(prev, cur cpuSample, onlineCPUs int) float64 {
	if cur.total < prev.total || cur.system < prev.system {
		return 0
	}
	cpuDelta := float64(cur.total - prev.total)
	systemDelta := float64(cur.system - prev.system)
	if cpuDelta <= 0 || systemDelta <= 0 {
		return 0
	}
	if onlineCPUs < 1 {
		onlineCPUs = 1
	}
	pct := cpuDelta / systemDelta * float64(onlineCPUs) * 100.0
	if !isFinite(pct) || pct < 0 {
		return 0
	}
	return pct
}

type statsView struct {
	CPU    cpuStats `json:"cpu_stats"`
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
