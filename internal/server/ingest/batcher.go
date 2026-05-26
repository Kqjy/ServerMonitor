package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"servermonitor/pkg/wire"
)

type Row struct {
	Time    time.Time
	HostID  int64
	Metric  int16
	Labels  []byte
	Value   float64
}

type Batcher struct {
	pool      *pgxpool.Pool
	in        chan Row
	enqueueMu sync.Mutex
	reserved  int
	maxRows   int
	maxAge    time.Duration
	dropped   atomic.Uint64
	flushed   atomic.Uint64
	logger    *slog.Logger
	wg        sync.WaitGroup
	stopOnce  sync.Once
	stopCh    chan struct{}
}

func NewBatcher(pool *pgxpool.Pool, maxRows int, maxAge time.Duration, logger *slog.Logger) *Batcher {
	if maxRows <= 0 {
		maxRows = 50000
	}
	if maxAge <= 0 {
		maxAge = 2 * time.Second
	}
	return &Batcher{
		pool:    pool,
		in:      make(chan Row, 200_000),
		maxRows: maxRows,
		maxAge:  maxAge,
		logger:  logger,
		stopCh:  make(chan struct{}),
	}
}

func (b *Batcher) Start(ctx context.Context) {
	b.wg.Add(1)
	go b.loop(ctx)
}

func (b *Batcher) Stop(ctx context.Context) error {
	b.stopOnce.Do(func() { close(b.stopCh) })
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var ErrBackpressure = errors.New("ingest queue full")

func (b *Batcher) Enqueue(rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	if err := b.Reserve(len(rows)); err != nil {
		return err
	}
	b.CommitReserved(rows)
	return nil
}

func (b *Batcher) Reserve(n int) error {
	if n <= 0 {
		return nil
	}
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	if n > cap(b.in)-len(b.in)-b.reserved {
		b.dropped.Add(uint64(n))
		return ErrBackpressure
	}
	b.reserved += n
	return nil
}

func (b *Batcher) CommitReserved(rows []Row) {
	if len(rows) == 0 {
		return
	}
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	for i := range rows {
		b.in <- rows[i]
		b.reserved--
	}
}

func (b *Batcher) ReleaseReserved(n int) {
	if n <= 0 {
		return
	}
	b.enqueueMu.Lock()
	defer b.enqueueMu.Unlock()
	b.reserved -= n
}

func (b *Batcher) Stats() (queued int, flushed uint64, dropped uint64) {
	return len(b.in), b.flushed.Load(), b.dropped.Load()
}

func (b *Batcher) Pool() *pgxpool.Pool { return b.pool }

func (b *Batcher) loop(ctx context.Context) {
	defer b.wg.Done()
	buf := make([]Row, 0, b.maxRows)
	t := time.NewTimer(b.maxAge)
	defer t.Stop()

	flush := func(flushCtx context.Context) {
		defer func() {
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(b.maxAge)
		}()
		if len(buf) == 0 {
			return
		}
		if err := b.copyRows(flushCtx, buf); err != nil {
			b.logger.Error("ingest copy failed", "err", err, "rows", len(buf))
		} else {
			b.flushed.Add(uint64(len(buf)))
		}
		buf = buf[:0]
	}

	drainAndFlush := func() {
		for {
			select {
			case r := <-b.in:
				buf = append(buf, r)
			default:
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				flush(shutdownCtx)
				cancel()
				return
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			drainAndFlush()
			return
		case <-b.stopCh:
			drainAndFlush()
			return
		case r := <-b.in:
			buf = append(buf, r)
			if len(buf) >= b.maxRows {
				flush(ctx)
			}
		case <-t.C:
			flush(ctx)
		}
	}
}

func (b *Batcher) copyRows(ctx context.Context, rows []Row) error {
	src := pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
		r := rows[i]
		labels := r.Labels
		if len(labels) == 0 {
			labels = []byte("{}")
		}
		return []any{r.Time, r.HostID, r.Metric, labels, r.Value}, nil
	})
	_, err := b.pool.CopyFrom(ctx, pgx.Identifier{"metric_points"},
		[]string{"time", "host_id", "metric", "labels", "value"}, src)
	return err
}

type ProcessRow struct {
	Time     time.Time
	HostID   int64
	PID      int32
	Name     string
	User     string
	Cmdline  string
	CPUPct   float32
	MemRSS   int64
	Status   string
	NThreads int32
}

func InsertProcesses(ctx context.Context, pool *pgxpool.Pool, rows []ProcessRow) error {
	if len(rows) == 0 {
		return nil
	}
	src := pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
		r := rows[i]
		return []any{r.Time, r.HostID, r.PID, r.Name, r.User, r.Cmdline, r.CPUPct, r.MemRSS, r.Status, r.NThreads}, nil
	})
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"processes"},
		[]string{"time", "host_id", "pid", "name", "user_", "cmdline", "cpu_pct", "mem_rss", "status", "nthreads"}, src)
	return err
}

