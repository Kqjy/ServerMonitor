package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const staleLockAfter = 24 * time.Hour

type RunResult struct {
	LockSkipped bool
	Repos       []RepoStatus
}

type backupSummary struct {
	DurationS  int64
	AddedBytes int64
	TotalBytes int64
}

func RunFromConfigPath(ctx context.Context, path string, opts Options) (RunResult, error) {
	cfg, err := Load(path)
	if err != nil {
		return RunResult{}, err
	}
	resticPath, err := ResolveResticPath(cfg)
	if err != nil {
		return RunResult{}, err
	}
	cfg.ResticPath = resticPath
	return Run(ctx, cfg, opts)
}

func Run(ctx context.Context, cfg Config, opts Options) (RunResult, error) {
	cfg = normalizedConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return RunResult{}, err
	}
	if cfg.ResticPath == "" {
		return RunResult{}, fmt.Errorf("restic path is not resolved")
	}
	logger := opts.logger()
	release, skipped, err := acquireRunLock(cfg.StatusPath, opts.now())
	if err != nil {
		return RunResult{}, err
	}
	if skipped {
		logger.Info("another backup run is in progress")
		return RunResult{LockSkipped: true}, nil
	}
	defer release()

	cfg, session := PrepareTunnel(cfg, opts)
	if session != nil {
		defer session.Close()
	}

	statusDir := filepath.Dir(cfg.StatusPath)
	cacheDir := filepath.Join(statusDir, "restic-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return RunResult{}, fmt.Errorf("create restic cache dir: %w", err)
	}

	existing, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		logger.Warn("could not read existing backup status; starting a fresh status file", "path", cfg.StatusPath, "err", err)
		existing = StatusFile{Version: statusVersion}
	}

	updates := make([]RepoStatus, 0, len(cfg.Repos))
	for _, repo := range cfg.Repos {
		if ctx.Err() != nil {
			break
		}
		updates = append(updates, runRepo(ctx, cfg, repo, cacheDir, opts))
	}
	if len(updates) > 0 {
		if err := writeStatusAtomic(cfg.StatusPath, mergeStatus(existing, updates)); err != nil {
			return RunResult{Repos: updates}, fmt.Errorf("write backup status: %w", err)
		}
	}
	return RunResult{Repos: updates}, nil
}

func (r RunResult) AnySucceeded() bool {
	if r.LockSkipped {
		return true
	}
	for _, repo := range r.Repos {
		if repo.Success {
			return true
		}
	}
	return false
}

func runRepo(ctx context.Context, cfg Config, repo Repo, cacheDir string, opts Options) RepoStatus {
	logger := opts.logger()
	start := utcSecond(opts.now())
	status := RepoStatus{
		Name:        repo.Name,
		Engine:      "restic",
		LastStarted: &start,
		Tunnel:      repo.UsesTunnel(),
	}
	logger.Info("backup repo started", "repo", repo.Name)

	summary, err := runBackupCommand(ctx, cfg, repo, cacheDir, opts)
	if err != nil {
		return finishRepoStatus(status, opts, err, logger)
	}
	status.DurationS = summary.DurationS
	status.AddedBytes = summary.AddedBytes
	status.TotalBytes = summary.TotalBytes

	if cfg.PruneMode == "host" {
		if err := runForgetCommand(ctx, cfg, repo, cacheDir, opts); err != nil {
			return finishRepoStatus(status, opts, err, logger)
		}
	}

	snapshots, err := runSnapshotsCommand(ctx, cfg, repo, cacheDir, opts)
	if err != nil {
		return finishRepoStatus(status, opts, err, logger)
	}
	status.SnapshotCount = int64(len(snapshots))
	status.Snapshots = snapshotInventory(snapshots)

	finish := utcSecond(opts.now())
	status.LastFinished = &finish
	status.LastSuccess = &finish
	status.Success = true
	logger.Info("backup repo completed", "repo", repo.Name)
	return status
}

func finishRepoStatus(status RepoStatus, opts Options, err error, logger *slog.Logger) RepoStatus {
	finish := utcSecond(opts.now())
	status.LastFinished = &finish
	status.Success = false
	status.Error = err.Error()
	logger.Error("backup repo failed", "repo", status.Name, "err", err)
	return status
}

func runBackupCommand(ctx context.Context, cfg Config, repo Repo, cacheDir string, opts Options) (backupSummary, error) {
	result, err := resticCommand(ctx, cfg, repo, cacheDir, backupArgs(cfg, opts.goos()), opts)
	if err != nil {
		return backupSummary{}, err
	}
	return parseBackupSummary(result.Stdout)
}

func runSnapshotsCommand(ctx context.Context, cfg Config, repo Repo, cacheDir string, opts Options) ([]Snapshot, error) {
	result, err := resticCommand(ctx, cfg, repo, cacheDir, []string{"snapshots", "--json"}, opts)
	if err != nil {
		return nil, err
	}
	return ParseSnapshots(result.Stdout)
}

func runForgetCommand(ctx context.Context, cfg Config, repo Repo, cacheDir string, opts Options) error {
	_, err := resticCommandAction(ctx, cfg, repo, cacheDir, forgetArgs(effectiveRetention(cfg.Retention, repo.Retention)), "retention prune", opts)
	return err
}

func backupArgs(cfg Config, goos string) []string {
	args := []string{"backup", "--json"}
	for _, exclude := range cfg.Excludes {
		if exclude != "" {
			args = append(args, "--exclude", exclude)
		}
	}
	if cfg.OneFileSystem && goos != "windows" {
		args = append(args, "--one-file-system")
	}
	if goos == "windows" {
		args = append(args, "--use-fs-snapshot")
	}
	args = append(args, cfg.Paths...)
	return args
}

func forgetArgs(retention Retention) []string {
	args := []string{"forget", "--prune"}
	if retention.Daily > 0 {
		args = append(args, "--keep-daily", strconv.Itoa(retention.Daily))
	}
	if retention.Weekly > 0 {
		args = append(args, "--keep-weekly", strconv.Itoa(retention.Weekly))
	}
	if retention.Monthly > 0 {
		args = append(args, "--keep-monthly", strconv.Itoa(retention.Monthly))
	}
	return args
}

func parseBackupSummary(data []byte) (backupSummary, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var summary *backupSummary
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg struct {
			MessageType         string  `json:"message_type"`
			TotalDuration       float64 `json:"total_duration"`
			DataAdded           int64   `json:"data_added"`
			TotalBytesProcessed int64   `json:"total_bytes_processed"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.MessageType == "summary" {
			summary = &backupSummary{
				DurationS:  int64(math.Round(msg.TotalDuration)),
				AddedBytes: msg.DataAdded,
				TotalBytes: msg.TotalBytesProcessed,
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return backupSummary{}, err
	}
	if summary == nil {
		return backupSummary{}, fmt.Errorf("summary record not found")
	}
	return *summary, nil
}

func acquireRunLock(statusPath string, now time.Time) (func(), bool, error) {
	dir := filepath.Dir(statusPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	lockPath := filepath.Join(dir, "backup.lock")
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if _, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid()); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, false, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, false, closeErr
			}
			return func() { _ = os.Remove(lockPath) }, false, nil
		}
		if !os.IsExist(err) {
			return nil, false, err
		}
		info, statErr := os.Stat(lockPath)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, false, statErr
		}
		if now.Sub(info.ModTime()) < staleLockAfter {
			return nil, true, nil
		}
		if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, false, removeErr
		}
	}
}
