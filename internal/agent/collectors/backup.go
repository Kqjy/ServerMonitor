package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

const (
	backupStateUnknown       = "unknown"
	backupStateOK            = "ok"
	backupStateNotConfigured = "not_configured"
	backupStateError         = "error"
	backupStateStale         = "stale"
	backupStatusStaleAfter   = 26 * time.Hour
	backupSnapshotLimit      = 50
	backupInventoryInterval  = 15 * time.Minute
)

type backupCollector struct {
	mu                       sync.Mutex
	path                     string
	now                      func() time.Time
	state                    string
	stateMsg                 string
	lastInventoryFingerprint backupStatusFingerprint
	lastInventoryAttached    time.Time
	lastStatusFingerprint    backupStatusFingerprint
	lastStatus               backupStatusFile
	lastStatusOK             bool
}

type backupStatusFingerprint struct {
	mtime time.Time
	size  int64
}

var defaultBackupCollector = newBackupCollector(defaultBackupStatusPath())

func init() { Register(defaultBackupCollector) }

func newBackupCollector(path string) *backupCollector {
	return &backupCollector{path: path, now: time.Now}
}

func SetBackupStatusPath(path string) {
	defaultBackupCollector.setPath(path)
}

func defaultBackupStatusPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ServerMonitor", "backup-status.json")
		}
		return `C:\ProgramData\ServerMonitor\backup-status.json`
	}
	return "/var/lib/servermonitor/backup-status.json"
}

func (c *backupCollector) Name() string        { return "backup" }
func (c *backupCollector) Platforms() []string { return []string{"all"} }

func (c *backupCollector) setPath(path string) {
	if path == "" {
		path = defaultBackupStatusPath()
	}
	c.mu.Lock()
	c.path = path
	c.mu.Unlock()
}

func (c *backupCollector) Status() wire.CollectorStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state
	if state == "" {
		state = backupStateUnknown
	}
	return wire.CollectorStatus{State: state, Message: c.stateMsg}
}

func (c *backupCollector) setState(state, msg string) {
	c.mu.Lock()
	c.state = state
	c.stateMsg = shortBackupMessage(msg)
	c.mu.Unlock()
}

func (c *backupCollector) currentPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path
}

func (c *backupCollector) currentTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c *backupCollector) Collect(ctx context.Context) ([]wire.Point, error) {
	info, status, ok := c.readBackupStatus()
	if !ok {
		return nil, nil
	}
	now := c.currentTime()
	out := backupPoints(now, status.Repos)
	if age := now.Sub(info.ModTime()); age > backupStatusStaleAfter {
		c.setState(backupStateStale, "status file age "+age.Truncate(time.Second).String())
	} else {
		c.setState(backupStateOK, "")
	}
	return out, nil
}

func (c *backupCollector) CollectBackups(ctx context.Context) ([]wire.BackupRepoStatus, error) {
	info, status, ok := c.readBackupStatus()
	if !ok {
		return nil, nil
	}
	now := c.currentTime()
	fingerprint := backupStatusFingerprint{mtime: info.ModTime(), size: info.Size()}
	if !c.shouldAttachInventory(now, fingerprint) {
		return nil, nil
	}
	out := make([]wire.BackupRepoStatus, 0, len(status.Repos))
	for _, repo := range status.Repos {
		out = append(out, backupRepoStatus(repo))
	}
	return out, nil
}

func (c *backupCollector) readBackupStatus() (os.FileInfo, backupStatusFile, bool) {
	path := c.currentPath()
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.setState(backupStateNotConfigured, "")
			c.clearLastStatus()
			return nil, backupStatusFile{}, false
		}
		c.setState(backupStateError, "read status file: "+err.Error())
		c.clearLastStatus()
		return nil, backupStatusFile{}, false
	}
	fingerprint := backupStatusFingerprint{mtime: info.ModTime(), size: info.Size()}
	if status, ok := c.cachedBackupStatus(fingerprint); ok {
		return info, status, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		c.setState(backupStateError, "read status file: "+err.Error())
		c.clearLastStatus()
		return nil, backupStatusFile{}, false
	}
	status, err := parseBackupStatus(data)
	if err != nil {
		c.setState(backupStateError, err.Error())
		c.clearLastStatus()
		return nil, backupStatusFile{}, false
	}
	c.setLastStatus(fingerprint, status)
	return info, status, true
}

func (c *backupCollector) cachedBackupStatus(fingerprint backupStatusFingerprint) (backupStatusFile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastStatusOK && c.lastStatusFingerprint == fingerprint {
		return c.lastStatus, true
	}
	return backupStatusFile{}, false
}

func (c *backupCollector) setLastStatus(fingerprint backupStatusFingerprint, status backupStatusFile) {
	c.mu.Lock()
	c.lastStatusFingerprint = fingerprint
	c.lastStatus = status
	c.lastStatusOK = true
	c.mu.Unlock()
}

func (c *backupCollector) clearLastStatus() {
	c.mu.Lock()
	c.lastStatus = backupStatusFile{}
	c.lastStatusOK = false
	c.mu.Unlock()
}

