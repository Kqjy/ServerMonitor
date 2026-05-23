package collectors

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

const (
	procCountTTL = 10 * time.Second
	procTopNTTL  = 10 * time.Second
)

type procCountSnapshot struct {
	when    time.Time
	pids    int
	running int
	sleep   int
	zombie  int
	stopped int
	threads int
}

type procTopNSnapshot struct {
	when time.Time
	topN int
	rows []wire.Process
}

type procCPUSample struct {
	createTime int64
	cputime    float64
}

type processCollector struct {
	mu              sync.Mutex
	last            procCountSnapshot
	lastTopN        procTopNSnapshot
	countRefreshing bool
	prevCPU         map[int32]procCPUSample
	prevCPUAt       time.Time
	lastTopNSkipped int
}

func init() { Register(&processCollector{}) }

func (c *processCollector) Name() string        { return "processes" }
func (c *processCollector) Platforms() []string { return []string{"all"} }

func (c *processCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	now := time.Now()
	c.mu.Lock()
	cached := c.last
	stale := cached.when.IsZero() || now.Sub(cached.when) >= procCountTTL
	startRefresh := stale && !c.countRefreshing
	if startRefresh {
		c.countRefreshing = true
	}
	c.mu.Unlock()

	if startRefresh {
		if cached.when.IsZero() {
			snap, err := scanProcCounts(ctx)
			c.mu.Lock()
			c.countRefreshing = false
			if err == nil {
				snap.when = time.Now()
				c.last = snap
				cached = snap
			}
			c.mu.Unlock()
			if err != nil {
				return nil, err
			}
		} else {
			go func() {
				snap, err := scanProcCounts(context.Background())
				c.mu.Lock()
				c.countRefreshing = false
				if err == nil {
					snap.when = time.Now()
					c.last = snap
				}
				c.mu.Unlock()
			}()
		}
	}
	c.mu.Lock()
	skipped := c.lastTopNSkipped
	c.mu.Unlock()
	return []wire.Point{
		point(now, metrics.ProcCount, nil, float64(cached.pids)),
		point(now, metrics.ProcRunning, nil, float64(cached.running)),
		point(now, metrics.ProcSleep, nil, float64(cached.sleep)),
		point(now, metrics.ProcZombie, nil, float64(cached.zombie)),
		point(now, metrics.ProcStop, nil, float64(cached.stopped)),
		point(now, metrics.ProcThreads, nil, float64(cached.threads)),
		point(now, metrics.ProcScanSkipped, nil, float64(skipped)),
	}, nil
}

func (c *processCollector) CollectProcesses(ctx context.Context, topN int) ([]wire.Process, error) {
	if topN <= 0 {
		topN = 50
	}
	now := time.Now()
	c.mu.Lock()
	cached := c.lastTopN
	c.mu.Unlock()

	if !cached.when.IsZero() && cached.topN == topN && now.Sub(cached.when) < procTopNTTL {
		out := make([]wire.Process, len(cached.rows))
		for i, p := range cached.rows {
			p.Time = now
			out[i] = p
		}
		return out, nil
	}

	rows, err := c.scanTopProcesses(ctx, topN)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lastTopN = procTopNSnapshot{when: time.Now(), topN: topN, rows: rows}
	c.mu.Unlock()
	out := make([]wire.Process, len(rows))
	for i, p := range rows {
		p.Time = now
		out[i] = p
	}
	return out, nil
}

func (c *processCollector) scanTopProcesses(ctx context.Context, topN int) ([]wire.Process, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	c.mu.Lock()
	prevCPU := c.prevCPU
	prevAt := c.prevCPUAt
	c.mu.Unlock()

	elapsed := now.Sub(prevAt).Seconds()
	havePrev := !prevAt.IsZero() && elapsed > 0
	nextCPU := make(map[int32]procCPUSample, len(procs))

	type row struct {
		pid     int32
		name    string
		user    string
		cmdline string
		cpu     float32
		rss     int64
		status  string
		threads int32
	}
	rows := make([]row, 0, len(procs))
	skipped := 0
	for _, p := range procs {
		if ctx.Err() != nil {
			break
		}
		times, err := p.TimesWithContext(ctx)
		if err != nil {
			skipped++
			continue
		}
		ct, _ := p.CreateTimeWithContext(ctx)
		cputime := times.User + times.System
		nextCPU[p.Pid] = procCPUSample{createTime: ct, cputime: cputime}

		var cpuPct float64
		if havePrev && ct != 0 {
			if prev, ok := prevCPU[p.Pid]; ok && prev.createTime == ct {
				if d := cputime - prev.cputime; d > 0 {
					cpuPct = d / elapsed * 100.0
				}
			}
		}

		mem, err := p.MemoryInfoWithContext(ctx)
		if err != nil || mem == nil {
			skipped++
			continue
		}
		name, _ := p.NameWithContext(ctx)
		user, _ := p.UsernameWithContext(ctx)
		cmd, _ := p.CmdlineWithContext(ctx)
		stat, _ := p.StatusWithContext(ctx)
		nt, _ := p.NumThreadsWithContext(ctx)
		rows = append(rows, row{
			pid:     p.Pid,
			name:    name,
			user:    user,
			cmdline: trunc(cmd, 512),
			cpu:     float32(cpuPct),
			rss:     int64(mem.RSS),
			status:  strings.Join(stat, ","),
			threads: nt,
		})
	}

	c.mu.Lock()
	c.prevCPU = nextCPU
	c.prevCPUAt = now
	c.lastTopNSkipped = skipped
	c.mu.Unlock()

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].cpu != rows[j].cpu {
			return rows[i].cpu > rows[j].cpu
		}
		return rows[i].rss > rows[j].rss
	})
	if len(rows) > topN {
		rows = rows[:topN]
	}

	out := make([]wire.Process, len(rows))
	for i, r := range rows {
		out[i] = wire.Process{
			PID:      r.pid,
			Name:     r.name,
			User:     r.user,
			Cmdline:  r.cmdline,
			CPUPct:   r.cpu,
			MemRSS:   r.rss,
			Status:   r.status,
			NThreads: r.threads,
		}
	}
	return out, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
