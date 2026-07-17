package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/version"
	"servermonitor/pkg/wire"
)

const (
	backupStateUnknown       = "unknown"
	backupStateOK            = "ok"
	backupStateNotConfigured = "not_configured"
	backupStateScheduled     = "scheduled"
	backupStateError         = "error"
	backupStateStale         = "stale"
	backupStateStaleAgent    = "stale_agent"
	backupStateAgentPerms    = "agent_perms"
	backupStatusStaleAfter   = 26 * time.Hour
	backupSnapshotLimit      = 50
	backupInventoryInterval  = 15 * time.Minute
	backupProgressLiveWindow = 45 * time.Second
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
	lastRunningRepo          string
	queryNextRun             func(ctx context.Context) *time.Time
	scheduledRepos           []string
	privPath                 string
	privFingerprint          backupStatusFingerprint
	privVersion              string
	privVersionOK            bool
	execVersion              func(ctx context.Context, path string) (string, error)
	accessExecutable         func(path string) error
}

type backupStatusFingerprint struct {
	mtime time.Time
	size  int64
}

var defaultBackupCollector = newBackupCollector(defaultBackupStatusPath())

func init() { Register(defaultBackupCollector) }

func newBackupCollector(path string) *backupCollector {
	return &backupCollector{
		path:             path,
		now:              time.Now,
		queryNextRun:     nextBackupRun,
		privPath:         defaultPrivilegedAgentPath(),
		execVersion:      execAgentVersion,
		accessExecutable: defaultExecutableAccess(),
	}
}

func defaultPrivilegedAgentPath() string {
	switch runtime.GOOS {
	case "linux":
		return "/usr/local/bin/sm-agent"
	case "windows":
		if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
			return filepath.Join(programFiles, "ServerMonitor", "sm-agent.exe")
		}
		return `C:\Program Files\ServerMonitor\sm-agent.exe`
	default:
		return ""
	}
}

func execAgentVersion(ctx context.Context, path string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := run(cmdCtx, path, "--version")
	return string(out), err
}

func parseAgentVersion(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, "sm-agent ") {
		return "", false
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(value, "sm-agent ")))
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

func parseSystemdNextElapse(raw string) *time.Time {
	s := strings.TrimSpace(raw)
	if s == "" || s == "n/a" || s == "0" || s == "infinity" {
		return nil
	}
	if strings.HasPrefix(s, "@") {
		v, err := strconv.ParseInt(strings.TrimPrefix(s, "@"), 10, 64)
		if err != nil || v <= 0 {
			return nil
		}
		t := time.Unix(v, 0).UTC()
		return &t
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		if v <= 0 {
			return nil
		}
		t := time.UnixMicro(v).UTC()
		return &t
	}
	t, err := time.ParseInLocation("Mon 2006-01-02 15:04:05 MST", s, time.Local)
	if err != nil {
		return nil
	}
	return &t
}

func parseWindowsNextRun(raw string) *time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func nextBackupRun(ctx context.Context) *time.Time {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "linux":
		out, err := run(cmdCtx, "systemctl", "show", "sm-backup.timer", "--property=NextElapseUSecRealtime", "--value", "--timestamp=unix")
		if err != nil {
			out, err = run(cmdCtx, "systemctl", "show", "sm-backup.timer", "--property=NextElapseUSecRealtime", "--value")
		}
		if err != nil {
			return nil
		}
		return parseSystemdNextElapse(string(out))
	case "windows":
		out, err := run(cmdCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-ScheduledTaskInfo -TaskName 'ServerMonitor Backup').NextRunTime.ToUniversalTime().ToString('o')")
		if err != nil {
			return nil
		}
		return parseWindowsNextRun(string(out))
	default:
		return nil
	}
}

func SetBackupStatusPath(path string) {
	defaultBackupCollector.setPath(path)
}

func SetBackupNextRunSource(fn func(ctx context.Context) *time.Time) {
	if fn == nil {
		return
	}
	defaultBackupCollector.mu.Lock()
	defaultBackupCollector.queryNextRun = fn
	defaultBackupCollector.mu.Unlock()
}

