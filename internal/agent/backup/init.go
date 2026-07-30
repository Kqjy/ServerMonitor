package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type InitRepoResult struct {
	Name        string
	Initialized bool
	Skipped     bool
	Error       string
}

type InitResult struct {
	Repos []InitRepoResult
}

func InitFromConfigPath(ctx context.Context, path string, opts Options) (InitResult, error) {
	cfg, err := Load(path)
	if err != nil {
		return InitResult{}, err
	}
	resticPath, err := ResolveResticPath(cfg)
	if err != nil {
		return InitResult{}, err
	}
	cfg.ResticPath = resticPath
	return Init(ctx, cfg, opts)
}

func Init(ctx context.Context, cfg Config, opts Options) (InitResult, error) {
	cfg = normalizedConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return InitResult{}, err
	}
	if cfg.ResticPath == "" {
		return InitResult{}, fmt.Errorf("restic path is not resolved")
	}
	release, err := tunnelLockGuard(cfg, opts)
	if err != nil {
		return InitResult{}, err
	}
	defer release()
	cfg, session := PrepareTunnel(cfg, opts)
	if session != nil {
		defer session.Close()
	}
	cacheDir := filepath.Join(filepath.Dir(cfg.StatusPath), "restic-cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return InitResult{}, fmt.Errorf("create restic cache dir: %w", err)
	}
	result := InitResult{Repos: make([]InitRepoResult, 0, len(cfg.Repos))}
	logger := opts.logger()
	for _, repo := range cfg.Repos {
		entry := InitRepoResult{Name: repo.Name}
		if err := passwordFileReady(repo.PasswordFile); err != nil {
			entry.Error = err.Error()
			result.Repos = append(result.Repos, entry)
			logger.Error("backup repo init failed", "repo", repo.Name, "err", err)
			continue
		}
		catResult, err := resticCommand(ctx, cfg, repo, cacheDir, []string{"cat", "config"}, opts)
		if err == nil {
			entry.Skipped = true
			result.Repos = append(result.Repos, entry)
			logger.Info("backup repo already initialized", "repo", repo.Name)
			continue
		}
		if !isUninitializedRepository(string(catResult.Stdout) + "\n" + string(catResult.Stderr)) {
			entry.Error = err.Error()
			result.Repos = append(result.Repos, entry)
			logger.Error("backup repo init failed", "repo", repo.Name, "err", err)
			continue
		}
		_, err = resticCommand(ctx, cfg, repo, cacheDir, []string{"init"}, opts)
		if err != nil {
			entry.Error = err.Error()
			result.Repos = append(result.Repos, entry)
			logger.Error("backup repo init failed", "repo", repo.Name, "err", err)
			continue
		}
		entry.Initialized = true
		result.Repos = append(result.Repos, entry)
		logger.Info("backup repo initialized", "repo", repo.Name)
	}
	if err := seedScheduledStatus(cfg); err != nil {
		logger.Warn("could not seed scheduled backup status", "path", cfg.StatusPath, "err", err)
	}
	return result, nil
}

func seedScheduledStatus(cfg Config) error {
	existing, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(existing.Repos))
	for _, repo := range existing.Repos {
		known[repo.Name] = true
	}
	oneFileSystem := cfg.OneFileSystem
	scheduled := make([]RepoStatus, 0, len(cfg.Repos))
	for _, repo := range cfg.Repos {
		if known[repo.Name] {
			continue
		}
		scheduled = append(scheduled, RepoStatus{
			Name:          repo.Name,
			Engine:        "restic",
			Tunnel:        repo.UsesTunnel(),
			Paths:         nonEmptyCopy(cfg.Paths),
			Excludes:      nonEmptyCopy(cfg.Excludes),
			OneFileSystem: &oneFileSystem,
		})
	}
	if len(scheduled) == 0 {
		return nil
	}
	return writeStatusAtomic(cfg.StatusPath, mergeStatus(existing, scheduled))
}

func (r InitResult) Failed() bool {
	for _, repo := range r.Repos {
		if repo.Error != "" {
			return true
		}
	}
	return false
}

func passwordFileReady(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("password_file %s is missing or unreadable; run the installer or reconfigure with --enable-backup so it generates it: %w", path, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return fmt.Errorf("password_file %s is empty or not a file; run the installer or reconfigure with --enable-backup so it generates it", path)
	}
	return nil
}

func isUninitializedRepository(text string) bool {
	text = strings.ToLower(text)
	for _, needle := range []string{
		"repository does not exist",
		"is there a repository at",
		"unable to open config",
		"config file does not exist",
		"config does not exist",
		"not initialized",
		"unable to open repository",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
