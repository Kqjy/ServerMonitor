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

type StatusFile struct {
	Version int          `json:"version"`
	Repos   []RepoStatus `json:"repos"`
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
	Snapshots     []Snapshot `json:"snapshots,omitempty"`
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(status); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "backup-status-*.json.tmp")
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
