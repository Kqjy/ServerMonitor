package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const drillSampleLimit = 5
const drillSizeLimit = 4 * 1024 * 1024

type CheckOptions struct {
	Repo           string
	ReadDataSubset string
	Drill          bool
}

type CheckRepoResult struct {
	Name    string
	Success bool
	Error   string
}

type CheckResult struct {
	LockSkipped bool
	Repos       []CheckRepoResult
}

func (r CheckResult) AllSucceeded() bool {
	if r.LockSkipped {
		return true
	}
	for _, repo := range r.Repos {
		if !repo.Success {
			return false
		}
	}
	return true
}

func CheckFromConfigPath(ctx context.Context, path string, co CheckOptions, opts Options) (CheckResult, error) {
	cfg, err := Load(path)
	if err != nil {
		return CheckResult{}, err
	}
	resticPath, err := ResolveResticPath(cfg)
	if err != nil {
		return CheckResult{}, err
	}
	cfg.ResticPath = resticPath
	return Check(ctx, cfg, co, opts)
}

func Check(ctx context.Context, cfg Config, co CheckOptions, opts Options) (CheckResult, error) {
	cfg = normalizedConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return CheckResult{}, err
	}
	if err := validateHostRoot(opts.HostRoot); err != nil {
		return CheckResult{}, err
	}
	if cfg.ResticPath == "" {
		return CheckResult{}, fmt.Errorf("restic path is not resolved")
	}
	if err := validateReadDataSubset(co.ReadDataSubset); err != nil {
		return CheckResult{}, err
	}
	targets, err := snapshotTargets(cfg.Repos, co.Repo)
	if err != nil {
		return CheckResult{}, err
	}

	logger := opts.logger()
	release, skipped, err := acquireRunLock(cfg.StatusPath, opts.now())
	if err != nil {
		return CheckResult{}, err
	}
	if skipped {
		logger.Info("another backup operation is in progress; check skipped")
		return CheckResult{LockSkipped: true}, nil
	}
	defer release()

	cfg, session := PrepareTunnel(cfg, opts)
	if session != nil {
		defer session.Close()
	}
	targets, err = snapshotTargets(cfg.Repos, co.Repo)
	if err != nil {
		return CheckResult{}, err
	}

	statusDir := filepath.Dir(cfg.StatusPath)
	cacheDir := filepath.Join(statusDir, "restic-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return CheckResult{}, fmt.Errorf("create restic cache dir: %w", err)
	}

	status, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		logger.Warn("could not read existing backup status; check will create a fresh file", "path", cfg.StatusPath, "err", err)
		status = StatusFile{Version: statusVersion}
	}

	out := CheckResult{Repos: make([]CheckRepoResult, 0, len(targets))}
	for _, repo := range targets {
		if ctx.Err() != nil {
			break
		}
		entry := checkRepo(ctx, cfg, repo, cacheDir, co, opts)
		out.Repos = append(out.Repos, entry)
		status = applyCheckResult(status, repo.Name, utcSecond(opts.now()), entry.Success, repo.UsesTunnel())
		if err := writeStatusAtomic(cfg.StatusPath, status); err != nil {
			return out, fmt.Errorf("write backup status: %w", err)
		}
	}
	return out, nil
}

func checkRepo(ctx context.Context, cfg Config, repo Repo, cacheDir string, co CheckOptions, opts Options) CheckRepoResult {
	logger := opts.logger()
	logger.Info("backup check started", "repo", repo.Name)
	entry := CheckRepoResult{Name: repo.Name, Success: true}
	if _, err := resticCommand(ctx, cfg, repo, cacheDir, checkArgs(co.ReadDataSubset), opts); err != nil {
		entry.Success = false
		entry.Error = err.Error()
		logger.Error("backup check failed", "repo", repo.Name, "err", err)
		return entry
	}
	logger.Info("backup check passed", "repo", repo.Name)
	if co.Drill {
		if err := runDrill(ctx, cfg, repo, cacheDir, opts); err != nil {
			entry.Success = false
			entry.Error = err.Error()
			logger.Error("backup drill failed", "repo", repo.Name, "err", err)
			return entry
		}
		logger.Info("backup drill passed", "repo", repo.Name)
	}
	return entry
}

