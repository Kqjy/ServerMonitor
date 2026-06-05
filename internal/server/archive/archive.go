package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parquet-go/parquet-go"
)

type Config struct {
	Bucket       string
	Region       string
	Prefix       string
	Cutoff       time.Duration
	UsePathStyle bool
}

type Archiver struct {
	pool   *pgxpool.Pool
	cfg    Config
	logger *slog.Logger
	s3     *s3.Client
}

type Record struct {
	Bucket int64   `parquet:"bucket,timestamp(microsecond)"`
	HostID int64   `parquet:"host_id"`
	Metric int32   `parquet:"metric"`
	Labels string  `parquet:"labels,zstd"`
	Avg    float64 `parquet:"avg"`
	Min    float64 `parquet:"min"`
	Max    float64 `parquet:"max"`
	Last   float64 `parquet:"last"`
}

func New(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) (*Archiver, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("archive disabled (no bucket)")
	}
	if cfg.Cutoff == 0 {
		cfg.Cutoff = 180 * 24 * time.Hour
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "metrics"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	s3Opts := []func(*s3.Options){}
	if cfg.UsePathStyle {
		s3Opts = append(s3Opts, func(o *s3.Options) { o.UsePathStyle = true })
	}
	return &Archiver{
		pool:   pool,
		cfg:    cfg,
		logger: logger,
		s3:     s3.NewFromConfig(awsCfg, s3Opts...),
	}, nil
}

func (a *Archiver) RunOnce(ctx context.Context) error {
	hostIDs, err := a.hostIDs(ctx)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-a.cfg.Cutoff)
	a.logger.Info("archive run", "cutoff", cutoff.Format(time.RFC3339), "hosts", len(hostIDs))
	failed := 0
	for _, h := range hostIDs {
		if err := a.archiveHost(ctx, h, cutoff); err != nil {
			a.logger.Warn("archive host failed", "host", h, "err", err)
			failed++
		}
	}
	if failed > 0 {
		a.logger.Warn("archive run incomplete; skipping chunk drop", "failed", failed, "total", len(hostIDs))
		return fmt.Errorf("archive: %d of %d hosts failed", failed, len(hostIDs))
	}
	return a.dropArchivedChunks(ctx, cutoff)
}

