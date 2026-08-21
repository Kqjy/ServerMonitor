package runner

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"

	"servermonitor/internal/agent/collectors"
	"servermonitor/internal/agent/config"
	"servermonitor/internal/agent/transport"
	"servermonitor/pkg/version"
	"servermonitor/pkg/wire"
)

var ErrDeregistered = errors.New("host deregistered by server")

const Version = version.Version

type Runner struct {
	cfg               *config.Config
	client            *transport.Client
	logger            *slog.Logger
	collectors        []collectors.Collector
	intervalCh        chan time.Duration
	externallyManaged bool
}

func New(cfg *config.Config, client *transport.Client, logger *slog.Logger) *Runner {
	collectors.SetBackupStatusPath(cfg.BackupStatusPath)
	return &Runner{
		cfg:        cfg,
		client:     client,
		logger:     logger,
		collectors: collectors.Filtered(cfg.Enabled, cfg.Disabled),
		intervalCh: make(chan time.Duration, 1),
	}
}

func (r *Runner) SetExternallyManaged(v bool) {
	r.externallyManaged = v
}

func (r *Runner) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case r.intervalCh <- d:
	default:
	}
}

type cpuIdentity struct {
	model   string
	cores   int
	threads int
}

var cpuIdent = sync.OnceValue(func() cpuIdentity {
	var id cpuIdentity
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		id.model = strings.TrimSpace(infos[0].ModelName)
		if id.model == "" {
			id.model = strings.TrimSpace(infos[0].VendorID)
		}
	}
	if n, err := cpu.Counts(true); err == nil && n > 0 {
		id.threads = n
	}
	if n, err := cpu.Counts(false); err == nil && n > 0 && (id.threads == 0 || n <= id.threads) {
		id.cores = n
	}
	return id
})

func (r *Runner) HostInfo() wire.HostInfo {
	hn, _ := os.Hostname()
	var kernel string
	if info, err := host.Info(); err == nil {
		kernel = info.KernelVersion
	}
	cpuID := cpuIdent()
	names := make([]string, 0, len(r.collectors))
	var statuses map[string]wire.CollectorStatus
	for _, c := range r.collectors {
		names = append(names, c.Name())
		if sr, ok := c.(collectors.StatusReporter); ok {
			if statuses == nil {
				statuses = make(map[string]wire.CollectorStatus)
			}
			statuses[c.Name()] = sr.Status()
		}
	}
	managed := r.externallyManaged
	return wire.HostInfo{
		Hostname:          hn,
		OS:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		Kernel:            kernel,
		CPUModel:          cpuID.model,
		CPUCores:          cpuID.cores,
		CPUThreads:        cpuID.threads,
		AgentVersion:      Version,
		Collectors:        names,
		CollectorStatus:   statuses,
		Tags:              r.cfg.Tags,
		ExternallyManaged: &managed,
	}
}

func (r *Runner) Run(ctx context.Context) error {
	interval := r.cfg.Interval()
	r.logger.Info("agent started",
		"interval", interval,
		"collectors", len(r.collectors),
		"server", r.cfg.ServerURL)

	t := time.NewTicker(interval)
	defer t.Stop()

	dereg := r.client.Deregistered()

	r.tick(ctx)
	select {
	case <-dereg:
		return ErrDeregistered
	default:
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-dereg:
			return ErrDeregistered
		case d := <-r.intervalCh:
			if d != interval {
				interval = d
				t.Reset(d)
				r.logger.Info("interval updated", "interval", d)
			}
		case <-t.C:
			r.tick(ctx)
			select {
			case <-dereg:
				return ErrDeregistered
			default:
			}
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	batch := &wire.Batch{
		Sent: time.Now(),
	}

	tickCtx, cancel := context.WithTimeout(ctx, r.cfg.Interval()-100*time.Millisecond)
	defer cancel()

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		procs []wire.Process
		conts []wire.Container
		ports []wire.Port
		baks  []wire.BackupRepoStatus
		bans  *wire.IPBanReport
	)

	for _, c := range r.collectors {
		wg.Add(1)
		go func(col collectors.Collector) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					r.logger.Error("collector panic", "name", col.Name(), "panic", rec, "stack", string(debug.Stack()))
				}
			}()
			points, err := col.Collect(tickCtx)
			if err != nil {
				r.logger.Warn("collector error", "name", col.Name(), "err", err)
				return
			}
			mu.Lock()
			batch.Points = append(batch.Points, points...)
			mu.Unlock()

			if pc, ok := col.(collectors.ProcessCollector); ok {
				ps, err := pc.CollectProcesses(tickCtx, r.cfg.ProcessTopN)
				if err != nil {
					r.logger.Warn("collect processes", "err", err)
				} else {
					mu.Lock()
					procs = append(procs, ps...)
					mu.Unlock()
				}
			}
			if cc, ok := col.(collectors.ContainerCollector); ok {
				if cs, err := cc.CollectContainers(tickCtx); err == nil {
					mu.Lock()
					conts = append(conts, cs...)
					mu.Unlock()
				}
			}
			if pc, ok := col.(collectors.PortCollector); ok {
				if ps, err := pc.CollectPorts(tickCtx); err == nil {
					mu.Lock()
					ports = append(ports, ps...)
					mu.Unlock()
				}
			}
			if bc, ok := col.(collectors.BackupCollector); ok {
				if bs, err := bc.CollectBackups(tickCtx); err == nil {
					mu.Lock()
					baks = append(baks, bs...)
					mu.Unlock()
				}
			}
			if ir, ok := col.(collectors.IPBanReporter); ok {
				if report := ir.CollectIPBan(tickCtx); report != nil {
					mu.Lock()
					bans = report
					mu.Unlock()
				}
			}
		}(c)
	}
	wg.Wait()

	batch.Host = r.HostInfo()
	batch.Processes = procs
	batch.Containers = conts
	batch.Ports = ports
	batch.Backups = baks
	batch.IPBan = bans

	if len(batch.Points) == 0 && len(procs) == 0 && len(conts) == 0 && len(ports) == 0 && len(baks) == 0 && (bans == nil || len(bans.Events) == 0) {
		return
	}

	sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
	defer sendCancel()
	if err := r.client.Send(sendCtx, batch); err != nil {
		r.logger.Error("send failed", "err", err, "points", len(batch.Points))
		return
	}
	r.logger.Debug("send ok", "points", len(batch.Points), "procs", len(procs), "containers", len(conts), "ports", len(ports), "backups", len(baks))
}
