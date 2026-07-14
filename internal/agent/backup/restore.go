package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RestoreOptions struct {
	Repo     string
	Snapshot string
	Includes []string
	Target   string
	InPlace  bool
	Instance string
}

type RestoreResult struct {
	LockSkipped bool
	Target      string
	InPlace     bool
	Summary     *restoreSummary
}

type restoreSummary struct {
	FilesRestored int64
	FilesSkipped  int64
	TotalFiles    int64
	BytesRestored int64
	TotalBytes    int64
}

func RestoreFromConfigPath(ctx context.Context, path string, ro RestoreOptions, opts Options) (RestoreResult, error) {
	cfg, err := Load(path)
	if err != nil {
		return RestoreResult{}, err
	}
	resticPath, err := ResolveResticPath(cfg)
	if err != nil {
		return RestoreResult{}, err
	}
	cfg.ResticPath = resticPath
	return Restore(ctx, cfg, ro, opts)
}

func Restore(ctx context.Context, cfg Config, ro RestoreOptions, opts Options) (RestoreResult, error) {
	cfg = normalizedConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return RestoreResult{}, err
	}
	if err := validateHostRoot(opts.HostRoot); err != nil {
		return RestoreResult{}, err
	}
	if cfg.ResticPath == "" {
		return RestoreResult{}, fmt.Errorf("restic path is not resolved")
	}
	logger := opts.logger()
	release, skipped, err := acquireRunLock(cfg.StatusPath, opts.now())
	if err != nil {
		return RestoreResult{}, err
	}
	if skipped {
		logger.Info("another backup operation is in progress; restore skipped")
		return RestoreResult{LockSkipped: true}, nil
	}
	defer release()

	ro, err = resolveRestoreOptions(ro)
	if err != nil {
		return RestoreResult{}, err
	}
	repo, err := findRepo(cfg.Repos, ro.Repo)
	if err != nil {
		return RestoreResult{}, err
	}

	statusDir := filepath.Dir(cfg.StatusPath)
	var target string
	if ro.InPlace {
		target, err = inPlaceTarget(opts.goos(), opts.Containerized)
		if err != nil {
			return RestoreResult{}, err
		}
	} else {
		restoreRoot := filepath.Join(statusDir, "restore")
		target, err = containedRestoreTarget(restoreRoot, ro.Snapshot, ro.Target)
		if err != nil {
			return RestoreResult{}, err
		}
		if err := ensureEmptyTarget(target); err != nil {
			return RestoreResult{}, err
		}
	}

	cfg, session := PrepareTunnel(cfg, opts)
	if session != nil {
		defer session.Close()
	}
	repo, err = findRepo(cfg.Repos, ro.Repo)
	if err != nil {
		return RestoreResult{}, err
	}

	if !ro.InPlace {
		if err := ensureEmptyTarget(target); err != nil {
			return RestoreResult{}, err
		}
		if err := os.MkdirAll(target, 0o700); err != nil {
			return RestoreResult{}, fmt.Errorf("create restore target: %w", err)
		}
	}

	cacheDir := filepath.Join(statusDir, "restic-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return RestoreResult{}, fmt.Errorf("create restic cache dir: %w", err)
	}

	logger.Info("restore started", "repo", repo.Name, "snapshot", ro.Snapshot, "in_place", ro.InPlace)
	result, err := resticCommand(ctx, cfg, repo, cacheDir, restoreArgs(ro.Snapshot, target, ro.Includes), opts)
	if err != nil {
		return RestoreResult{}, err
	}
	out := RestoreResult{Target: target, InPlace: ro.InPlace}
	if summary, ok := parseRestoreSummary(result.Stdout); ok {
		out.Summary = &summary
	}
	logger.Info("restore completed", "repo", repo.Name, "snapshot", ro.Snapshot, "target", target)
	return out, nil
}

func resolveRestoreOptions(ro RestoreOptions) (RestoreOptions, error) {
	if strings.TrimSpace(ro.Instance) != "" {
		if strings.TrimSpace(ro.Repo) != "" || strings.TrimSpace(ro.Snapshot) != "" || len(ro.Includes) > 0 {
			return RestoreOptions{}, fmt.Errorf("--instance cannot be combined with --repo, --snapshot or --include")
		}
		if !ro.InPlace {
			return RestoreOptions{}, fmt.Errorf("--instance requires --in-place")
		}
		repo, snapshot, includes, err := parseInstance(ro.Instance)
		if err != nil {
			return RestoreOptions{}, err
		}
		ro.Repo = repo
		ro.Snapshot = snapshot
		ro.Includes = includes
	}
	ro.Repo = strings.TrimSpace(ro.Repo)
	ro.Snapshot = strings.TrimSpace(ro.Snapshot)
	if ro.Repo == "" {
		return RestoreOptions{}, fmt.Errorf("--repo is required")
	}
	if ro.Snapshot == "" {
		return RestoreOptions{}, fmt.Errorf("--snapshot is required")
	}
	if ro.InPlace && strings.TrimSpace(ro.Target) != "" {
		return RestoreOptions{}, fmt.Errorf("--in-place and --target are mutually exclusive")
	}
	return ro, nil
}

