package backupsched

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const reconcileInterval = time.Minute

type Config struct {
	BackupTime   string
	BackupJitter time.Duration
	CheckWeekday time.Weekday
	CheckTime    string
	CheckJitter  time.Duration
}

type Options struct {
	StatePath string
	RunBackup func(context.Context) error
	RunCheck  func(context.Context) error
	Now       func() time.Time
	Jitter    func(time.Duration) time.Duration
	Logger    *slog.Logger
}

type Scheduler struct {
	cfg       Config
	statePath string
	runBackup func(context.Context) error
	runCheck  func(context.Context) error
	now       func() time.Time
	jitter    func(time.Duration) time.Duration
	logger    *slog.Logger

	mu         sync.Mutex
	nextBackup time.Time
	nextCheck  time.Time

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func New(cfg Config, opts Options) *Scheduler {
	s := &Scheduler{
		cfg:       cfg,
		statePath: opts.StatePath,
		runBackup: opts.RunBackup,
		runCheck:  opts.RunCheck,
		now:       opts.Now,
		jitter:    opts.Jitter,
		logger:    opts.Logger,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.jitter == nil {
		s.jitter = randomJitter
	}
	if s.logger == nil {
		s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max)))
}

type scheduleState struct {
	Version           int   `json:"version"`
	BackupLastTrigger int64 `json:"backup_last_trigger"`
	CheckLastTrigger  int64 `json:"check_last_trigger"`
}

func (s *Scheduler) Run(ctx context.Context) {
	defer close(s.done)

	bh, bm, err := parseHHMM(s.cfg.BackupTime)
	if err != nil {
		s.logger.Error("backup schedule disabled: invalid backup time", "value", s.cfg.BackupTime, "err", err)
		return
	}
	ch, cm, err := parseHHMM(s.cfg.CheckTime)
	if err != nil {
		s.logger.Error("backup schedule disabled: invalid check time", "value", s.cfg.CheckTime, "err", err)
		return
	}

	st, existed := s.loadState()
	now := s.now()
	if !existed {
		st = scheduleState{Version: 1, BackupLastTrigger: now.Unix(), CheckLastTrigger: now.Unix()}
		s.saveState(st)
	}

	s.setNextBackup(s.arm(now, st.BackupLastTrigger,
		func(t time.Time) time.Time { return mostRecentDaily(t, bh, bm) },
		func(t time.Time) time.Time { return nextDaily(t, bh, bm) }, s.cfg.BackupJitter))
	s.setNextCheck(s.arm(now, st.CheckLastTrigger,
		func(t time.Time) time.Time { return mostRecentWeekly(t, s.cfg.CheckWeekday, ch, cm) },
		func(t time.Time) time.Time { return nextWeekly(t, s.cfg.CheckWeekday, ch, cm) }, s.cfg.CheckJitter))

	s.logger.Info("backup scheduler started", "next_backup", s.NextBackupRun(), "next_check", s.nextCheckRun())

	for {
		timer := time.NewTimer(s.sleepFor())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.stop:
			timer.Stop()
			return
		case <-timer.C:
		}

		now := s.now()
		if next := s.NextBackupRun(); next != nil && !now.Before(*next) {
			st.BackupLastTrigger = mostRecentDaily(now, bh, bm).Unix()
			s.saveState(st)
			s.setNextBackup(nextDaily(now, bh, bm).Add(s.jitter(s.cfg.BackupJitter)))
			s.runJob(ctx, "backup", s.runBackup)
		}
		if next := s.nextCheckRun(); next != nil && !now.Before(*next) {
			st.CheckLastTrigger = mostRecentWeekly(now, s.cfg.CheckWeekday, ch, cm).Unix()
			s.saveState(st)
			s.setNextCheck(nextWeekly(now, s.cfg.CheckWeekday, ch, cm).Add(s.jitter(s.cfg.CheckJitter)))
			s.runJob(ctx, "check", s.runCheck)
		}
	}
}

func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Scheduler) Wait() {
	<-s.done
}