func SetBackupScheduledRepos(names []string) {
	defaultBackupCollector.mu.Lock()
	defaultBackupCollector.scheduledRepos = append([]string(nil), names...)
	defaultBackupCollector.mu.Unlock()
}

func (c *backupCollector) scheduledReposCopy() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.scheduledRepos...)
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
	if state == backupStateAgentPerms {
		c.stateMsg = strings.TrimSpace(msg)
	} else {
		c.stateMsg = shortBackupMessage(msg)
	}
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
	now := c.currentTime()
	out := c.backupProgressPoints(now)
	staleAgent, staleKnown, unexecutable, executableKnown, agentHealthMsg := c.privilegedAgentDrift(ctx)
	if staleKnown {
		value := 0.0
		if staleAgent {
			value = 1
		}
		out = append(out, point(now, metrics.BackupAgentStale, nil, value))
	}
	if executableKnown {
		value := 0.0
		if unexecutable {
			value = 1
		}
		out = append(out, point(now, metrics.BackupAgentUnexecutable, nil, value))
	}
	info, status, ok := c.readBackupStatus()
	if !ok {
		if c.Status().State != backupStateError && len(c.scheduledReposCopy()) > 0 {
			c.setState(backupStateScheduled, "")
		}
		state := c.Status().State
		if unexecutable && (state == backupStateNotConfigured || state == backupStateScheduled) {
			c.setState(backupStateAgentPerms, agentHealthMsg)
		} else if staleAgent && (state == backupStateNotConfigured || state == backupStateScheduled) {
			c.setState(backupStateStaleAgent, agentHealthMsg)
		}
		return out, nil
	}
	out = append(out, backupPoints(now, status.Repos)...)
	if age := now.Sub(info.ModTime()); age > backupStatusStaleAfter {
		c.setState(backupStateStale, "status file age "+age.Truncate(time.Second).String())
	} else {
		c.setState(backupStateOK, "")
	}
	if unexecutable && c.Status().State == backupStateOK {
		c.setState(backupStateAgentPerms, agentHealthMsg)
	} else if staleAgent && c.Status().State == backupStateOK {
		c.setState(backupStateStaleAgent, agentHealthMsg)
	}
	return out, nil
}

func (c *backupCollector) privilegedAgentDrift(ctx context.Context) (stale bool, staleKnown bool, unexecutable bool, executableKnown bool, msg string) {
	if c.privPath == "" {
		return false, false, false, false, ""
	}
	info, err := os.Stat(c.privPath)
	if err != nil {
		return false, false, false, false, ""
	}
	residentPath, err := os.Executable()
	if err != nil {
		return false, false, false, false, ""
	}
	resolvedResidentPath, err := filepath.EvalSymlinks(residentPath)
	if err != nil {
		resolvedResidentPath, err = filepath.Abs(residentPath)
		if err != nil {
			return false, false, false, false, ""
		}
	}
	privilegedPath, err := filepath.EvalSymlinks(c.privPath)
	if err != nil {
		privilegedPath, err = filepath.Abs(c.privPath)
		if err != nil {
			return false, false, false, false, ""
		}
	}
	if resolvedResidentPath == privilegedPath {
		return false, false, false, false, ""
	}
	if c.accessExecutable != nil {
		executableKnown = true
		if err := c.accessExecutable(c.privPath); err != nil {
			return false, false, true, true, fmt.Sprintf("backup agent copy at %s is not executable by the agent service account; scheduled backups cannot run until it is repaired (chown root:root, chmod 0755, or re-run the installer)", c.privPath)
		}
	}
	fingerprint := backupStatusFingerprint{mtime: info.ModTime(), size: info.Size()}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.privFingerprint != fingerprint {
		raw, execErr := c.execVersion(ctx, c.privPath)
		parsed, ok := parseAgentVersion(raw)
		c.privFingerprint = fingerprint
		c.privVersion = parsed
		c.privVersionOK = execErr == nil && ok
	}
	if !c.privVersionOK {
		return false, false, false, executableKnown, ""
	}
	stale = version.IsNewer(version.Version, c.privVersion)
	msg = fmt.Sprintf("backup agent copy v%s is older than the resident agent v%s", c.privVersion, version.Version)
	return stale, true, false, executableKnown, msg
}

