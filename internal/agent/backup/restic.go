package backup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Command struct {
	Path         string
	Args         []string
	Env          []string
	OnStdoutLine func([]byte)
}

type CommandResult struct {
	Stdout []byte
	Stderr []byte
}

type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
}

type ExecRunner struct{}

type Options struct {
	Runner           CommandRunner
	Logger           *slog.Logger
	Now              func() time.Time
	GOOS             string
	ProgressInterval time.Duration
}

func (ExecRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Env = command.Env
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if command.OnStdoutLine == nil {
		cmd.Stdout = &stdout
		err := cmd.Run()
		return CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return CommandResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return CommandResult{Stderr: stderr.Bytes()}, err
	}
	scanner := bufio.NewScanner(io.TeeReader(pipe, &stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		command.OnStdoutLine(scanner.Bytes())
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		_, _ = io.Copy(&stdout, pipe)
	}
	err = cmd.Wait()
	if err == nil {
		err = scanErr
	}
	return CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

func ResolveResticPath(cfg Config) (string, error) {
	return resolveResticPath(cfg, exec.LookPath, executableExists, runtime.GOOS)
}

func resolveResticPath(cfg Config, lookup func(string) (string, error), exists func(string, string) bool, goos string) (string, error) {
	if path := strings.TrimSpace(cfg.ResticPath); path != "" {
		resolved, ok := resolveResticCandidate(path, lookup, exists, goos)
		if ok {
			return resolved, nil
		}
		return "", fmt.Errorf("restic binary %q was configured but was not found or is not executable", path)
	}
	if path := strings.TrimSpace(os.Getenv("SM_RESTIC_PATH")); path != "" {
		resolved, ok := resolveResticCandidate(path, lookup, exists, goos)
		if ok {
			return resolved, nil
		}
		return "", fmt.Errorf("restic binary %q from SM_RESTIC_PATH was not found or is not executable", path)
	}
	installed := defaultResticInstallPath(goos)
	if exists(installed, goos) {
		return installed, nil
	}
	if resolved, err := lookup("restic"); err == nil {
		return resolved, nil
	}
	return "", fmt.Errorf("restic binary not found: set restic_path in backup.toml or SM_RESTIC_PATH, install %s, or put restic on PATH", installed)
}

func resolveResticCandidate(path string, lookup func(string) (string, error), exists func(string, string) bool, goos string) (string, bool) {
	if pathLooksExplicit(path) {
		if exists(path, goos) {
			return path, true
		}
		return "", false
	}
	if resolved, err := lookup(path); err == nil {
		return resolved, true
	}
	if exists(path, goos) {
		return path, true
	}
	return "", false
}

func pathLooksExplicit(path string) bool {
	return filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.ContainsAny(path, `/\`)
}

func executableExists(path, goos string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if goos == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

func defaultResticInstallPath(goos string) string {
	if goos == "windows" {
		return `C:\Program Files\ServerMonitor\restic.exe`
	}
	return "/usr/local/bin/sm-restic"
}

var strippedResticEnvKeys = []string{"RESTIC_PASSWORD", "RESTIC_PASSWORD_COMMAND", "RESTIC_REPOSITORY_FILE"}

func withResticEnv(repo Repo, cacheDir string, repoEnv map[string]string) []string {
	overrides := make(map[string]string, len(repoEnv)+3)
	for key, value := range repoEnv {
		overrides[key] = value
	}
	overrides["RESTIC_REPOSITORY"] = repo.URL
	overrides["RESTIC_PASSWORD_FILE"] = repo.PasswordFile
	overrides["RESTIC_CACHE_DIR"] = cacheDir
	return envWithOverrides(stripEnvKeys(os.Environ(), strippedResticEnvKeys...), overrides)
}

func stripEnvKeys(env []string, keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, key := range keys {
		drop[key] = true
	}
	out := make([]string, 0, len(env))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if ok && drop[key] {
			continue
		}
		out = append(out, item)
	}
	return out
}

func resticGlobalArgs(repo Repo, args []string) []string {
	if !repo.S3PathStyle {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "-o", "s3.bucket-lookup=path")
	return append(out, args...)
}

func envWithOverrides(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, replace := overrides[key]; replace {
			continue
		}
		out = append(out, item)
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

func resticCommand(ctx context.Context, cfg Config, repo Repo, cacheDir string, args []string, opts Options) (CommandResult, error) {
	return resticCommandStream(ctx, cfg, repo, cacheDir, args, args[0], opts, nil)
}

func resticCommandAction(ctx context.Context, cfg Config, repo Repo, cacheDir string, args []string, action string, opts Options) (CommandResult, error) {
	return resticCommandStream(ctx, cfg, repo, cacheDir, args, action, opts, nil)
}

func resticCommandStream(ctx context.Context, cfg Config, repo Repo, cacheDir string, args []string, action string, opts Options, onStdoutLine func([]byte)) (CommandResult, error) {
	if repo.tunnelErr != nil {
		return CommandResult{}, fmt.Errorf("%s failed: %w", action, repo.tunnelErr)
	}
	if repo.UsesTunnel() && repo.URL == "" {
		return CommandResult{}, fmt.Errorf("%s failed: tunnel repo %q was not resolved through an active tunnel session", action, repo.Name)
	}
	repoEnv, err := loadRepoEnv(repo)
	if err != nil {
		return CommandResult{}, fmt.Errorf("%s failed: %w", action, err)
	}
	result, err := opts.runner().Run(ctx, Command{
		Path:         cfg.ResticPath,
		Args:         resticGlobalArgs(repo, args),
		Env:          withResticEnv(repo, cacheDir, repoEnv),
		OnStdoutLine: onStdoutLine,
	})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return result, commandError(action, repo, repoEnv, result, err)
	}
	return result, nil
}

func commandError(action string, repo Repo, repoEnv map[string]string, result CommandResult, err error) error {
	detail := strings.TrimSpace(string(result.Stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.Stdout))
	}
	detail = sanitizeResticText(detail, repo, repoEnv)
	if detail != "" {
		return fmt.Errorf("%s failed: %v: %s", action, err, detail)
	}
	return fmt.Errorf("%s failed: %w", action, err)
}

func sanitizeResticText(text string, repo Repo, repoEnv map[string]string) string {
	text = strings.TrimSpace(text)
	if repo.URL != "" {
		text = strings.ReplaceAll(text, repo.URL, "[repository]")
	}
	for _, value := range repoEnvSecretValues(repoEnv) {
		text = strings.ReplaceAll(text, value, "[credential]")
	}
	return text
}

func (o Options) runner() CommandRunner {
	if o.Runner != nil {
		return o.Runner
	}
	return ExecRunner{}
}

func (o Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func (o Options) progressInterval() time.Duration {
	if o.ProgressInterval > 0 {
		return o.ProgressInterval
	}
	return 10 * time.Second
}