func checkArgs(readDataSubset string) []string {
	args := []string{"check"}
	readDataSubset = strings.TrimSpace(readDataSubset)
	if readDataSubset != "" {
		args = append(args, "--read-data-subset", readDataSubset)
	}
	return args
}

var (
	readDataSubsetPercent  = regexp.MustCompile(`^\d+(\.\d+)?%$`)
	readDataSubsetSize     = regexp.MustCompile(`^\d+(\.\d+)?[kKmMgGtT]$`)
	readDataSubsetFraction = regexp.MustCompile(`^\d+/\d+$`)
)

func validateReadDataSubset(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if readDataSubsetPercent.MatchString(value) || readDataSubsetSize.MatchString(value) {
		return nil
	}
	if readDataSubsetFraction.MatchString(value) {
		parts := strings.SplitN(value, "/", 2)
		n, ok1 := parsePositiveInt(parts[0])
		m, ok2 := parsePositiveInt(parts[1])
		if ok1 && ok2 && n <= m {
			return nil
		}
		return fmt.Errorf("--read-data-subset %q: n/m requires 1 <= n <= m", value)
	}
	return fmt.Errorf("--read-data-subset %q must be a percentage (5%%), a size (250M), or a fraction (1/5)", value)
}

func parsePositiveInt(value string) (int, bool) {
	n := 0
	if value == "" {
		return 0, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

func applyCheckResult(status StatusFile, name string, checkTime time.Time, success bool, tunnel bool) StatusFile {
	out := StatusFile{Version: statusVersion, Repos: make([]RepoStatus, 0, len(status.Repos)+1)}
	found := false
	for _, repo := range status.Repos {
		if repo.Name == name {
			last := checkTime
			ok := success
			repo.CheckLast = &last
			repo.CheckSuccess = &ok
			repo.Tunnel = tunnel
			found = true
		}
		out.Repos = append(out.Repos, repo)
	}
	if !found {
		last := checkTime
		ok := success
		out.Repos = append(out.Repos, RepoStatus{Name: name, Engine: "restic", CheckLast: &last, CheckSuccess: &ok, Tunnel: tunnel})
	}
	return out
}

type drillFile struct {
	Path string
	Size int64
}

type drillDecision int

const (
	drillMatch drillDecision = iota
	drillMismatch
	drillSkip
)

func runDrill(ctx context.Context, cfg Config, repo Repo, cacheDir string, opts Options) error {
	snapResult, err := resticCommand(ctx, cfg, repo, cacheDir, []string{"snapshots", "--json", "--latest", "1"}, opts)
	if err != nil {
		return err
	}
	snapshot, ok, err := parseLatestSnapshot(snapResult.Stdout)
	if err != nil {
		return fmt.Errorf("parse latest snapshot: %w", err)
	}
	if !ok {
		opts.logger().Info("backup drill has no snapshot to sample", "repo", repo.Name)
		return nil
	}
	lsResult, err := resticCommand(ctx, cfg, repo, cacheDir, []string{"ls", "--json", snapshot.ID}, opts)
	if err != nil {
		return err
	}
	nodes, err := parseLsNodes(lsResult.Stdout)
	if err != nil {
		return fmt.Errorf("parse snapshot listing: %w", err)
	}
	sample := selectDrillFiles(nodes, drillSampleLimit, drillSizeLimit)
	if len(sample) == 0 {
		opts.logger().Info("backup drill sample is empty", "repo", repo.Name)
		return nil
	}

	tmpRoot, err := os.MkdirTemp(filepath.Dir(cfg.StatusPath), "drill-*")
	if err != nil {
		return fmt.Errorf("create drill dir: %w", err)
	}
	defer os.RemoveAll(tmpRoot)

	args := []string{"restore", snapshot.ID, "--target", tmpRoot}
	for _, file := range sample {
		args = append(args, "--include", file.Path)
	}
	if _, err := resticCommand(ctx, cfg, repo, cacheDir, args, opts); err != nil {
		return err
	}

	for _, file := range sample {
		restored := filepath.Join(tmpRoot, filepath.FromSlash(file.Path))
		live := drillLivePath(file.Path, opts.goos(), opts.HostRoot)
		decision, err := compareDrillFile(restored, live, snapshot.Time)
		if err != nil {
			return err
		}
		if decision == drillMismatch {
			return fmt.Errorf("drill mismatch: %s differs from its snapshot copy", file.Path)
		}
	}
	return nil
}

func drillLivePath(lsPath, goos, hostRoot string) string {
	if goos != "windows" {
		if hostRoot != "" {
			return hostRoot + lsPath
		}
		return lsPath
	}
	trimmed := strings.TrimPrefix(lsPath, "/")
	drive, rest, found := strings.Cut(trimmed, "/")
	if len(drive) == 1 && isDriveLetter(drive[0]) {
		if !found {
			return drive + `:\`
		}
		return drive + `:\` + strings.ReplaceAll(rest, "/", `\`)
	}
	return strings.ReplaceAll(lsPath, "/", `\`)
}

func isDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func compareDrillFile(restoredPath, livePath string, snapshotTime time.Time) (drillDecision, error) {
	restored, err := os.ReadFile(restoredPath)
	if err != nil {
		return drillSkip, fmt.Errorf("read restored file: %w", err)
	}
	info, err := os.Lstat(livePath)
	if err != nil {
		if os.IsNotExist(err) {
			return drillSkip, nil
		}
		return drillSkip, fmt.Errorf("stat live file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return drillSkip, nil
	}
	bytesEqual := false
	if live, err := os.ReadFile(livePath); err == nil {
		bytesEqual = bytes.Equal(restored, live)
	}
	return decideDrill(true, info.ModTime(), snapshotTime, bytesEqual), nil
}

func decideDrill(liveExists bool, liveMtime, snapshotTime time.Time, bytesEqual bool) drillDecision {
	if !liveExists {
		return drillSkip
	}
	if liveMtime.After(snapshotTime) {
		return drillSkip
	}
	if bytesEqual {
		return drillMatch
	}
	return drillMismatch
}

func selectDrillFiles(nodes []lsNode, maxFiles int, maxSize int64) []drillFile {
	files := make([]drillFile, 0, len(nodes))
	for _, node := range nodes {
		if node.Type != "file" {
			continue
		}
		if node.Path == "" {
			continue
		}
		if node.Size > maxSize {
			continue
		}
		files = append(files, drillFile{Path: node.Path, Size: node.Size})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return files
}

type lsNode struct {
	Path string
	Size int64
	Type string
}

func parseLatestSnapshot(data []byte) (Snapshot, bool, error) {
	var raw []struct {
		ID   string    `json:"id"`
		Time time.Time `json:"time"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Snapshot{}, false, err
	}
	best := Snapshot{}
	found := false
	for _, item := range raw {
		if item.ID == "" {
			continue
		}
		when := item.Time.UTC()
		if !found || when.After(best.Time) {
			best = Snapshot{ID: item.ID, Time: when}
			found = true
		}
	}
	return best, found, nil
}

func parseLsNodes(data []byte) ([]lsNode, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var nodes []lsNode
	for decoder.More() {
		var msg struct {
			MessageType string `json:"message_type"`
			StructType  string `json:"struct_type"`
			Type        string `json:"type"`
			Path        string `json:"path"`
			Size        int64  `json:"size"`
		}
		if err := decoder.Decode(&msg); err != nil {
			return nil, err
		}
		kind := msg.MessageType
		if kind == "" {
			kind = msg.StructType
		}
		if kind != "node" {
			continue
		}
		nodes = append(nodes, lsNode{Path: msg.Path, Size: msg.Size, Type: msg.Type})
	}
	return nodes, nil
}
