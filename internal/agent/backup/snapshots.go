package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"servermonitor/pkg/wire"
)

const snapshotInventoryLimit = 50

var browseSnapshotRE = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

type BrowseOptions struct {
	Repo       string
	Snapshot   string
	Path       string
	Recursive  bool
	MaxEntries int
}

type BrowseResult struct {
	Snapshot  string
	Entries   []wire.BackupBrowseEntry
	Truncated bool
}

type Snapshot struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time,omitempty"`
	Paths      []string  `json:"paths,omitempty"`
	SizeBytes  *int64    `json:"size_bytes,omitempty"`
	AddedBytes *int64    `json:"added_bytes,omitempty"`
	FileCount  *int64    `json:"file_count,omitempty"`
	DurationS  *float64  `json:"duration_s,omitempty"`
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

func BrowseFromConfigPath(ctx context.Context, configPath string, bo BrowseOptions, opts Options) (BrowseResult, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return BrowseResult{}, err
	}
	resticPath, err := ResolveResticPath(cfg)
	if err != nil {
		return BrowseResult{}, err
	}
	cfg.ResticPath = resticPath
	return browseSnapshot(ctx, cfg, bo, opts)
}

func browseSnapshot(ctx context.Context, cfg Config, bo BrowseOptions, opts Options) (BrowseResult, error) {
	cfg = normalizedConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return BrowseResult{}, err
	}
	if cfg.ResticPath == "" {
		return BrowseResult{}, fmt.Errorf("restic path is not resolved")
	}
	repo, err := validateBrowseOptions(cfg.Repos, bo)
	if err != nil {
		return BrowseResult{}, err
	}
	release, err := tunnelLockGuard(cfg, opts)
	if err != nil {
		return BrowseResult{}, err
	}
	defer release()
	cfg, session := PrepareTunnel(cfg, opts)
	if session != nil {
		defer session.Close()
	}
	for _, candidate := range cfg.Repos {
		if candidate.Name == repo.Name {
			repo = candidate
			break
		}
	}
	cacheDir := filepath.Join(filepath.Dir(cfg.StatusPath), "restic-browse-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return BrowseResult{}, fmt.Errorf("create restic browse cache dir: %w", err)
	}
	if err := os.Chmod(cacheDir, 0o700); err != nil {
		return BrowseResult{}, fmt.Errorf("secure restic browse cache dir: %w", err)
	}
	args := []string{"ls", "--json"}
	if bo.Recursive {
		args = append(args, "--recursive")
	}
	args = append(args, bo.Snapshot, bo.Path)
	result := BrowseResult{Snapshot: bo.Snapshot, Entries: []wire.BackupBrowseEntry{}}
	requestedPath := strings.TrimRight(bo.Path, "/")
	if requestedPath == "" {
		requestedPath = "/"
	}
	_, err = resticCommandStreamDiscard(ctx, cfg, repo, cacheDir, args, "snapshot browse", opts, func(line []byte) {
		var node struct {
			MessageType string    `json:"message_type"`
			ID          string    `json:"id"`
			Name        string    `json:"name"`
			Type        string    `json:"type"`
			Path        string    `json:"path"`
			Size        int64     `json:"size"`
			Mtime       time.Time `json:"mtime"`
		}
		if json.Unmarshal(line, &node) != nil {
			return
		}
		if node.MessageType == "snapshot" {
			if node.ID != "" {
				result.Snapshot = node.ID
			}
			return
		}
		if node.MessageType != "node" {
			return
		}
		if !bo.Recursive && node.Type == "dir" && node.Path == requestedPath {
			return
		}
		if len(result.Entries) >= bo.MaxEntries {
			result.Truncated = true
			return
		}
		entry := wire.BackupBrowseEntry{Name: node.Name, Type: node.Type}
		if node.Type == "file" {
			entry.Size = node.Size
		}
		if !node.Mtime.IsZero() {
			mtime := node.Mtime.UTC()
			entry.Mtime = &mtime
		}
		result.Entries = append(result.Entries, entry)
	})
	if err != nil {
		return BrowseResult{}, err
	}
	sort.SliceStable(result.Entries, func(i, j int) bool {
		iDir := result.Entries[i].Type == "dir"
		jDir := result.Entries[j].Type == "dir"
		if iDir != jDir {
			return iDir
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})
	return result, nil
}