func (a *Archiver) hostIDs(ctx context.Context) ([]int64, error) {
	rows, err := a.pool.Query(ctx, `SELECT id FROM hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (a *Archiver) archiveHost(ctx context.Context, hostID int64, cutoff time.Time) error {
	since, err := a.lastArchivedBucket(ctx, hostID)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "sm-archive-*.parquet")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	rows, err := a.pool.Query(ctx, `
		SELECT bucket, host_id, metric, labels::text, avg, min, max, last_val
		FROM metric_points_5m
		WHERE host_id = $1 AND bucket > $2 AND bucket < $3
		ORDER BY bucket ASC
	`, hostID, since, cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()

	writer := parquet.NewGenericWriter[Record](tmp, parquet.Compression(&parquet.Zstd))
	count := int64(0)
	const batchSize = 5000
	batch := make([]Record, 0, batchSize)
	bucketFrom := time.Time{}
	bucketTo := time.Time{}
	for rows.Next() {
		var (
			r        Record
			bucket   time.Time
			metricID int16
		)
		if err := rows.Scan(&bucket, &r.HostID, &metricID, &r.Labels, &r.Avg, &r.Min, &r.Max, &r.Last); err != nil {
			writer.Close()
			return err
		}
		if bucketFrom.IsZero() {
			bucketFrom = bucket
		}
		bucketTo = bucket
		r.Bucket = bucket.UnixMicro()
		r.Metric = int32(metricID)
		batch = append(batch, r)
		count++
		if len(batch) >= batchSize {
			if _, err := writer.Write(batch); err != nil {
				writer.Close()
				return err
			}
			batch = batch[:0]
		}
	}
	if err := rows.Err(); err != nil {
		writer.Close()
		return err
	}
	if len(batch) > 0 {
		if _, err := writer.Write(batch); err != nil {
			writer.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}
	fi, err := tmp.Stat()
	if err != nil {
		return err
	}
	key := s3Key(a.cfg.Prefix, bucketFrom, bucketTo, hostID)
	if err := a.upload(ctx, key, tmp, fi.Size()); err != nil {
		return err
	}

	_, err = a.pool.Exec(ctx, `
		INSERT INTO archive_manifests (kind, bucket_from, bucket_to, host_id, s3_key, row_count, byte_size)
		VALUES ('metric_points_5m', $1, $2, $3, $4, $5, $6)
	`, bucketFrom, bucketTo, hostID, key, count, fi.Size())
	if err != nil {
		return err
	}
	a.logger.Info("archived",
		"host", hostID, "rows", count, "bytes", fi.Size(), "key", key)
	return nil
}

func (a *Archiver) lastArchivedBucket(ctx context.Context, hostID int64) (time.Time, error) {
	var t *time.Time
	err := a.pool.QueryRow(ctx, `
		SELECT MAX(bucket_to) FROM archive_manifests
		WHERE host_id = $1 AND kind = 'metric_points_5m'
	`, hostID).Scan(&t)
	if err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

func (a *Archiver) upload(ctx context.Context, key string, body *os.File, size int64) error {
	_, err := a.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(a.cfg.Bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: size,
		ContentType:   aws.String("application/x-parquet"),
	})
	return err
}

func (a *Archiver) dropArchivedChunks(ctx context.Context, cutoff time.Time) error {
	_, err := a.pool.Exec(ctx, fmt.Sprintf(`SELECT drop_chunks('metric_points_5m', older_than => TIMESTAMPTZ '%s')`, cutoff.Format(time.RFC3339)))
	return err
}

func s3Key(prefix string, bucketFrom, bucketTo time.Time, hostID int64) string {
	return filepath.ToSlash(filepath.Join(prefix,
		fmt.Sprintf("year=%d", bucketFrom.Year()),
		fmt.Sprintf("month=%02d", bucketFrom.Month()),
		fmt.Sprintf("host=%d", hostID),
		fmt.Sprintf("%d-%d.parquet", bucketFrom.UnixMicro(), bucketTo.UnixMicro())))
}

func (a *Archiver) Read(ctx context.Context, hostID int64, metric int16, from, to time.Time, labelSel map[string]string) ([]Record, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT s3_key FROM archive_manifests
		WHERE host_id = $1 AND bucket_from < $3 AND bucket_to >= $2
		ORDER BY bucket_from ASC
	`, hostID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	out := []Record{}
	for _, key := range keys {
		recs, err := a.readKey(ctx, key, hostID, metric, from, to, labelSel)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

type Interval struct {
	From time.Time
	To   time.Time
}

func (a *Archiver) CoverageIntervals(ctx context.Context, hostID int64, from, to time.Time) ([]Interval, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT bucket_from, bucket_to FROM archive_manifests
		WHERE host_id = $1 AND bucket_from < $3 AND bucket_to >= $2
		ORDER BY bucket_from ASC
	`, hostID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interval
	for rows.Next() {
		var iv Interval
		if err := rows.Scan(&iv.From, &iv.To); err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

func (a *Archiver) readKey(ctx context.Context, key string, hostID int64, metric int16, from, to time.Time, labelSel map[string]string) ([]Record, error) {
	resp, err := a.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(a.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := bytes.NewBuffer(nil)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	reader := parquet.NewGenericReader[Record](bytes.NewReader(buf.Bytes()))
	defer reader.Close()
	out := []Record{}
	for {
		batch := make([]Record, 1024)
		n, err := reader.Read(batch)
		for i := 0; i < n; i++ {
			r := batch[i]
			t := time.UnixMicro(r.Bucket)
			if r.HostID != hostID || int16(r.Metric) != metric {
				continue
			}
			if t.Before(from) || !t.Before(to) {
				continue
			}
			if !labelsMatch(r.Labels, labelSel) {
				continue
			}
			out = append(out, r)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parquet read %s: %w", key, err)
		}
	}
	return out, nil
}

func labelsMatch(rawLabels string, sel map[string]string) bool {
	if len(sel) == 0 {
		return true
	}
	if rawLabels == "" {
		return false
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(rawLabels), &got); err != nil {
		return false
	}
	for k, v := range sel {
		if got[k] != v {
			return false
		}
	}
	return true
}

var _ = pgx.ErrNoRows