func parseInstance(instance string) (string, string, []string, error) {
	parts := strings.SplitN(instance, ":", 3)
	if len(parts) < 2 {
		return "", "", nil, fmt.Errorf("--instance must be repo:snapshot[:include,include]")
	}
	repo := strings.TrimSpace(parts[0])
	snapshot := strings.TrimSpace(parts[1])
	if repo == "" || snapshot == "" {
		return "", "", nil, fmt.Errorf("--instance must be repo:snapshot[:include,include]")
	}
	var includes []string
	if len(parts) == 3 {
		for _, item := range strings.Split(parts[2], ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				includes = append(includes, item)
			}
		}
	}
	return repo, snapshot, includes, nil
}

func findRepo(repos []Repo, name string) (Repo, error) {
	for _, repo := range repos {
		if repo.Name == name {
			return repo, nil
		}
	}
	return Repo{}, fmt.Errorf("repo %q not found", name)
}

func inPlaceTarget(goos string, containerized bool) (string, error) {
	if goos == "windows" {
		return "", fmt.Errorf("in-place restore is not supported on Windows: restic restores drive letters as subdirectories of the target; restore to a staging directory and copy files into place")
	}
	if containerized {
		return "", fmt.Errorf("in-place restore is disabled in a containerized agent: restoring to / would target the container filesystem, not the host. Restore to the staging directory (omit --in-place), then copy files onto the host, e.g. with docker cp")
	}
	return "/", nil
}

func restoreArgs(snapshot, target string, includes []string) []string {
	args := []string{"restore", snapshot, "--json", "--target", target}
	for _, include := range includes {
		include = strings.TrimSpace(include)
		if include != "" {
			args = append(args, "--include", include)
		}
	}
	return args
}

func containedRestoreTarget(restoreRoot, snapshot, target string) (string, error) {
	root := filepath.Clean(restoreRoot)
	target = strings.TrimSpace(target)
	var candidate string
	if target == "" {
		if strings.ContainsAny(snapshot, `/\`) {
			return "", fmt.Errorf("snapshot id %q is not a valid directory name", snapshot)
		}
		candidate = filepath.Join(root, snapshot)
	} else if filepath.IsAbs(target) {
		candidate = filepath.Clean(target)
	} else {
		candidate = filepath.Clean(filepath.Join(root, target))
	}
	resolvedRoot, err := evalDeepestAncestor(root)
	if err != nil {
		return "", fmt.Errorf("resolve restore root: %w", err)
	}
	resolvedTarget, err := evalDeepestAncestor(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve restore target: %w", err)
	}
	if !withinRoot(resolvedRoot, resolvedTarget) {
		return "", fmt.Errorf("restore target %q escapes the restore root %q", candidate, root)
	}
	return candidate, nil
}

func withinRoot(root, target string) bool {
	if target == root {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func evalDeepestAncestor(path string) (string, error) {
	path = filepath.Clean(path)
	tail := ""
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if tail == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, tail), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Join(current, tail), nil
		}
		tail = filepath.Join(filepath.Base(current), tail)
		current = parent
	}
}

func ensureEmptyTarget(target string) error {
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("restore target %q already exists and is not a directory; pass --in-place or a fresh --target", target)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("restore target %q is not empty; pass --in-place or a fresh --target", target)
	}
	return nil
}

func parseRestoreSummary(data []byte) (restoreSummary, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var summary *restoreSummary
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg struct {
			MessageType   string `json:"message_type"`
			FilesRestored int64  `json:"files_restored"`
			FilesSkipped  int64  `json:"files_skipped"`
			TotalFiles    int64  `json:"total_files"`
			BytesRestored int64  `json:"bytes_restored"`
			TotalBytes    int64  `json:"total_bytes"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.MessageType == "summary" {
			summary = &restoreSummary{
				FilesRestored: msg.FilesRestored,
				FilesSkipped:  msg.FilesSkipped,
				TotalFiles:    msg.TotalFiles,
				BytesRestored: msg.BytesRestored,
				TotalBytes:    msg.TotalBytes,
			}
		}
	}
	if summary == nil {
		return restoreSummary{}, false
	}
	return *summary, true
}