type backupProgressFile struct {
	Version    int     `json:"version"`
	Running    bool    `json:"running"`
	Repo       string  `json:"repo"`
	StartedAt  int64   `json:"started_at"`
	UpdatedAt  int64   `json:"updated_at"`
	Percent    float64 `json:"percent"`
	BytesDone  int64   `json:"bytes_done"`
	TotalBytes int64   `json:"total_bytes"`
}

func (c *backupCollector) backupProgressPoints(now time.Time) []wire.Point {
	path := filepath.Join(filepath.Dir(c.currentPath()), "backup-progress.json")
	data, err := os.ReadFile(path)
	var progress backupProgressFile
	live := err == nil && json.Unmarshal(data, &progress) == nil && progress.Version == 1 && progress.Running && strings.TrimSpace(progress.Repo) != "" && now.Unix()-progress.UpdatedAt < int64(backupProgressLiveWindow/time.Second)
	previous := c.swapRunningRepo(progress.Repo, live)
	out := make([]wire.Point, 0, 6)
	if previous != "" && (!live || previous != progress.Repo) {
		out = append(out, point(now, metrics.BackupRunning, map[string]string{"repo": previous}, 0))
	}
	if !live {
		return out
	}
	labels := map[string]string{"repo": progress.Repo}
	elapsed := now.Unix() - progress.StartedAt
	if elapsed < 0 {
		elapsed = 0
	}
	out = append(out,
		point(now, metrics.BackupRunning, labels, 1),
		point(now, metrics.BackupRunElapsedS, labels, float64(elapsed)),
		point(now, metrics.BackupProgressPct, labels, progress.Percent),
		point(now, metrics.BackupProgressBytes, labels, float64(progress.BytesDone)),
		point(now, metrics.BackupProgressTotalBytes, labels, float64(progress.TotalBytes)),
	)
	return out
}

func (c *backupCollector) swapRunningRepo(repo string, live bool) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.lastRunningRepo
	if live {
		c.lastRunningRepo = repo
	} else {
		c.lastRunningRepo = ""
	}
	return previous
}

func (c *backupCollector) CollectBackups(ctx context.Context) ([]wire.BackupRepoStatus, error) {
	info, status, ok := c.readBackupStatus()
	if !ok {
		repos := c.scheduledReposCopy()
		if len(repos) == 0 {
			return nil, nil
		}
		next := c.queryNextRun(ctx)
		out := make([]wire.BackupRepoStatus, 0, len(repos))
		for _, name := range repos {
			out = append(out, wire.BackupRepoStatus{Name: name, Engine: "restic", NextRun: next})
		}
		return out, nil
	}
	now := c.currentTime()
	fingerprint := backupStatusFingerprint{mtime: info.ModTime(), size: info.Size()}
	if !c.shouldAttachInventory(now, fingerprint) {
		return nil, nil
	}
	next := c.queryNextRun(ctx)
	out := make([]wire.BackupRepoStatus, 0, len(status.Repos))
	for _, repo := range status.Repos {
		rs := backupRepoStatus(repo)
		rs.NextRun = next
		out = append(out, rs)
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
	Paths         []string              `json:"paths,omitempty"`
	Excludes      []string              `json:"excludes,omitempty"`
	OneFileSystem *bool                 `json:"one_file_system,omitempty"`
	PathStats     []wire.BackupPathStat `json:"path_stats,omitempty"`
	StatsSnapshot string                `json:"stats_snapshot,omitempty"`
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
		Name:          repo.Name,
		Engine:        repo.Engine,
		LastStarted:   repo.LastStarted,
		LastFinished:  repo.LastFinished,
		LastSuccess:   repo.LastSuccess,
		Error:         repo.Error,
		CheckLast:     repo.CheckLast,
		CheckSuccess:  repo.CheckSuccess,
		Tunnel:        repo.Tunnel,
		Paths:         repo.Paths,
		Excludes:      repo.Excludes,
		OneFileSystem: repo.OneFileSystem,
		PathStats:     repo.PathStats,
		StatsSnapshot: repo.StatsSnapshot,
		Snapshots:     repo.Snapshots,
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
