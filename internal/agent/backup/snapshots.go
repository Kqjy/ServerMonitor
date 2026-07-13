package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

const snapshotInventoryLimit = 50

type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time,omitempty"`
	Paths []string  `json:"paths,omitempty"`
}

type SnapshotRepoResult struct {
	Name      string     `json:"name"`
	Raw       []byte     `json:"-"`
	Snapshots []Snapshot `json:"snapshots,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type SnapshotListResult struct {
	Repos []SnapshotRepoResult `json:"repos"`
}

func SnapshotsFromConfigPath(ctx context.Context, path, repoName string, opts Options) (SnapshotListResult, error) {
	cfg, err := Load(path)
	if err != nil {
		return SnapshotListResult{}, err
	}
	resticPath, err := ResolveResticPath(cfg)
	if err != nil {
		return SnapshotListResult{}, err
	}
	cfg.ResticPath = resticPath
	return ListSnapshots(ctx, cfg, repoName, opts)
}

func ListSnapshots(ctx context.Context, cfg Config, repoName string, opts Options) (SnapshotListResult, error) {
	cfg = normalizedConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return SnapshotListResult{}, err
	}
	if cfg.ResticPath == "" {
		return SnapshotListResult{}, fmt.Errorf("restic path is not resolved")
	}
	release, err := tunnelLockGuard(cfg, opts)
	if err != nil {
		return SnapshotListResult{}, err
	}
	defer release()
	cfg, session := PrepareTunnel(cfg, opts)
	if session != nil {
		defer session.Close()
	}
	targets, err := snapshotTargets(cfg.Repos, repoName)
	if err != nil {
		return SnapshotListResult{}, err
	}
	cacheDir := filepath.Join(filepath.Dir(cfg.StatusPath), "restic-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return SnapshotListResult{}, fmt.Errorf("create restic cache dir: %w", err)
	}
	result := SnapshotListResult{Repos: make([]SnapshotRepoResult, 0, len(targets))}
	for _, repo := range targets {
		commandResult, err := resticCommand(ctx, cfg, repo, cacheDir, []string{"snapshots", "--json"}, opts)
		entry := SnapshotRepoResult{Name: repo.Name, Raw: commandResult.Stdout}
		if err != nil {
			entry.Error = err.Error()
			result.Repos = append(result.Repos, entry)
			continue
		}
		snapshots, err := ParseSnapshots(commandResult.Stdout)
		if err != nil {
			entry.Error = fmt.Sprintf("parse snapshots failed: %v", err)
			result.Repos = append(result.Repos, entry)
			continue
		}
		entry.Snapshots = snapshots
		result.Repos = append(result.Repos, entry)
	}
	return result, nil
}

func (r SnapshotListResult) Failed() bool {
	for _, repo := range r.Repos {
		if repo.Error != "" {
			return true
		}
	}
	return false
}

func (r SnapshotListResult) WriteJSON(w io.Writer, rawSingleRepo bool) error {
	if rawSingleRepo && len(r.Repos) == 1 {
		_, err := w.Write(r.Repos[0].Raw)
		if err != nil {
			return err
		}
		if len(r.Repos[0].Raw) == 0 || r.Repos[0].Raw[len(r.Repos[0].Raw)-1] != '\n' {
			_, err = io.WriteString(w, "\n")
		}
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func (r SnapshotListResult) WriteTable(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	multiRepo := len(r.Repos) != 1
	if multiRepo {
		fmt.Fprintln(tw, "REPO\tID\tTIME\tPATHS")
	} else {
		fmt.Fprintln(tw, "ID\tTIME\tPATHS")
	}
	for _, repo := range r.Repos {
		if repo.Error != "" {
			continue
		}
		for _, snapshot := range repo.Snapshots {
			when := ""
			if !snapshot.Time.IsZero() {
				when = snapshot.Time.UTC().Format(time.RFC3339)
			}
			paths := strings.Join(snapshot.Paths, ",")
			if multiRepo {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", repo.Name, snapshot.ID, when, paths)
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", snapshot.ID, when, paths)
			}
		}
	}
	return tw.Flush()
}

func ParseSnapshots(data []byte) ([]Snapshot, error) {
	var raw []struct {
		ID    string    `json:"id"`
		Time  time.Time `json:"time"`
		Paths []string  `json:"paths"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(raw))
	for _, snapshot := range raw {
		out = append(out, Snapshot{
			ID:    truncateSnapshotID(snapshot.ID),
			Time:  snapshot.Time.UTC(),
			Paths: append([]string(nil), snapshot.Paths...),
		})
	}
	return out, nil
}

func snapshotInventory(snapshots []Snapshot) []Snapshot {
	start := 0
	if len(snapshots) > snapshotInventoryLimit {
		start = len(snapshots) - snapshotInventoryLimit
	}
	out := make([]Snapshot, 0, len(snapshots)-start)
	for _, snapshot := range snapshots[start:] {
		out = append(out, Snapshot{
			ID:    truncateSnapshotID(snapshot.ID),
			Time:  snapshot.Time.UTC(),
			Paths: append([]string(nil), snapshot.Paths...),
		})
	}
	return out
}

func truncateSnapshotID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func snapshotTargets(repos []Repo, repoName string) ([]Repo, error) {
	repoName = strings.TrimSpace(repoName)
	if repoName == "" {
		return repos, nil
	}
	for _, repo := range repos {
		if repo.Name == repoName {
			return []Repo{repo}, nil
		}
	}
	return nil, fmt.Errorf("repo %q not found", repoName)
}
