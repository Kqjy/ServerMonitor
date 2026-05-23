package tasks

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-co-op/gocron/v2"

	"servermonitor/internal/server/archive"
)

type ArchiveScheduler struct {
	scheduler gocron.Scheduler
	archiver  *archive.Archiver
	logger    *slog.Logger
}

func NewArchiveScheduler(a *archive.Archiver, logger *slog.Logger) (*ArchiveScheduler, error) {
	s, err := gocron.NewScheduler(gocron.WithLocation(time.Local))
	if err != nil {
		return nil, err
	}
	return &ArchiveScheduler{scheduler: s, archiver: a, logger: logger}, nil
}

func (s *ArchiveScheduler) Start(ctx context.Context) error {
	if s.archiver == nil {
		return nil
	}
	_, err := s.scheduler.NewJob(
		gocron.CronJob("0 3 * * *", false),
		gocron.NewTask(func() {
			runCtx, cancel := context.WithTimeout(ctx, 1*time.Hour)
			defer cancel()
			if err := s.archiver.RunOnce(runCtx); err != nil {
				s.logger.Warn("archive job", "err", err)
			}
		}),
		gocron.WithName("archive-parquet-s3"),
	)
	if err != nil {
		return err
	}
	s.scheduler.Start()
	return nil
}

func (s *ArchiveScheduler) Stop() {
	_ = s.scheduler.Shutdown()
}
