package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
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

type backupProgress struct {
	mu   sync.Mutex
	file ProgressFile
}

func (p *backupProgress) snapshot(now time.Time) ProgressFile {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.file
	out.UpdatedAt = now.UTC().Unix()
	return out
}

func (p *backupProgress) startRepo(repo string, done int) {
	p.mu.Lock()
	p.file.Repo = repo
	p.file.ReposDone = done
	p.file.Percent = 0
	p.file.BytesDone = 0
	p.file.TotalBytes = 0
	p.mu.Unlock()
}

func (p *backupProgress) finishRepo(done int) {
	p.mu.Lock()
	p.file.ReposDone = done
	p.mu.Unlock()
}

func (p *backupProgress) update(update backupProgressUpdate) {
	p.mu.Lock()
	p.file.Percent = update.Percent
	p.file.BytesDone = update.BytesDone
	p.file.TotalBytes = update.TotalBytes
	p.mu.Unlock()
}

type backupProgressUpdate struct {
	Percent    float64
	BytesDone  int64
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
	if err := validateHostRoot(opts.HostRoot); err != nil {
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
	progressFilePath := progressPath(cfg.StatusPath)
	started := opts.now()
	progress := &backupProgress{file: ProgressFile{
		Version:    progressVersion,
		Running:    true,
		ReposTotal: len(cfg.Repos),
		StartedAt:  started.Unix(),
		UpdatedAt:  started.Unix(),
	}}
	if err := writeProgressAtomic(progressFilePath, progress.snapshot(started)); err != nil {
		return RunResult{}, fmt.Errorf("write backup progress: %w", err)
	}
	defer os.Remove(progressFilePath)
	heartbeatDone := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	go func() {
		defer close(heartbeatStopped)
		ticker := time.NewTicker(opts.progressInterval())
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case now := <-ticker.C:
				if err := writeProgressAtomic(progressFilePath, progress.snapshot(now)); err != nil {
					logger.Warn("could not update backup progress", "path", progressFilePath, "err", err)
				}
			}
		}
	}()
	var stopHeartbeatOnce sync.Once
	stopHeartbeat := func() {
		stopHeartbeatOnce.Do(func() {
			close(heartbeatDone)
			<-heartbeatStopped
		})
	}
	defer stopHeartbeat()

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
	for i, repo := range cfg.Repos {
		if ctx.Err() != nil {
			break
		}
		progress.startRepo(repo.Name, i)
		if err := writeProgressAtomic(progressFilePath, progress.snapshot(opts.now())); err != nil {
			return RunResult{Repos: updates}, fmt.Errorf("write backup progress: %w", err)
		}
		updates = append(updates, runRepo(ctx, cfg, repo, cacheDir, opts, progress))
		progress.finishRepo(i + 1)
	}
	stopHeartbeat()
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

