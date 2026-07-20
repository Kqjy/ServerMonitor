package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const statusVersion = 1

const progressVersion = 1

type StatusFile struct {
	Version int          `json:"version"`
	Repos   []RepoStatus `json:"repos"`
}

type ProgressFile struct {
	Version    int     `json:"version"`
	Running    bool    `json:"running"`
	Repo       string  `json:"repo"`
	ReposDone  int     `json:"repos_done"`
	ReposTotal int     `json:"repos_total"`
	StartedAt  int64   `json:"started_at"`
	UpdatedAt  int64   `json:"updated_at"`
	Percent    float64 `json:"percent"`
	BytesDone  int64   `json:"bytes_done"`
	TotalBytes int64   `json:"total_bytes"`
}

type RepoStatus struct {
	Name          string     `json:"name"`
	Engine        string     `json:"engine"`
	LastStarted   *time.Time `json:"last_started,omitempty"`
	LastFinished  *time.Time `json:"last_finished,omitempty"`
	LastSuccess   *time.Time `json:"last_success,omitempty"`
	Success       bool       `json:"success"`
	Error         string     `json:"error"`
	DurationS     int64      `json:"duration_s,omitempty"`
	AddedBytes    int64      `json:"added_bytes,omitempty"`
	TotalBytes    int64      `json:"total_bytes,omitempty"`
	SnapshotCount int64      `json:"snapshot_count,omitempty"`
	CheckLast     *time.Time `json:"check_last,omitempty"`
	CheckSuccess  *bool      `json:"check_success,omitempty"`
	Tunnel        bool       `json:"tunnel,omitempty"`
	Paths         []string   `json:"paths,omitempty"`
	Excludes      []string   `json:"excludes,omitempty"`
	OneFileSystem *bool      `json:"one_file_system,omitempty"`
	PathStats     []PathStat `json:"path_stats,omitempty"`
	StatsSnapshot string     `json:"stats_snapshot,omitempty"`
	StatsAt       *time.Time `json:"stats_at,omitempty"`
	StatsScope    string     `json:"stats_scope,omitempty"`
	Snapshots     []Snapshot `json:"snapshots,omitempty"`
}

type PathStat struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Files int64  `json:"files"`
}

func readStatusFile(path string) (StatusFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StatusFile{Version: statusVersion}, nil
		}
		return StatusFile{}, err
	}
	var status StatusFile
	if err := json.Unmarshal(data, &status); err != nil {
		return StatusFile{}, err
	}
	if status.Version != statusVersion {
		return StatusFile{}, fmt.Errorf("unsupported status version %d", status.Version)
	}
	return status, nil
}

func writeStatusAtomic(path string, status StatusFile) error {
	status.Version = statusVersion
	return writeJSONAtomic(path, status)
}

func RecordTunnelEnrollmentFailure(configPath string, enrollmentErr error, now time.Time) error {
	raw, err := readRawConfig(configPath)
	if err != nil {
		return err
	}
	statusPath := filepath.Clean(raw.StatusPath)
	if raw.StatusPath == "" {
		statusPath = DefaultStatusPath()
	}
	existing, err := readStatusFile(statusPath)
	if err != nil {
		return fmt.Errorf("read backup status: %w", err)
	}
	finished := utcSecond(now)
	updates := []RepoStatus{}
	for _, repo := range raw.Repos {
		if repo.TunnelName == "" {
			continue
		}
		updates = append(updates, RepoStatus{
			Name:         repo.Name,
			Engine:       "restic",
			LastFinished: &finished,
			Success:      false,
			Error:        fmt.Sprintf("tunnel enrollment failed: %v", enrollmentErr),
			Tunnel:       true,
		})
	}
	if len(updates) == 0 {
		return nil
	}
	if err := writeStatusAtomic(statusPath, mergeStatus(existing, updates)); err != nil {
		return fmt.Errorf("write backup status: %w", err)
	}
	return nil
}

func progressPath(statusPath string) string {
	return filepath.Join(filepath.Dir(statusPath), "backup-progress.json")
}

func readProgressFile(path string) (ProgressFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProgressFile{}, err
	}
	var progress ProgressFile
	if err := json.Unmarshal(data, &progress); err != nil {
		return ProgressFile{}, err
	}
	if progress.Version != progressVersion {
		return ProgressFile{}, fmt.Errorf("unsupported progress version %d", progress.Version)
	}
	return progress, nil
}

func writeProgressAtomic(path string, progress ProgressFile) error {
	progress.Version = progressVersion
	return writeJSONAtomic(path, progress)
}

func writeJSONAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func mergeStatus(existing StatusFile, updates []RepoStatus) StatusFile {
	previous := make(map[string]RepoStatus, len(existing.Repos))
	for _, repo := range existing.Repos {
		previous[repo.Name] = repo
	}
	out := make([]RepoStatus, 0, len(existing.Repos)+len(updates))
	seen := map[string]bool{}
	for _, update := range updates {
		if prev, ok := previous[update.Name]; ok {
			update.CheckLast = prev.CheckLast
			update.CheckSuccess = prev.CheckSuccess
			if !update.Success {
				update.LastSuccess = prev.LastSuccess
				if update.SnapshotCount == 0 {
					update.SnapshotCount = prev.SnapshotCount
				}
				if update.TotalBytes == 0 {
					update.TotalBytes = prev.TotalBytes
				}
				if update.Snapshots == nil && prev.Snapshots != nil {
					update.Snapshots = append([]Snapshot(nil), prev.Snapshots...)
				}
			}
			if update.Paths == nil && prev.Paths != nil {
				update.Paths = append([]string(nil), prev.Paths...)
				update.Excludes = append([]string(nil), prev.Excludes...)
				if prev.OneFileSystem != nil {
					oneFileSystem := *prev.OneFileSystem
					update.OneFileSystem = &oneFileSystem
				}
			}
			if !update.Success && update.PathStats == nil && prev.PathStats != nil {
				update.PathStats = append([]PathStat(nil), prev.PathStats...)
				update.StatsSnapshot = prev.StatsSnapshot
				update.StatsAt = prev.StatsAt
				update.StatsScope = prev.StatsScope
			}
		}
		out = append(out, update)
		seen[update.Name] = true
	}
	for _, repo := range existing.Repos {
		if !seen[repo.Name] {
			out = append(out, repo)
		}
	}
	return StatusFile{Version: statusVersion, Repos: out}
}

func utcSecond(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}