func (c *backupCollector) shouldAttachInventory(now time.Time, fingerprint backupStatusFingerprint) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	attach := c.lastInventoryAttached.IsZero() ||
		c.lastInventoryFingerprint != fingerprint ||
		!now.Before(c.lastInventoryAttached.Add(backupInventoryInterval))
	if attach {
		c.lastInventoryFingerprint = fingerprint
		c.lastInventoryAttached = now
	}
	return attach
}

type backupStatusFile struct {
	Version int              `json:"version"`
	Repos   []backupRepoFile `json:"repos"`
}

type backupRepoFile struct {
	Name          string                `json:"name"`
	Engine        string                `json:"engine"`
	LastStarted   *time.Time            `json:"last_started"`
	LastFinished  *time.Time            `json:"last_finished"`
	LastSuccess   *time.Time            `json:"last_success"`
	Success       *bool                 `json:"success"`
	Error         string                `json:"error"`
	DurationS     *int64                `json:"duration_s"`
	AddedBytes    *int64                `json:"added_bytes"`
	TotalBytes    *int64                `json:"total_bytes"`
	SnapshotCount *int64                `json:"snapshot_count"`
	CheckLast     *time.Time            `json:"check_last"`
	CheckSuccess  *bool                 `json:"check_success"`
	Tunnel        bool                  `json:"tunnel"`
	Snapshots     []wire.BackupSnapshot `json:"snapshots"`
}

func parseBackupStatus(data []byte) (backupStatusFile, error) {
	var status backupStatusFile
	if err := json.Unmarshal(data, &status); err != nil {
		return status, fmt.Errorf("parse status file: %w", err)
	}
	if status.Version != 1 {
		return status, fmt.Errorf("parse status file: unsupported version %d", status.Version)
	}
	for i := range status.Repos {
		status.Repos[i].Name = strings.TrimSpace(status.Repos[i].Name)
		if status.Repos[i].Name == "" {
			return status, fmt.Errorf("parse status file: repo name is required")
		}
		if len(status.Repos[i].Snapshots) > backupSnapshotLimit {
			status.Repos[i].Snapshots = status.Repos[i].Snapshots[:backupSnapshotLimit]
		}
	}
	return status, nil
}

func backupPoints(now time.Time, repos []backupRepoFile) []wire.Point {
	out := make([]wire.Point, 0, len(repos)*8)
	for _, repo := range repos {
		labels := map[string]string{"repo": repo.Name}
		if repo.LastSuccess != nil {
			out = append(out, point(now, metrics.BackupLastSuccessAgeS, labels, now.Sub(*repo.LastSuccess).Seconds()))
		}
		if repo.Success != nil {
			v := 0.0
			if *repo.Success {
				v = 1
			}
			out = append(out, point(now, metrics.BackupLastRunOK, labels, v))
		}
		if repo.DurationS != nil {
			out = append(out, point(now, metrics.BackupDurationS, labels, float64(*repo.DurationS)))
		}
		if repo.AddedBytes != nil {
			out = append(out, point(now, metrics.BackupAddedBytes, labels, float64(*repo.AddedBytes)))
		}
		if repo.TotalBytes != nil {
			out = append(out, point(now, metrics.BackupTotalBytes, labels, float64(*repo.TotalBytes)))
		}
		if repo.SnapshotCount != nil {
			out = append(out, point(now, metrics.BackupSnapshotCount, labels, float64(*repo.SnapshotCount)))
		}
		if repo.CheckLast != nil {
			out = append(out, point(now, metrics.BackupCheckAgeS, labels, now.Sub(*repo.CheckLast).Seconds()))
		}
		if repo.CheckSuccess != nil {
			v := 0.0
			if *repo.CheckSuccess {
				v = 1
			}
			out = append(out, point(now, metrics.BackupCheckOK, labels, v))
		}
	}
	return out
}

func backupRepoStatus(repo backupRepoFile) wire.BackupRepoStatus {
	status := wire.BackupRepoStatus{
		Name:         repo.Name,
		Engine:       repo.Engine,
		LastStarted:  repo.LastStarted,
		LastFinished: repo.LastFinished,
		LastSuccess:  repo.LastSuccess,
		Error:        repo.Error,
		CheckLast:    repo.CheckLast,
		CheckSuccess: repo.CheckSuccess,
		Tunnel:       repo.Tunnel,
		Snapshots:    repo.Snapshots,
	}
	if repo.Success != nil {
		status.Success = *repo.Success
	}
	if repo.DurationS != nil {
		status.DurationS = *repo.DurationS
	}
	if repo.AddedBytes != nil {
		status.AddedBytes = *repo.AddedBytes
	}
	if repo.TotalBytes != nil {
		status.TotalBytes = *repo.TotalBytes
	}
	if repo.SnapshotCount != nil {
		status.SnapshotCount = *repo.SnapshotCount
	}
	return status
}

func shortBackupMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) <= 180 {
		return msg
	}
	return msg[:180]
}