func runRepo(ctx context.Context, cfg Config, repo Repo, cacheDir string, opts Options, progress *backupProgress) RepoStatus {
	logger := opts.logger()
	start := utcSecond(opts.now())
	status := RepoStatus{
		Name:        repo.Name,
		Engine:      "restic",
		LastStarted: &start,
		Tunnel:      repo.UsesTunnel(),
	}
	logger.Info("backup repo started", "repo", repo.Name)

	summary, err := runBackupCommand(ctx, cfg, repo, cacheDir, opts, progress)
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

func runBackupCommand(ctx context.Context, cfg Config, repo Repo, cacheDir string, opts Options, progress *backupProgress) (backupSummary, error) {
	result, err := resticCommandStream(ctx, cfg, repo, cacheDir, backupArgs(cfg, opts.goos()), "backup", opts.HostRoot, opts, func(line []byte) {
		if update, ok := parseBackupProgressLine(line); ok && progress != nil {
			progress.update(update)
		}
	})
	if err != nil {
		return backupSummary{}, err
	}
	return parseBackupSummary(result.Stdout)
}

func parseBackupProgressLine(line []byte) (backupProgressUpdate, bool) {
	var message struct {
		MessageType string  `json:"message_type"`
		PercentDone float64 `json:"percent_done"`
		BytesDone   int64   `json:"bytes_done"`
		TotalBytes  int64   `json:"total_bytes"`
	}
	if err := json.Unmarshal(line, &message); err != nil || message.MessageType != "status" {
		return backupProgressUpdate{}, false
	}
	return backupProgressUpdate{
		Percent:    math.Max(0, math.Min(100, message.PercentDone*100)),
		BytesDone:  message.BytesDone,
		TotalBytes: message.TotalBytes,
	}, true
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
	token, err := newRunLockToken()
	if err != nil {
		return nil, false, err
	}
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if _, writeErr := file.Write(token); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, false, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, false, closeErr
			}
			return func() { releaseRunLock(lockPath, token) }, false, nil
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
		owner, readErr := os.ReadFile(lockPath)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, false, readErr
		}
		if isVersionedLockToken(owner) {
			alive, aliveErr := runLockOwnerAlive(owner)
			if aliveErr != nil {
				return nil, false, aliveErr
			}
			if alive {
				return nil, true, nil
			}
		} else {
			if now.Sub(info.ModTime()) < staleLockAfter {
				return nil, true, nil
			}
			alive, aliveErr := runLockOwnerAlive(owner)
			if aliveErr != nil {
				return nil, false, aliveErr
			}
			if alive {
				return nil, true, nil
			}
		}
		current, readErr := os.ReadFile(lockPath)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, false, readErr
		}
		if !bytes.Equal(current, owner) {
			continue
		}
		reclaimNonce := make([]byte, 8)
		if _, err := rand.Read(reclaimNonce); err != nil {
			return nil, false, err
		}
		reclaimPath := lockPath + ".reclaim." + hex.EncodeToString(reclaimNonce)
		if err := os.Rename(lockPath, reclaimPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, false, err
		}
		grabbed, grabErr := os.ReadFile(reclaimPath)
		if grabErr != nil {
			_ = os.Remove(reclaimPath)
			return nil, false, grabErr
		}
		if bytes.Equal(grabbed, owner) {
			if removeErr := os.Remove(reclaimPath); removeErr != nil && !os.IsNotExist(removeErr) {
				return nil, false, removeErr
			}
			continue
		}
		if err := renameNoReplace(reclaimPath, lockPath); err != nil {
			_ = os.Remove(reclaimPath)
		}
		return nil, true, nil
	}
}

func renameNoReplace(oldpath, newpath string) error {
	if err := os.Link(oldpath, newpath); err != nil {
		return err
	}
	return os.Remove(oldpath)
}

const runLockVersion = "v2"

func newRunLockToken() ([]byte, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("%s %d %d %s\n", runLockVersion, os.Getpid(), selfStartTime(), hex.EncodeToString(nonce))), nil
}

func selfStartTime() int64 {
	p, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return 0
	}
	ct, err := p.CreateTime()
	if err != nil {
		return 0
	}
	return ct
}

func isVersionedLockToken(token []byte) bool {
	fields := strings.Fields(string(token))
	return len(fields) > 0 && fields[0] == runLockVersion
}

func runLockOwnerAlive(token []byte) (bool, error) {
	fields := strings.Fields(string(token))
	if len(fields) == 0 {
		return false, errors.New("backup lock has no owner")
	}
	if fields[0] == runLockVersion {
		if len(fields) < 3 {
			return false, errors.New("backup lock token is malformed")
		}
		pid, err := strconv.ParseInt(fields[1], 10, 32)
		if err != nil || pid <= 0 {
			return false, errors.New("backup lock has an invalid owner")
		}
		start, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return false, errors.New("backup lock has an invalid owner start time")
		}
		return pidAliveWithStart(int32(pid), start)
	}
	pid, err := strconv.ParseInt(fields[0], 10, 32)
	if err != nil || pid <= 0 {
		return false, errors.New("backup lock has an invalid owner")
	}
	return process.PidExists(int32(pid))
}

func pidAliveWithStart(pid int32, start int64) (bool, error) {
	exists, err := process.PidExists(pid)
	if err != nil || !exists {
		return false, err
	}
	if start == 0 {
		return true, nil
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return false, nil
	}
	ct, err := p.CreateTime()
	if err != nil {
		return true, nil
	}
	return ct == start, nil
}

func releaseRunLock(lockPath string, token []byte) {
	current, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(current, token) {
		return
	}
	_ = os.Remove(lockPath)
}
