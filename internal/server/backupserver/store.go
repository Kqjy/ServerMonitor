package backupserver

import (
	"context"
	"errors"
)

type StoreConfig struct {
	Dir string
	S3  S3Config
}

func NewStore(ctx context.Context, cfg StoreConfig) (Store, error) {
	if cfg.Dir != "" && cfg.S3.Bucket != "" {
		return nil, errors.New("backup store: set BACKUP_DIR or BACKUP_S3_BUCKET, not both")
	}
	if cfg.Dir != "" {
		return newDiskStore(cfg.Dir)
	}
	if cfg.S3.Bucket != "" {
		return newS3Store(ctx, cfg.S3)
	}
	return nil, errors.New("backup store: no backend configured")
}