func validateBrowseOptions(repos []Repo, bo BrowseOptions) (Repo, error) {
	var repo Repo
	for _, candidate := range repos {
		if candidate.Name == bo.Repo {
			repo = candidate
			break
		}
	}
	if repo.Name == "" {
		return Repo{}, fmt.Errorf("repo %q not found", bo.Repo)
	}
	if bo.Snapshot != "latest" && !browseSnapshotRE.MatchString(bo.Snapshot) {
		return Repo{}, fmt.Errorf("snapshot must be latest or 4 to 64 hexadecimal characters")
	}
	if len(bo.Path) == 0 || len(bo.Path) > 4096 || bo.Path[0] != '/' || strings.ContainsRune(bo.Path, 0) {
		return Repo{}, fmt.Errorf("path must be an absolute snapshot path of at most 4096 bytes")
	}
	for _, segment := range strings.Split(bo.Path, "/") {
		if segment == ".." {
			return Repo{}, fmt.Errorf("path must not contain a .. segment")
		}
	}
	if bo.MaxEntries <= 0 {
		return Repo{}, fmt.Errorf("max entries must be positive")
	}
	return repo, nil
}

func ClassifyBrowseError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	switch {
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(lower, "context deadline exceeded"):
		return "timed_out", "snapshot browse timed out after 5m; retrying is usually faster because the restic cache is already warm"
	case errors.Is(err, os.ErrPermission), strings.Contains(lower, "permission denied"), strings.Contains(lower, "access is denied"):
		return "insufficient_privilege", message
	case strings.Contains(lower, "holds the tunnel lock"):
		return "busy", message
	case strings.Contains(lower, "no matching id found"), strings.Contains(lower, "snapshot not found"), strings.Contains(lower, "path not found"), strings.Contains(lower, "no such file or directory"):
		return "not_found", message
	default:
		return "failed", message
	}
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
		commandResult, err := resticCommand(ctx, cfg, repo, cacheDir, snapshotsArgs(opts), opts)
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
		fmt.Fprintln(tw, "REPO\tID\tTIME\tSIZE\tFILES\tPATHS")
	} else {
		fmt.Fprintln(tw, "ID\tTIME\tSIZE\tFILES\tPATHS")
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
			size := ""
			if snapshot.SizeBytes != nil {
				size = strconv.FormatInt(*snapshot.SizeBytes, 10)
			}
			files := ""
			if snapshot.FileCount != nil {
				files = strconv.FormatInt(*snapshot.FileCount, 10)
			}
			paths := strings.Join(snapshot.Paths, ",")
			if multiRepo {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", repo.Name, snapshot.ID, when, size, files, paths)
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", snapshot.ID, when, size, files, paths)
			}
		}
	}
	return tw.Flush()
}

func ParseSnapshots(data []byte) ([]Snapshot, error) {
	var raw []struct {
		ID      string    `json:"id"`
		Time    time.Time `json:"time"`
		Paths   []string  `json:"paths"`
		Summary *struct {
			TotalBytesProcessed int64     `json:"total_bytes_processed"`
			DataAdded           int64     `json:"data_added"`
			TotalFilesProcessed int64     `json:"total_files_processed"`
			BackupStart         time.Time `json:"backup_start"`
			BackupEnd           time.Time `json:"backup_end"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(raw))
	for _, snapshot := range raw {
		parsed := Snapshot{
			ID:    truncateSnapshotID(snapshot.ID),
			Time:  snapshot.Time.UTC(),
			Paths: append([]string(nil), snapshot.Paths...),
		}
		if snapshot.Summary != nil {
			parsed.SizeBytes = &snapshot.Summary.TotalBytesProcessed
			parsed.AddedBytes = &snapshot.Summary.DataAdded
			parsed.FileCount = &snapshot.Summary.TotalFilesProcessed
			if !snapshot.Summary.BackupStart.IsZero() && !snapshot.Summary.BackupEnd.IsZero() && !snapshot.Summary.BackupEnd.Before(snapshot.Summary.BackupStart) {
				durationS := snapshot.Summary.BackupEnd.Sub(snapshot.Summary.BackupStart).Seconds()
				parsed.DurationS = &durationS
			}
		}
		out = append(out, parsed)
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
			ID:         truncateSnapshotID(snapshot.ID),
			Time:       snapshot.Time.UTC(),
			Paths:      append([]string(nil), snapshot.Paths...),
			SizeBytes:  snapshot.SizeBytes,
			AddedBytes: snapshot.AddedBytes,
			FileCount:  snapshot.FileCount,
			DurationS:  snapshot.DurationS,
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