func (s *Scheduler) runJob(ctx context.Context, name string, fn func(context.Context) error) {
	if fn == nil {
		return
	}
	if err := fn(ctx); err != nil {
		s.logger.Error("scheduled backup job failed", "job", name, "err", err)
	}
}

func (s *Scheduler) arm(now time.Time, lastTrigger int64, recent, next func(time.Time) time.Time, jitterMax time.Duration) time.Time {
	occ := recent(now)
	if lastTrigger < occ.Unix() {
		return now.Add(s.jitter(jitterMax))
	}
	return next(now).Add(s.jitter(jitterMax))
}

func (s *Scheduler) sleepFor() time.Duration {
	soonest := s.soonest()
	if soonest.IsZero() {
		return reconcileInterval
	}
	d := soonest.Sub(s.now())
	if d < 0 {
		d = 0
	}
	if d > reconcileInterval {
		d = reconcileInterval
	}
	return d
}

func (s *Scheduler) soonest() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.nextBackup.IsZero():
		return s.nextCheck
	case s.nextCheck.IsZero():
		return s.nextBackup
	case s.nextBackup.Before(s.nextCheck):
		return s.nextBackup
	default:
		return s.nextCheck
	}
}

func (s *Scheduler) setNextBackup(t time.Time) {
	s.mu.Lock()
	s.nextBackup = t
	s.mu.Unlock()
}

func (s *Scheduler) setNextCheck(t time.Time) {
	s.mu.Lock()
	s.nextCheck = t
	s.mu.Unlock()
}

func (s *Scheduler) NextBackupRun() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextBackup.IsZero() {
		return nil
	}
	t := s.nextBackup
	return &t
}

func (s *Scheduler) nextCheckRun() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextCheck.IsZero() {
		return nil
	}
	t := s.nextCheck
	return &t
}

func (s *Scheduler) loadState() (scheduleState, bool) {
	if s.statePath == "" {
		return scheduleState{Version: 1}, false
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return scheduleState{Version: 1}, false
	}
	var st scheduleState
	if err := json.Unmarshal(data, &st); err != nil {
		s.logger.Warn("backup schedule state is unreadable; treating as fresh", "path", s.statePath, "err", err)
		return scheduleState{Version: 1}, false
	}
	return st, true
}

func (s *Scheduler) saveState(st scheduleState) {
	if s.statePath == "" {
		return
	}
	st.Version = 1
	data, err := json.Marshal(st)
	if err != nil {
		s.logger.Warn("could not encode backup schedule state", "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o755); err != nil {
		s.logger.Warn("could not create backup schedule state dir", "err", err)
		return
	}
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		s.logger.Warn("could not write backup schedule state", "err", err)
		return
	}
	if err := os.Rename(tmp, s.statePath); err != nil {
		s.logger.Warn("could not persist backup schedule state", "err", err)
		_ = os.Remove(tmp)
	}
}

func parseHHMM(s string) (int, int, error) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("time %q must be HH:MM", s)
	}
	hh, err := strconv.Atoi(parts[0])
	if err != nil || hh < 0 || hh > 23 {
		return 0, 0, fmt.Errorf("hour in %q must be 00-23", s)
	}
	mm, err := strconv.Atoi(parts[1])
	if err != nil || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("minute in %q must be 00-59", s)
	}
	return hh, mm, nil
}

func mostRecentDaily(now time.Time, hh, mm int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

func nextDaily(now time.Time, hh, mm int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func mostRecentWeekly(now time.Time, wd time.Weekday, hh, mm int) time.Time {
	daysSince := (int(now.Weekday()) - int(wd) + 7) % 7
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location()).AddDate(0, 0, -daysSince)
	if t.After(now) {
		t = t.AddDate(0, 0, -7)
	}
	return t
}

func nextWeekly(now time.Time, wd time.Weekday, hh, mm int) time.Time {
	daysUntil := (int(wd) - int(now.Weekday()) + 7) % 7
	t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location()).AddDate(0, 0, daysUntil)
	if !t.After(now) {
		t = t.AddDate(0, 0, 7)
	}
	return t
}