func InsertSnapshots(ctx context.Context, pool *pgxpool.Pool, procs []ProcessRow, conts []ContainerRow, ports []PortRow) error {
	if len(procs) == 0 && len(conts) == 0 && len(ports) == 0 {
		return nil
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if len(procs) > 0 {
		src := pgx.CopyFromSlice(len(procs), func(i int) ([]any, error) {
			r := procs[i]
			return []any{r.Time, r.HostID, r.PID, r.Name, r.User, r.Cmdline, r.CPUPct, r.MemRSS, r.Status, r.NThreads}, nil
		})
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"processes"},
			[]string{"time", "host_id", "pid", "name", "user_", "cmdline", "cpu_pct", "mem_rss", "status", "nthreads"}, src); err != nil {
			return err
		}
	}
	if len(conts) > 0 {
		src := pgx.CopyFromSlice(len(conts), func(i int) ([]any, error) {
			r := conts[i]
			return []any{r.Time, r.HostID, r.CID, r.Name, r.Image, r.State, r.CPUPct, r.MemUsed, r.MemLimit, r.RxBytes, r.TxBytes}, nil
		})
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"containers"},
			[]string{"time", "host_id", "cid", "name", "image", "state", "cpu_pct", "mem_used", "mem_limit", "rx_bytes", "tx_bytes"}, src); err != nil {
			return err
		}
	}
	if len(ports) > 0 {
		src := pgx.CopyFromSlice(len(ports), func(i int) ([]any, error) {
			r := ports[i]
			return []any{r.Time, r.HostID, r.Proto, r.Addr, r.Port, r.PID, r.Process}, nil
		})
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"ports"},
			[]string{"time", "host_id", "proto", "addr", "port", "pid", "process"}, src); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type PortRow struct {
	Time    time.Time
	HostID  int64
	Proto   string
	Addr    string
	Port    int32
	PID     *int32
	Process *string
}

type ContainerRow struct {
	Time     time.Time
	HostID   int64
	CID      string
	Name     string
	Image    string
	State    string
	CPUPct   float32
	MemUsed  int64
	MemLimit int64
	RxBytes  int64
	TxBytes  int64
}

func InsertContainers(ctx context.Context, pool *pgxpool.Pool, rows []ContainerRow) error {
	if len(rows) == 0 {
		return nil
	}
	src := pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
		r := rows[i]
		return []any{r.Time, r.HostID, r.CID, r.Name, r.Image, r.State, r.CPUPct, r.MemUsed, r.MemLimit, r.RxBytes, r.TxBytes}, nil
	})
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"containers"},
		[]string{"time", "host_id", "cid", "name", "image", "state", "cpu_pct", "mem_used", "mem_limit", "rx_bytes", "tx_bytes"}, src)
	return err
}

func ConvertBatch(hostID int64, batch *wire.Batch) ([]Row, []ProcessRow, []ContainerRow, []PortRow) {
	points := make([]Row, 0, len(batch.Points))
	now := time.Now()
	for _, p := range batch.Points {
		t := p.Time
		if t.IsZero() {
			t = now
		}
		labels, _ := json.Marshal(p.Labels)
		points = append(points, Row{
			Time:   t,
			HostID: hostID,
			Metric: int16(p.Metric),
			Labels: labels,
			Value:  p.Value,
		})
	}
	procs := make([]ProcessRow, 0, len(batch.Processes))
	for _, p := range batch.Processes {
		t := p.Time
		if t.IsZero() {
			t = now
		}
		procs = append(procs, ProcessRow{
			Time: t, HostID: hostID, PID: p.PID, Name: p.Name,
			User: p.User, Cmdline: p.Cmdline, CPUPct: p.CPUPct,
			MemRSS: p.MemRSS, Status: p.Status, NThreads: p.NThreads,
		})
	}
	conts := make([]ContainerRow, 0, len(batch.Containers))
	for _, c := range batch.Containers {
		t := c.Time
		if t.IsZero() {
			t = now
		}
		conts = append(conts, ContainerRow{
			Time: t, HostID: hostID, CID: c.ID, Name: c.Name, Image: c.Image,
			State: c.State, CPUPct: c.CPUPct, MemUsed: c.MemUsed,
			MemLimit: c.MemLimit, RxBytes: c.RxBytes, TxBytes: c.TxBytes,
		})
	}
	ports := make([]PortRow, 0, len(batch.Ports))
	for _, p := range batch.Ports {
		t := p.Time
		if t.IsZero() {
			t = now
		}
		row := PortRow{
			Time:   t,
			HostID: hostID,
			Proto:  p.Proto,
			Addr:   p.Addr,
			Port:   int32(p.Port),
		}
		if p.PID > 0 {
			pid := p.PID
			row.PID = &pid
		}
		if p.Process != "" {
			name := p.Process
			row.Process = &name
		}
		ports = append(ports, row)
	}
	return points, procs, conts, ports
}
