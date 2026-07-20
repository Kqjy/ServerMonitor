package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeResticResponse struct {
	stdout string
	stderr string
	err    error
}

type fakeResticRunner struct {
	calls     []Command
	responses map[string][]fakeResticResponse
}

type progressResticRunner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	fail    bool
}

func (r *progressResticRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	switch resticSubcommand(command.Args) {
	case "backup":
		if command.OnStdoutLine != nil {
			command.OnStdoutLine([]byte(`{"message_type":"status","percent_done":0.42,"bytes_done":1200,"total_bytes":3000}`))
		}
		r.once.Do(func() { close(r.entered) })
		select {
		case <-ctx.Done():
			return CommandResult{}, ctx.Err()
		case <-r.release:
		}
		if r.fail {
			return CommandResult{Stderr: []byte("failed")}, errors.New("exit status 1")
		}
		return CommandResult{Stdout: []byte(`{"message_type":"summary","total_duration":2.6,"data_added":123,"total_bytes_processed":456}` + "\n")}, nil
	case "snapshots":
		return CommandResult{Stdout: []byte(`[]`)}, nil
	case "forget":
		return CommandResult{}, nil
	}
	return CommandResult{}, errors.New("unexpected command")
}

func newFakeResticRunner() *fakeResticRunner {
	return &fakeResticRunner{responses: map[string][]fakeResticResponse{}}
}

func (r *fakeResticRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	r.calls = append(r.calls, Command{
		Path:   command.Path,
		Args:   append([]string(nil), command.Args...),
		Env:    append([]string(nil), command.Env...),
		Chroot: command.Chroot,
	})
	key := resticCallKey(command)
	queue := r.responses[key]
	if len(queue) > 0 {
		response := queue[0]
		r.responses[key] = queue[1:]
		emitFakeStdout(command, response.stdout)
		return CommandResult{Stdout: []byte(response.stdout), Stderr: []byte(response.stderr)}, response.err
	}
	switch resticSubcommand(command.Args) {
	case "backup":
		return CommandResult{Stdout: []byte(`{"message_type":"summary","total_duration":2.6,"data_added":123,"total_bytes_processed":456}` + "\n")}, nil
	case "snapshots":
		return CommandResult{Stdout: []byte(`[{"id":"abcdef123456","time":"2026-07-03T02:00:00Z","paths":["/etc"]}]`)}, nil
	case "forget":
		return CommandResult{}, nil
	case "cat":
		return CommandResult{}, nil
	case "init":
		return CommandResult{}, nil
	default:
		return CommandResult{Stderr: []byte("unexpected command")}, errors.New("exit status 1")
	}
}

func emitFakeStdout(command Command, stdout string) {
	if command.OnStdoutLine == nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(line) != "" {
			command.OnStdoutLine([]byte(line))
		}
	}
}

func (r *fakeResticRunner) enqueue(repoURL, action string, response fakeResticResponse) {
	key := repoURL + "|" + action
	r.responses[key] = append(r.responses[key], response)
}

func resticSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

func resticCallKey(command Command) string {
	return envValue(command.Env, "RESTIC_REPOSITORY") + "|" + resticSubcommand(command.Args)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, value := range env {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{
		StatusPath:    filepath.Join(dir, "backup-status.json"),
		ResticPath:    "restic",
		Paths:         []string{"/etc", "/home"},
		Excludes:      []string{"**/.cache"},
		OneFileSystem: true,
		PruneMode:     "host",
		Retention:     Retention{Daily: 7, Weekly: 4, Monthly: 6},
		Repos: []Repo{
			{Name: "repo1", URL: "rest:https://user:pass@backup-a.example.com/host1", PasswordFile: filepath.Join(dir, "key1")},
		},
	}
}

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func q(value string) string {
	return strconv.Quote(value)
}

func intRef(value int) *int {
	return &value
}

func TestLoadConfigDefaultsAndEnvOverride(t *testing.T) {
	configPath := writeConfigFile(t, `
paths = [" /etc "]
excludes = [" **/.cache "]
one_file_system = true

[retention]
weekly = 2

[[repo]]
name = " repo1 "
url = " rest:https://user:pass@example.com/repo "
password_file = " /etc/servermonitor/backup.key "
`)
	t.Setenv("SM_BACKUP_CONFIG", configPath)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StatusPath != DefaultStatusPath() {
		t.Fatalf("status path = %q, want %q", cfg.StatusPath, DefaultStatusPath())
	}
	if cfg.PruneMode != "host" {
		t.Fatalf("prune mode = %q", cfg.PruneMode)
	}
	if cfg.Retention != (Retention{Daily: 7, Weekly: 2, Monthly: 6}) {
		t.Fatalf("retention = %#v", cfg.Retention)
	}
	if !reflect.DeepEqual(cfg.Paths, []string{"/etc"}) {
		t.Fatalf("paths = %#v", cfg.Paths)
	}
	if !reflect.DeepEqual(cfg.Excludes, []string{"**/.cache"}) {
		t.Fatalf("excludes = %#v", cfg.Excludes)
	}
	if cfg.Repos[0].Name != "repo1" || cfg.Repos[0].PasswordFile != "/etc/servermonitor/backup.key" {
		t.Fatalf("repo trimming failed: %#v", cfg.Repos[0])
	}
}

func TestDefaultStatusPathHonorsEnvironment(t *testing.T) {
	t.Setenv("SM_BACKUP_STATUS_PATH", "/tmp/servermonitor-backup-status.json")
	if got := DefaultStatusPath(); got != "/tmp/servermonitor-backup-status.json" {
		t.Fatalf("DefaultStatusPath = %q", got)
	}
}

func TestLoadConfigRepoRetentionOverrides(t *testing.T) {
	configPath := writeConfigFile(t, `
paths = ["/etc"]

[retention]
daily = 9
weekly = 5
monthly = 2

[[repo]]
name = "full"
url = "rest:https://example.com/full"
password_file = "/key-full"
  [repo.retention]
  daily = 30
  weekly = 1
  monthly = 0

[[repo]]
name = "partial"
url = "rest:https://example.com/partial"
password_file = "/key-partial"
  [repo.retention]
  daily = 0

[[repo]]
name = "none"
url = "rest:https://example.com/none"
password_file = "/key-none"
`)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := []Retention{
		effectiveRetention(cfg.Retention, cfg.Repos[0].Retention),
		effectiveRetention(cfg.Retention, cfg.Repos[1].Retention),
		effectiveRetention(cfg.Retention, cfg.Repos[2].Retention),
	}
	want := []Retention{
		{Daily: 30, Weekly: 1, Monthly: 0},
		{Daily: 0, Weekly: 5, Monthly: 2},
		{Daily: 9, Weekly: 5, Monthly: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective retention = %#v, want %#v", got, want)
	}
}

func TestResolveResticPathEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "restic")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, []byte("fake"), 0o755); err != nil {
		t.Fatalf("write restic: %v", err)
	}
	t.Setenv("SM_RESTIC_PATH", path)
	got, err := ResolveResticPath(Config{})
	if err != nil {
		t.Fatalf("ResolveResticPath: %v", err)
	}
	if got != path {
		t.Fatalf("restic path = %q, want %q", got, path)
	}
}

func TestConfigValidationFailures(t *testing.T) {
	validStatus := filepath.Join(t.TempDir(), "status.json")
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no repo",
			body: `paths = ["/etc"]`,
			want: "at least one repo",
		},
		{
			name: "no path",
			body: `[[repo]]
name = "repo1"
url = "rest:https://example/repo"
password_file = "/key"
`,
			want: "at least one path",
		},
		{
			name: "repo name",
			body: `paths = ["/etc"]
[[repo]]
url = "rest:https://example/repo"
password_file = "/key"
`,
			want: "name is required",
		},
		{
			name: "repo url",
			body: `paths = ["/etc"]
[[repo]]
name = "repo1"
password_file = "/key"
`,
			want: "exactly one of url or tunnel_name is required",
		},
		{
			name: "repo password",
			body: `paths = ["/etc"]
[[repo]]
name = "repo1"
url = "rest:https://example/repo"
`,
			want: "password_file is required",
		},
		{
			name: "tunnel scheme in url",
			body: `paths = ["/etc"]
[[repo]]
name = "repo1"
url = "tunnel:node/repo1"
password_file = "/key"
`,
			want: "url uses the tunnel: scheme, which is not a restic backend",
		},
		{
			name: "duplicate repo",
			body: `paths = ["/etc"]
[[repo]]
name = "repo1"
url = "rest:https://example/repo1"
password_file = "/key1"
[[repo]]
name = "repo1"
url = "rest:https://example/repo2"
password_file = "/key2"
`,
			want: "duplicated",
		},
		{
			name: "invalid prune",
			body: `paths = ["/etc"]
prune_mode = "bad"
[[repo]]
name = "repo1"
url = "rest:https://example/repo"
password_file = "/key"
`,
			want: "prune_mode",
		},
		{
			name: "negative retention",
			body: `paths = ["/etc"]
[retention]
daily = -1
[[repo]]
name = "repo1"
url = "rest:https://example/repo"
password_file = "/key"
`,
			want: "retention values",
		},
		{
			name: "zero retention",
			body: `paths = ["/etc"]
[retention]
daily = 0
weekly = 0
monthly = 0
[[repo]]
name = "repo1"
url = "rest:https://example/repo"
password_file = "/key"
`,
			want: "at least one retention",
		},
		{
			name: "negative repo retention",
			body: `paths = ["/etc"]
[[repo]]
name = "repo1"
url = "rest:https://example/repo"
password_file = "/key"
  [repo.retention]
  daily = -1
`,
			want: "retention values",
		},
		{
			name: "zero repo retention",
			body: `paths = ["/etc"]
[retention]
daily = 1
weekly = 0
monthly = 0
[[repo]]
name = "repo1"
url = "rest:https://example/repo"
password_file = "/key"
  [repo.retention]
  daily = 0
`,
			want: "at least one retention",
		},
		{
			name: "relative env_file",
			body: `paths = ["/etc"]
[[repo]]
name = "repo1"
url = "s3:https://example/bucket/repo"
password_file = "/key"
env_file = "relative/creds.env"
`,
			want: "env_file must be an absolute",
		},
		{
			name: "s3 field on non-s3 url",
			body: `paths = ["/etc"]
[[repo]]
name = "repo1"
url = "rest:https://example/repo"
password_file = "/key"
s3_region = "us-east-1"
`,
			want: "require an s3: url",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, "status_path = "+q(validStatus)+"\n"+tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestEffectiveRetentionMerge(t *testing.T) {
	cases := []struct {
		name     string
		base     Retention
		override *RetentionOverride
		want     Retention
	}{
		{
			name: "nil",
			base: Retention{Daily: 7, Weekly: 4, Monthly: 6},
			want: Retention{Daily: 7, Weekly: 4, Monthly: 6},
		},
		{
			name:     "partial",
			base:     Retention{Daily: 7, Weekly: 4, Monthly: 6},
			override: &RetentionOverride{Weekly: intRef(9)},
			want:     Retention{Daily: 7, Weekly: 9, Monthly: 6},
		},
		{
			name:     "explicit zero",
			base:     Retention{Daily: 7, Weekly: 4, Monthly: 6},
			override: &RetentionOverride{Daily: intRef(0), Monthly: intRef(12)},
			want:     Retention{Daily: 0, Weekly: 4, Monthly: 12},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveRetention(tc.base, tc.override)
			if got != tc.want {
				t.Fatalf("effective retention = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseBackupSummaryUsesFinalSummary(t *testing.T) {
	data := []byte(`{"message_type":"status","percent_done":0.1}
not json
{"message_type":"summary","total_duration":2.2,"data_added":10,"total_bytes_processed":20}
{"message_type":"summary","total_duration":2.6,"data_added":30,"total_bytes_processed":40}
`)
	summary, err := parseBackupSummary(data)
	if err != nil {
		t.Fatalf("parseBackupSummary: %v", err)
	}
	if summary != (backupSummary{DurationS: 3, AddedBytes: 30, TotalBytes: 40}) {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestParseBackupProgressLine(t *testing.T) {
	valid, ok := parseBackupProgressLine([]byte(`{"message_type":"status","percent_done":0.42,"bytes_done":1200,"total_bytes":3000}`))
	if !ok || valid != (backupProgressUpdate{Percent: 42, BytesDone: 1200, TotalBytes: 3000}) {
		t.Fatalf("valid progress = %#v, %v", valid, ok)
	}
	if _, ok := parseBackupProgressLine([]byte(`{"message_type":"summary","percent_done":1}`)); ok {
		t.Fatal("summary line parsed as progress")
	}
	large := []byte(`{"message_type":"status","percent_done":1.5,"current_files":["` + strings.Repeat("x", 100000) + `"]}`)
	oversized, ok := parseBackupProgressLine(large)
	if !ok || oversized.Percent != 100 {
		t.Fatalf("oversized progress = %#v, %v", oversized, ok)
	}
	missing, ok := parseBackupProgressLine([]byte(`{"message_type":"status","percent_done":-0.1}`))
	if !ok || missing.Percent != 0 || missing.BytesDone != 0 || missing.TotalBytes != 0 {
		t.Fatalf("missing totals progress = %#v, %v", missing, ok)
	}
}

func TestRunProgressLifecycleAndHeartbeat(t *testing.T) {
	cfg := testConfig(t)
	runner := &progressResticRunner{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), cfg, Options{Runner: runner, Now: func() time.Time { return time.Unix(1, 0).UTC() }, ProgressInterval: 10 * time.Millisecond})
		done <- err
	}()
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("backup did not start")
	}
	path := progressPath(cfg.StatusPath)
	var progress ProgressFile
	deadline := time.Now().Add(time.Second)
	for {
		var err error
		progress, err = readProgressFile(path)
		if err == nil && progress.UpdatedAt > 1 && progress.Percent == 42 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress was not refreshed: %#v, %v", progress, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !progress.Running || progress.Repo != "repo1" || progress.ReposDone != 0 || progress.ReposTotal != 1 || progress.StartedAt != 1 || progress.BytesDone != 1200 || progress.TotalBytes != 3000 {
		t.Fatalf("progress = %#v", progress)
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("progress file remains after success: %v", err)
	}
}

func TestRunProgressCleanupOnRepoErrorAndEarlyCancellation(t *testing.T) {
	cfg := testConfig(t)
	runner := &progressResticRunner{entered: make(chan struct{}), release: make(chan struct{}), fail: true}
	close(runner.release)
	if _, err := Run(context.Background(), cfg, Options{Runner: runner, ProgressInterval: time.Millisecond}); err != nil {
		t.Fatalf("Run repo failure: %v", err)
	}
	if _, err := os.Stat(progressPath(cfg.StatusPath)); !os.IsNotExist(err) {
		t.Fatalf("progress file remains after repo error: %v", err)
	}

	cfg = testConfig(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.StatusPath), "restic-cache"), []byte("blocked"), 0o600); err != nil {
		t.Fatalf("block cache dir: %v", err)
	}
	if _, err := Run(context.Background(), cfg, Options{Runner: newFakeResticRunner()}); err == nil {
		t.Fatal("expected cache directory error")
	}
	if _, err := os.Stat(progressPath(cfg.StatusPath)); !os.IsNotExist(err) {
		t.Fatalf("progress file remains after run error: %v", err)
	}

	cfg = testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, cfg, Options{Runner: newFakeResticRunner()}); err != nil {
		t.Fatalf("Run canceled: %v", err)
	}
	if _, err := os.Stat(progressPath(cfg.StatusPath)); !os.IsNotExist(err) {
		t.Fatalf("progress file remains after cancellation: %v", err)
	}
	if _, err := os.Stat(cfg.StatusPath); !os.IsNotExist(err) {
		t.Fatalf("status file written for early cancellation: %v", err)
	}
}

func TestRunContinuesAfterRepoFailureAndExitLogic(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repos = append(cfg.Repos, Repo{Name: "repo2", URL: "rest:https://secret@example.com/repo2", PasswordFile: filepath.Join(t.TempDir(), "key2")})
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "backup", fakeResticResponse{stderr: "failed against " + cfg.Repos[0].URL, err: errors.New("exit status 1")})

	result, err := Run(context.Background(), cfg, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.AnySucceeded() {
		t.Fatal("one successful repo should make the run successful")
	}
	if len(result.Repos) != 2 {
		t.Fatalf("repos = %d, want 2", len(result.Repos))
	}
	if result.Repos[0].Success || !result.Repos[1].Success {
		t.Fatalf("unexpected success flags: %#v", result.Repos)
	}
	if strings.Contains(result.Repos[0].Error, cfg.Repos[0].URL) {
		t.Fatalf("error leaked repo URL: %q", result.Repos[0].Error)
	}

	cfg2 := testConfig(t)
	runner2 := newFakeResticRunner()
	runner2.enqueue(cfg2.Repos[0].URL, "backup", fakeResticResponse{stderr: "failed", err: errors.New("exit status 1")})
	result, err = Run(context.Background(), cfg2, Options{Runner: runner2})
	if err != nil {
		t.Fatalf("Run all failed: %v", err)
	}
	if result.AnySucceeded() {
		t.Fatal("all failed repos should not be successful")
	}
}

func TestRunUsesRepoRetentionForForget(t *testing.T) {
	cfg := testConfig(t)
	cfg.Retention = Retention{Daily: 7, Weekly: 4, Monthly: 6}
	cfg.Repos = []Repo{
		{
			Name:         "repo1",
			URL:          "rest:https://example.com/repo1",
			PasswordFile: filepath.Join(t.TempDir(), "key1"),
			Retention:    &RetentionOverride{Daily: intRef(30), Weekly: intRef(0)},
		},
		{
			Name:         "repo2",
			URL:          "rest:https://example.com/repo2",
			PasswordFile: filepath.Join(t.TempDir(), "key2"),
			Retention:    &RetentionOverride{Monthly: intRef(2)},
		},
	}
	runner := newFakeResticRunner()

	if _, err := Run(context.Background(), cfg, Options{Runner: runner}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := map[string][]string{}
	for _, call := range runner.calls {
		if len(call.Args) > 0 && call.Args[0] == "forget" {
			got[envValue(call.Env, "RESTIC_REPOSITORY")] = call.Args
		}
	}
	want := map[string][]string{
		cfg.Repos[0].URL: {"forget", "--prune", "--group-by", "host", "--keep-daily", "30", "--keep-monthly", "6"},
		cfg.Repos[1].URL: {"forget", "--prune", "--group-by", "host", "--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forget args by repo = %#v, want %#v", got, want)
	}
}

func TestForgetArgsOmitZeroRetentionValues(t *testing.T) {
	got := forgetArgs(Retention{Daily: 0, Weekly: 4, Monthly: 0})
	want := []string{"forget", "--prune", "--group-by", "host", "--keep-weekly", "4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forget args = %#v, want %#v", got, want)
	}
}

func TestRunRepoRecordsScopeOnSuccessAndFailure(t *testing.T) {
	cfg := testConfig(t)
	cfg.Paths = []string{"/etc", "", "/var/lib/docker/volumes"}
	cfg.Excludes = []string{"**/.cache", "", "/var/lib/docker/overlay2"}
	runner := newFakeResticRunner()
	success := runRepo(context.Background(), cfg, cfg.Repos[0], t.TempDir(), Options{Runner: runner}, nil, nil)
	if !success.Success || !reflect.DeepEqual(success.Paths, []string{"/etc", "/var/lib/docker/volumes"}) || !reflect.DeepEqual(success.Excludes, []string{"**/.cache", "/var/lib/docker/overlay2"}) || success.OneFileSystem == nil || !*success.OneFileSystem {
		t.Fatalf("success scope = %#v", success)
	}

	failingRunner := newFakeResticRunner()
	failingRunner.enqueue(cfg.Repos[0].URL, "backup", fakeResticResponse{stderr: "failed", err: errors.New("exit status 1")})
	failure := runRepo(context.Background(), cfg, cfg.Repos[0], t.TempDir(), Options{Runner: failingRunner}, nil, nil)
	if failure.Success || !reflect.DeepEqual(failure.Paths, success.Paths) || !reflect.DeepEqual(failure.Excludes, success.Excludes) || failure.OneFileSystem == nil || !*failure.OneFileSystem {
		t.Fatalf("failure scope = %#v", failure)
	}
}

func TestBackupArgsPlatformFlags(t *testing.T) {
	cfg := testConfig(t)
	linuxArgs := backupArgs(cfg, "linux")
	if !containsArg(linuxArgs, "--one-file-system") {
		t.Fatalf("linux args missing one-file-system: %#v", linuxArgs)
	}
	if containsArg(linuxArgs, "--use-fs-snapshot") {
		t.Fatalf("linux args should not include fs snapshot: %#v", linuxArgs)
	}
	windowsArgs := backupArgs(cfg, "windows")
	if containsArg(windowsArgs, "--one-file-system") {
		t.Fatalf("windows args should not include one-file-system: %#v", windowsArgs)
	}
	if !containsArg(windowsArgs, "--use-fs-snapshot") {
		t.Fatalf("windows args missing fs snapshot: %#v", windowsArgs)
	}
}

func TestForgetFailureMarksRepoFailed(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "forget", fakeResticResponse{stderr: "prune failed", err: errors.New("exit status 1")})
	result, err := Run(context.Background(), cfg, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Repos[0].Success {
		t.Fatalf("forget failure should mark repo failed: %#v", result.Repos[0])
	}
	if !strings.Contains(result.Repos[0].Error, "retention prune failed") {
		t.Fatalf("error = %q", result.Repos[0].Error)
	}
}

func TestPruneModeExternalSkipsForget(t *testing.T) {
	cfg := testConfig(t)
	cfg.PruneMode = "external"
	runner := newFakeResticRunner()
	if _, err := Run(context.Background(), cfg, Options{Runner: runner}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, call := range runner.calls {
		if len(call.Args) > 0 && call.Args[0] == "forget" {
			t.Fatalf("forget should be skipped in external mode: %#v", runner.calls)
		}
	}
}

func TestStatusMergePreservesChecksForeignReposAndLastSuccessOnFailure(t *testing.T) {
	cfg := testConfig(t)
	checkLast := time.Date(2026, 6, 28, 4, 0, 0, 0, time.UTC)
	lastSuccess := time.Date(2026, 7, 2, 2, 0, 0, 0, time.UTC)
	checkSuccess := true
	existing := StatusFile{
		Version: statusVersion,
		Repos: []RepoStatus{
			{Name: "repo1", Engine: "restic", LastSuccess: &lastSuccess, CheckLast: &checkLast, CheckSuccess: &checkSuccess, Success: true, DurationS: 40, AddedBytes: 300, TotalBytes: 2000, SnapshotCount: 7, Snapshots: []Snapshot{{ID: "abcd1234"}}},
			{Name: "foreign", Engine: "borg", Success: true},
		},
	}
	if err := writeStatusAtomic(cfg.StatusPath, existing); err != nil {
		t.Fatalf("writeStatusAtomic: %v", err)
	}
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "backup", fakeResticResponse{stderr: "failed", err: errors.New("exit status 1")})
	if _, err := Run(context.Background(), cfg, Options{Runner: runner}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		t.Fatalf("readStatusFile: %v", err)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("repos = %#v", got.Repos)
	}
	repo := got.Repos[0]
	if repo.LastSuccess == nil || !repo.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("last_success = %v, want %v", repo.LastSuccess, lastSuccess)
	}
	if repo.CheckLast == nil || !repo.CheckLast.Equal(checkLast) || repo.CheckSuccess == nil || !*repo.CheckSuccess {
		t.Fatalf("check fields not preserved: %#v", repo)
	}
	if repo.Success || repo.Error == "" {
		t.Fatalf("failure not preserved: %#v", repo)
	}
	if repo.SnapshotCount != 7 || repo.TotalBytes != 2000 || len(repo.Snapshots) != 1 || repo.Snapshots[0].ID != "abcd1234" {
		t.Fatalf("repository facts not preserved: %#v", repo)
	}
	if repo.AddedBytes != 0 || repo.DurationS != 0 {
		t.Fatalf("per-run fields carried forward: %#v", repo)
	}
	if got.Repos[1].Name != "foreign" {
		t.Fatalf("foreign repo not preserved: %#v", got.Repos)
	}
}

func TestMergeStatusPreservesScopeForPartialUpdate(t *testing.T) {
	oneFileSystem := false
	existing := StatusFile{Repos: []RepoStatus{{
		Name:          "repo1",
		Paths:         []string{"/etc", "/srv"},
		Excludes:      []string{"/srv/cache"},
		OneFileSystem: &oneFileSystem,
	}}}
	merged := mergeStatus(existing, []RepoStatus{{Name: "repo1", Error: "partial update"}})
	if len(merged.Repos) != 1 || !reflect.DeepEqual(merged.Repos[0].Paths, existing.Repos[0].Paths) || !reflect.DeepEqual(merged.Repos[0].Excludes, existing.Repos[0].Excludes) || merged.Repos[0].OneFileSystem == nil || *merged.Repos[0].OneFileSystem {
		t.Fatalf("scope not preserved: %#v", merged.Repos)
	}
}

func TestMergeStatusPreservesPathStatsForPartialUpdate(t *testing.T) {
	statsAt := time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC)
	existing := StatusFile{Repos: []RepoStatus{{
		Name:          "repo1",
		PathStats:     []PathStat{{Path: "/etc", Bytes: 100, Files: 2}},
		StatsSnapshot: "abcdef12",
		StatsAt:       &statsAt,
		StatsScope:    "scope-hash",
	}}}
	merged := mergeStatus(existing, []RepoStatus{{Name: "repo1", Error: "partial update"}})
	if len(merged.Repos) != 1 || !reflect.DeepEqual(merged.Repos[0].PathStats, existing.Repos[0].PathStats) || merged.Repos[0].StatsSnapshot != "abcdef12" || merged.Repos[0].StatsAt == nil || !merged.Repos[0].StatsAt.Equal(statsAt) || merged.Repos[0].StatsScope != "scope-hash" {
		t.Fatalf("path stats not preserved: %#v", merged.Repos)
	}
}

func TestMergeStatusPreservesRepositoryFactsForPartialFailure(t *testing.T) {
	existing := StatusFile{Repos: []RepoStatus{{
		Name:          "repo1",
		SnapshotCount: 7,
		TotalBytes:    2000,
		Snapshots:     []Snapshot{{ID: "abcd1234"}},
	}}}
	merged := mergeStatus(existing, []RepoStatus{{Name: "repo1", Success: false, TotalBytes: 500}})
	if len(merged.Repos) != 1 || merged.Repos[0].TotalBytes != 500 || merged.Repos[0].SnapshotCount != 7 || len(merged.Repos[0].Snapshots) != 1 {
		t.Fatalf("repository facts not preserved: %#v", merged.Repos)
	}
	if &merged.Repos[0].Snapshots[0] == &existing.Repos[0].Snapshots[0] {
		t.Fatal("snapshots slice was not copied")
	}
}

func TestBrowseValidation(t *testing.T) {
	cfg := testConfig(t)
	cases := []struct {
		name string
		opts BrowseOptions
	}{
		{"bad snapshot", BrowseOptions{Repo: "repo1", Snapshot: "-latest", Path: "/", MaxEntries: 1}},
		{"relative path", BrowseOptions{Repo: "repo1", Snapshot: "latest", Path: "etc", MaxEntries: 1}},
		{"parent segment", BrowseOptions{Repo: "repo1", Snapshot: "latest", Path: "/etc/../root", MaxEntries: 1}},
		{"unknown repo", BrowseOptions{Repo: "missing", Snapshot: "latest", Path: "/", MaxEntries: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateBrowseOptions(cfg.Repos, tc.opts); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestBrowseResticArgsParsingSortAndCap(t *testing.T) {
	cfg := testConfig(t)
	mtime := "2026-07-17T08:30:00Z"
	listing := strings.Join([]string{
		`{"message_type":"snapshot","id":"abcdef123456"}`,
		`{"message_type":"node","name":"z.txt","type":"file","path":"/z.txt","size":9,"mtime":"` + mtime + `"}`,
		`{"message_type":"node","name":"beta","type":"dir","path":"/beta","mtime":"` + mtime + `"}`,
		`{"message_type":"node","name":"alpha","type":"dir","path":"/alpha","mtime":"` + mtime + `"}`,
		`{"message_type":"node","name":"overflow","type":"file","path":"/overflow","size":5}`,
	}, "\n")
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: listing})
	result, err := browseSnapshot(context.Background(), cfg, BrowseOptions{Repo: "repo1", Snapshot: "latest", Path: "/", MaxEntries: 3}, Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot != "abcdef123456" || !result.Truncated || len(result.Entries) != 3 {
		t.Fatalf("browse result = %#v", result)
	}
	if result.Entries[0].Name != "alpha" || result.Entries[1].Name != "beta" || result.Entries[2].Name != "z.txt" || result.Entries[2].Size != 9 || result.Entries[2].Mtime == nil {
		t.Fatalf("entries = %#v", result.Entries)
	}
	want := []string{"ls", "--json", "latest", "/"}
	if !reflect.DeepEqual(runner.calls[0].Args, want) {
		t.Fatalf("args = %#v, want %#v", runner.calls[0].Args, want)
	}

	recursiveRunner := newFakeResticRunner()
	recursiveRunner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: listing})
	if _, err := browseSnapshot(context.Background(), cfg, BrowseOptions{Repo: "repo1", Snapshot: "abcd", Path: "/etc", Recursive: true, MaxEntries: 10}, Options{Runner: recursiveRunner}); err != nil {
		t.Fatal(err)
	}
	wantRecursive := []string{"ls", "--json", "--recursive", "abcd", "/etc"}
	if !reflect.DeepEqual(recursiveRunner.calls[0].Args, wantRecursive) {
		t.Fatalf("recursive args = %#v, want %#v", recursiveRunner.calls[0].Args, wantRecursive)
	}
}

func TestBrowseSkipsRequestedDirectoryNode(t *testing.T) {
	cfg := testConfig(t)
	listing := strings.Join([]string{
		`{"message_type":"snapshot","id":"abcdef123456"}`,
		`{"message_type":"node","name":"app-db","type":"dir","path":"/e2e/src/data/volumes/app-db"}`,
		`{"message_type":"node","name":"uploads","type":"dir","path":"/e2e/src/data/volumes/uploads"}`,
		`{"message_type":"node","name":"volumes","type":"dir","path":"/e2e/src/data/volumes"}`,
	}, "\n")
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: listing})
	result, err := browseSnapshot(context.Background(), cfg, BrowseOptions{Repo: "repo1", Snapshot: "latest", Path: "/e2e/src/data/volumes/", MaxEntries: 10}, Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || result.Entries[0].Name != "app-db" || result.Entries[1].Name != "uploads" {
		t.Fatalf("entries = %#v", result.Entries)
	}

	fileRunner := newFakeResticRunner()
	fileRunner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: `{"message_type":"node","name":"config.json","type":"file","path":"/e2e/config.json","size":42}`})
	fileResult, err := browseSnapshot(context.Background(), cfg, BrowseOptions{Repo: "repo1", Snapshot: "latest", Path: "/e2e/config.json", MaxEntries: 10}, Options{Runner: fileRunner})
	if err != nil {
		t.Fatal(err)
	}
	if len(fileResult.Entries) != 1 || fileResult.Entries[0].Name != "config.json" || fileResult.Entries[0].Size != 42 {
		t.Fatalf("file entries = %#v", fileResult.Entries)
	}
}

func TestBrowseErrorClassification(t *testing.T) {
	cases := []struct {
		err  error
		kind string
	}{
		{fmt.Errorf("browse: %w", context.DeadlineExceeded), "timed_out"},
		{errors.New("snapshot browse failed: context deadline exceeded: load index files"), "timed_out"},
		{fmt.Errorf("read config: %w", os.ErrPermission), "insufficient_privilege"},
		{errors.New("another backup operation holds the tunnel lock; retry"), "busy"},
		{errors.New("snapshot browse failed: no matching ID found"), "not_found"},
		{errors.New("something else failed"), "failed"},
	}
	for _, tc := range cases {
		kind, message := ClassifyBrowseError(tc.err)
		if kind != tc.kind || message == "" {
			t.Fatalf("ClassifyBrowseError(%v) = %q, %q", tc.err, kind, message)
		}
	}

	cfg := testConfig(t)
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stderr: "no matching ID found in " + cfg.Repos[0].URL, err: errors.New("exit status 1")})
	_, err := browseSnapshot(context.Background(), cfg, BrowseOptions{Repo: "repo1", Snapshot: "abcd", Path: "/", MaxEntries: 10}, Options{Runner: runner})
	kind, message := ClassifyBrowseError(err)
	if kind != "not_found" || strings.Contains(message, cfg.Repos[0].URL) || !strings.Contains(message, "[repository]") {
		t.Fatalf("classified sanitized error = %q, %q", kind, message)
	}
}

func TestParseBackupSummarySnapshotID(t *testing.T) {
	summary, err := parseBackupSummary([]byte(`{"message_type":"summary","total_duration":1.2,"data_added":3,"total_bytes_processed":4,"snapshot_id":"abcdef123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	if summary.SnapshotID != "abcdef123456" {
		t.Fatalf("snapshot id = %q", summary.SnapshotID)
	}
}

func TestCollectPathStatsLongestPrefixAndZeroBuckets(t *testing.T) {
	cfg := testConfig(t)
	cfg.Paths = []string{"/etc", "/var/lib", "/var/lib/docker/volumes"}
	listing := strings.Join([]string{
		`{"message_type":"node","type":"file","path":"/var/lib/db/data","size":100}`,
		`{"message_type":"node","type":"file","path":"/var/lib/docker/volumes/app/db","size":250}`,
		`{"message_type":"node","type":"file","path":"/tmp/ignored","size":999}`,
		`{"message_type":"node","type":"dir","path":"/var/lib/docker/volumes/empty"}`,
	}, "\n")
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: listing})
	stats, err := collectPathStats(context.Background(), cfg, cfg.Repos[0], t.TempDir(), "abcdef", Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	want := []PathStat{{Path: "/etc"}, {Path: "/var/lib", Bytes: 100, Files: 1}, {Path: "/var/lib/docker/volumes", Bytes: 250, Files: 1}}
	if !reflect.DeepEqual(stats, want) {
		t.Fatalf("stats = %#v, want %#v", stats, want)
	}
}

func TestCollectPathStatsWindowsDriveAndCaseNormalization(t *testing.T) {
	cfg := testConfig(t)
	cfg.Paths = []string{`C:\Users`, `D:\Data`}
	listing := strings.Join([]string{
		`{"message_type":"node","type":"file","path":"/C/Users/jun/file","size":100}`,
		`{"message_type":"node","type":"file","path":"/c/uSeRs/JUN/second","size":250}`,
	}, "\n")
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: listing})
	stats, err := collectPathStats(context.Background(), cfg, cfg.Repos[0], t.TempDir(), "abcdef", Options{Runner: runner, GOOS: "windows"})
	if err != nil {
		t.Fatal(err)
	}
	want := []PathStat{{Path: `C:\Users`, Bytes: 350, Files: 2}, {Path: `D:\Data`}}
	if !reflect.DeepEqual(stats, want) {
		t.Fatalf("stats = %#v, want %#v", stats, want)
	}
}

func TestCollectPathStatsLinuxMatchingRemainsByteExact(t *testing.T) {
	cfg := testConfig(t)
	cfg.Paths = []string{"/Data"}
	listing := strings.Join([]string{
		`{"message_type":"node","type":"file","path":"/data/lower","size":100}`,
		`{"message_type":"node","type":"file","path":"/Data/exact","size":250}`,
	}, "\n")
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: listing})
	stats, err := collectPathStats(context.Background(), cfg, cfg.Repos[0], t.TempDir(), "abcdef", Options{Runner: runner, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	want := []PathStat{{Path: "/Data", Bytes: 250, Files: 1}}
	if !reflect.DeepEqual(stats, want) {
		t.Fatalf("stats = %#v, want %#v", stats, want)
	}
}

func TestPathStatsFailureDoesNotFailBackup(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "backup", fakeResticResponse{stdout: `{"message_type":"summary","total_duration":1,"snapshot_id":"abcdef123456"}`})
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stderr: "listing failed", err: errors.New("exit status 1")})
	status := runRepo(context.Background(), cfg, cfg.Repos[0], t.TempDir(), Options{Runner: runner}, nil, nil)
	if !status.Success || status.PathStats != nil || status.StatsSnapshot != "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestRunRepoReusesFreshPathStatsForSameScope(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	statsAt := now.Add(-time.Hour)
	previous := RepoStatus{
		Name:          cfg.Repos[0].Name,
		PathStats:     []PathStat{{Path: "/etc", Bytes: 100, Files: 2}},
		StatsSnapshot: "oldstats",
		StatsAt:       &statsAt,
		StatsScope:    pathStatsScopeFingerprint(cfg),
	}
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "backup", fakeResticResponse{stdout: `{"message_type":"summary","total_duration":1,"snapshot_id":"newbackup"}`})
	status := runRepo(context.Background(), cfg, cfg.Repos[0], t.TempDir(), Options{Runner: runner, Now: func() time.Time { return now }}, nil, &previous)
	if !status.Success || !reflect.DeepEqual(status.PathStats, previous.PathStats) || status.StatsSnapshot != previous.StatsSnapshot || status.StatsAt == nil || !status.StatsAt.Equal(statsAt) || status.StatsScope != previous.StatsScope {
		t.Fatalf("reused path stats = %#v", status)
	}
	for _, call := range runner.calls {
		if resticSubcommand(call.Args) == "ls" {
			t.Fatalf("restic ls invoked for fresh unchanged stats: %#v", runner.calls)
		}
	}
}

func TestRunRepoRecomputesPathStatsWhenScopeChangesOrStatsExpire(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		prepare func(*Config, *RepoStatus)
	}{
		{"scope changed", func(cfg *Config, _ *RepoStatus) { cfg.Excludes = append(cfg.Excludes, "/tmp") }},
		{"stats expired", func(_ *Config, previous *RepoStatus) {
			expired := now.Add(-25 * time.Hour)
			previous.StatsAt = &expired
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			statsAt := now.Add(-time.Hour)
			previous := RepoStatus{Name: cfg.Repos[0].Name, PathStats: []PathStat{{Path: "/etc", Bytes: 100, Files: 2}}, StatsSnapshot: "oldstats", StatsAt: &statsAt, StatsScope: pathStatsScopeFingerprint(cfg)}
			tc.prepare(&cfg, &previous)
			runner := newFakeResticRunner()
			runner.enqueue(cfg.Repos[0].URL, "backup", fakeResticResponse{stdout: `{"message_type":"summary","total_duration":1,"snapshot_id":"newstats"}`})
			runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: `{"message_type":"node","type":"file","path":"/etc/config","size":42}`})
			status := runRepo(context.Background(), cfg, cfg.Repos[0], t.TempDir(), Options{Runner: runner, Now: func() time.Time { return now }}, nil, &previous)
			lsCalls := 0
			for _, call := range runner.calls {
				if resticSubcommand(call.Args) == "ls" {
					lsCalls++
				}
			}
			if !status.Success || lsCalls != 1 || status.StatsSnapshot != "newstats" || status.StatsAt == nil || !status.StatsAt.Equal(now) || status.StatsScope != pathStatsScopeFingerprint(cfg) || len(status.PathStats) != 2 || status.PathStats[0].Bytes != 42 {
				t.Fatalf("recomputed path stats = %#v, ls calls %d", status, lsCalls)
			}
		})
	}
}

func TestWriteStatusAtomicLeavesValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	for i := 0; i < 3; i++ {
		status := StatusFile{Version: statusVersion, Repos: []RepoStatus{{Name: fmt.Sprintf("repo%d", i), Engine: "restic", Success: true}}}
		if err := writeStatusAtomic(path, status); err != nil {
			t.Fatalf("writeStatusAtomic: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var parsed StatusFile
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("invalid JSON after atomic write: %v\n%s", err, data)
		}
	}
}

func TestLockNoopAndStaleTakeover(t *testing.T) {
	cfg := testConfig(t)
	lockPath := filepath.Join(filepath.Dir(cfg.StatusPath), "backup.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d active\n", os.Getpid())), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	runner := newFakeResticRunner()
	result, err := Run(context.Background(), cfg, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Run locked: %v", err)
	}
	if !result.LockSkipped || len(runner.calls) != 0 {
		t.Fatalf("expected lock skip with no calls, result=%#v calls=%#v", result, runner.calls)
	}

	stale := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	result, err = Run(context.Background(), cfg, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Run old live lock: %v", err)
	}
	if !result.LockSkipped || len(runner.calls) != 0 {
		t.Fatalf("old live owner lock must not be stolen, result=%#v calls=%#v", result, runner.calls)
	}
	if err := os.WriteFile(lockPath, []byte("2147483647 dead\n"), 0o644); err != nil {
		t.Fatalf("write dead lock: %v", err)
	}
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("chtimes dead lock: %v", err)
	}
	result, err = Run(context.Background(), cfg, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Run stale: %v", err)
	}
	if !result.AnySucceeded() {
		t.Fatal("stale lock takeover should run backup")
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock should be removed after run, stat err=%v", err)
	}
}

func TestRunLockReleaseKeepsReplacementOwner(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status.json")
	release, skipped, err := acquireRunLock(statusPath, time.Now())
	if err != nil || skipped {
		t.Fatalf("acquireRunLock: skipped=%v err=%v", skipped, err)
	}
	lockPath := filepath.Join(filepath.Dir(statusPath), "backup.lock")
	replacement := []byte("2147483647 replacement\n")
	if err := os.WriteFile(lockPath, replacement, 0o644); err != nil {
		t.Fatalf("replace lock owner: %v", err)
	}
	release()
	got, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("replacement lock removed: %v", err)
	}
	if !bytes.Equal(got, replacement) {
		t.Fatalf("replacement lock changed: %q", got)
	}
}

func TestParseSnapshotsRestic019Summary(t *testing.T) {
	data := []byte(`[
		{
			"id": "b83f11b518b7937c941b1daabb9e67c251bb844c3c1d5627ff9138d7cf4a95df",
			"time": "2026-07-15T03:14:15.9265357Z",
			"paths": ["/etc", "/var/lib/servermonitor"],
			"summary": {
				"backup_start": "2026-07-15T03:14:15.9265357Z",
				"backup_end": "2026-07-15T03:14:23.1265357Z",
				"data_added": 1234567,
				"total_files_processed": 4321,
				"total_bytes_processed": 987654321
			}
		},
		{
			"id": "4b52136d96a19741556ee9e0a2648b01ab1b0858aa90291b6a16a0297aa19520",
			"time": "2022-01-02T01:02:03Z",
			"paths": ["/etc"],
			"summary": {
				"data_added": 7654321,
				"total_files_processed": 1234,
				"total_bytes_processed": 123456789
			}
		},
		{
			"id": "ddbfadf00d8ad68f04a47cf95d9e46e0d74fb1c93b15d9ef37b15fe9b977a12e",
			"time": "2021-01-02T01:02:03Z",
			"paths": ["/srv"]
		}
	]`)
	snapshots, err := ParseSnapshots(data)
	if err != nil {
		t.Fatalf("ParseSnapshots: %v", err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("snapshot count = %d, want 3", len(snapshots))
	}
	withSummary := snapshots[0]
	if withSummary.ID != "b83f11b5" {
		t.Fatalf("truncated id = %q", withSummary.ID)
	}
	if withSummary.SizeBytes == nil || *withSummary.SizeBytes != 987654321 {
		t.Fatalf("size bytes = %v", withSummary.SizeBytes)
	}
	if withSummary.AddedBytes == nil || *withSummary.AddedBytes != 1234567 {
		t.Fatalf("added bytes = %v", withSummary.AddedBytes)
	}
	if withSummary.FileCount == nil || *withSummary.FileCount != 4321 {
		t.Fatalf("file count = %v", withSummary.FileCount)
	}
	if withSummary.DurationS == nil || *withSummary.DurationS != 7.2 {
		t.Fatalf("duration seconds = %v", withSummary.DurationS)
	}
	withoutTimes := snapshots[1]
	if withoutTimes.SizeBytes == nil || *withoutTimes.SizeBytes != 123456789 || withoutTimes.AddedBytes == nil || *withoutTimes.AddedBytes != 7654321 || withoutTimes.FileCount == nil || *withoutTimes.FileCount != 1234 || withoutTimes.DurationS != nil {
		t.Fatalf("snapshot without backup times = size %v, added %v, files %v, duration %v", withoutTimes.SizeBytes, withoutTimes.AddedBytes, withoutTimes.FileCount, withoutTimes.DurationS)
	}
	withoutSummary := snapshots[2]
	if withoutSummary.SizeBytes != nil || withoutSummary.AddedBytes != nil || withoutSummary.FileCount != nil || withoutSummary.DurationS != nil {
		t.Fatalf("legacy snapshot summary = size %v, added %v, files %v, duration %v", withoutSummary.SizeBytes, withoutSummary.AddedBytes, withoutSummary.FileCount, withoutSummary.DurationS)
	}
}

func TestSnapshotInventoryPreservesSummaryAndClamps(t *testing.T) {
	var raw []map[string]any
	for i := 0; i < 55; i++ {
		raw = append(raw, map[string]any{
			"id":    fmt.Sprintf("%08dabcdef", i),
			"time":  "2026-07-03T02:00:00Z",
			"paths": []string{"/etc"},
			"summary": map[string]any{
				"total_bytes_processed": int64(1000 + i),
				"data_added":            int64(2000 + i),
				"total_files_processed": int64(3000 + i),
				"backup_start":          "2026-07-03T02:00:00Z",
				"backup_end":            "2026-07-03T02:00:09Z",
			},
		})
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snapshots, err := ParseSnapshots(data)
	if err != nil {
		t.Fatalf("ParseSnapshots: %v", err)
	}
	if len(snapshots) != 55 {
		t.Fatalf("snapshot count = %d", len(snapshots))
	}
	if snapshots[0].ID != "00000000" {
		t.Fatalf("truncated id = %q", snapshots[0].ID)
	}
	inventory := snapshotInventory(snapshots)
	if len(inventory) != 50 {
		t.Fatalf("inventory = %d, want 50", len(inventory))
	}
	if inventory[0].ID != "00000005" || inventory[49].ID != "00000054" {
		t.Fatalf("inventory range = %q..%q", inventory[0].ID, inventory[49].ID)
	}
	if inventory[0].SizeBytes == nil || *inventory[0].SizeBytes != 1005 {
		t.Fatalf("inventory size bytes = %v", inventory[0].SizeBytes)
	}
	if inventory[0].AddedBytes == nil || *inventory[0].AddedBytes != 2005 {
		t.Fatalf("inventory added bytes = %v", inventory[0].AddedBytes)
	}
	if inventory[0].FileCount == nil || *inventory[0].FileCount != 3005 {
		t.Fatalf("inventory file count = %v", inventory[0].FileCount)
	}
	if inventory[0].DurationS == nil || *inventory[0].DurationS != 9 {
		t.Fatalf("inventory duration seconds = %v", inventory[0].DurationS)
	}
}

func TestRunSnapshotCountComesFromFullList(t *testing.T) {
	cfg := testConfig(t)
	var raw []map[string]any
	for i := 0; i < 55; i++ {
		raw = append(raw, map[string]any{
			"id":    fmt.Sprintf("%08dabcdef", i),
			"time":  "2026-07-03T02:00:00Z",
			"paths": []string{"/etc"},
		})
	}
	data, _ := json.Marshal(raw)
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "snapshots", fakeResticResponse{stdout: string(data)})
	result, err := Run(context.Background(), cfg, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Repos[0].SnapshotCount != 55 {
		t.Fatalf("snapshot count = %d", result.Repos[0].SnapshotCount)
	}
	if len(result.Repos[0].Snapshots) != 50 {
		t.Fatalf("inventory = %d", len(result.Repos[0].Snapshots))
	}
}

func TestInitAlreadyFreshAndMissingKey(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	keyReady := filepath.Join(dir, "ready.key")
	keyFresh := filepath.Join(dir, "fresh.key")
	if err := os.WriteFile(keyReady, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(keyFresh, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cfg.Repos = []Repo{
		{Name: "ready", URL: "rest:https://example.com/ready", PasswordFile: keyReady},
		{Name: "fresh", URL: "rest:https://example.com/fresh", PasswordFile: keyFresh},
		{Name: "missing", URL: "rest:https://example.com/missing", PasswordFile: filepath.Join(dir, "missing.key")},
	}
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[1].URL, "cat", fakeResticResponse{stderr: "repository does not exist", err: errors.New("exit status 1")})
	result, err := Init(context.Background(), cfg, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !result.Failed() {
		t.Fatal("missing key should make init fail")
	}
	if !result.Repos[0].Skipped {
		t.Fatalf("ready repo should be skipped: %#v", result.Repos[0])
	}
	if !result.Repos[1].Initialized {
		t.Fatalf("fresh repo should be initialized: %#v", result.Repos[1])
	}
	if !strings.Contains(result.Repos[2].Error, "installer") {
		t.Fatalf("missing key error should mention installer: %q", result.Repos[2].Error)
	}
}

func TestLoadRepoEnvAllowlistAndRegion(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "creds.env")
	body := "AWS_ACCESS_KEY_ID=AKIA123\nAWS_SECRET_ACCESS_KEY= topsecret \n\nAWS_DEFAULT_REGION=eu-central-1\n"
	if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	repo := Repo{Name: "repo1", URL: "s3:https://example/bucket", EnvFile: envPath, S3Region: "us-east-1"}
	env, err := loadRepoEnv(repo)
	if err != nil {
		t.Fatalf("loadRepoEnv: %v", err)
	}
	if env["AWS_ACCESS_KEY_ID"] != "AKIA123" || env["AWS_SECRET_ACCESS_KEY"] != "topsecret" {
		t.Fatalf("credentials not parsed/trimmed: %#v", env)
	}
	if env["AWS_DEFAULT_REGION"] != "eu-central-1" {
		t.Fatalf("env-file region should win over s3_region: %q", env["AWS_DEFAULT_REGION"])
	}

	regionOnly, err := loadRepoEnv(Repo{Name: "repo1", URL: "s3:https://example/bucket", S3Region: "us-east-1"})
	if err != nil {
		t.Fatalf("loadRepoEnv region only: %v", err)
	}
	if regionOnly["AWS_DEFAULT_REGION"] != "us-east-1" {
		t.Fatalf("s3_region not injected without env file: %#v", regionOnly)
	}

	badPath := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(badPath, []byte("LD_PRELOAD=/evil.so\n"), 0o600); err != nil {
		t.Fatalf("write bad env: %v", err)
	}
	_, err = loadRepoEnv(Repo{Name: "repo1", EnvFile: badPath})
	if err == nil || !strings.Contains(err.Error(), "LD_PRELOAD") {
		t.Fatalf("disallowed key should be rejected by name, got %v", err)
	}
}

func TestWithResticEnvPrecedence(t *testing.T) {
	repo := Repo{URL: "s3:https://example/bucket", PasswordFile: "/key"}
	repoEnv := map[string]string{
		"RESTIC_REPOSITORY": "s3:https://evil/override",
		"AWS_ACCESS_KEY_ID": "AKIA123",
	}
	env := withResticEnv(repo, "/cache", "", repoEnv)
	if envValue(env, "RESTIC_REPOSITORY") != "s3:https://example/bucket" {
		t.Fatalf("repo URL must win over env file: %q", envValue(env, "RESTIC_REPOSITORY"))
	}
	if envValue(env, "RESTIC_PASSWORD_FILE") != "/key" || envValue(env, "RESTIC_CACHE_DIR") != "/cache" {
		t.Fatalf("restic env not set: %#v", env)
	}
	if envValue(env, "AWS_ACCESS_KEY_ID") != "AKIA123" {
		t.Fatalf("credential not threaded: %#v", env)
	}
}

func TestWithResticEnvStripsAmbientPasswordOverrides(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "ambient-plaintext")
	t.Setenv("RESTIC_PASSWORD_COMMAND", "echo leak")
	t.Setenv("RESTIC_REPOSITORY_FILE", "/tmp/other-repo")
	env := withResticEnv(Repo{URL: "rest:https://example/repo", PasswordFile: "/key"}, "/cache", "", map[string]string{})
	for _, key := range []string{"RESTIC_PASSWORD", "RESTIC_PASSWORD_COMMAND", "RESTIC_REPOSITORY_FILE"} {
		if envValue(env, key) != "" {
			t.Fatalf("%s should be stripped so the configured password_file/repo wins, got %q", key, envValue(env, key))
		}
	}
	if envValue(env, "RESTIC_PASSWORD_FILE") != "/key" {
		t.Fatalf("password file not set: %#v", env)
	}
}

func TestResticGlobalArgsPathStyle(t *testing.T) {
	base := []string{"snapshots", "--json"}
	if got := resticGlobalArgs(Repo{}, base); !reflect.DeepEqual(got, base) {
		t.Fatalf("non path-style args changed: %#v", got)
	}
	got := resticGlobalArgs(Repo{S3PathStyle: true}, base)
	want := []string{"-o", "s3.bucket-lookup=path", "snapshots", "--json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("path-style args = %#v, want %#v", got, want)
	}
}

func TestSanitizeResticTextScrubsSecrets(t *testing.T) {
	repo := Repo{URL: "s3:https://example/bucket/host1"}
	repoEnv := map[string]string{
		"AWS_SECRET_ACCESS_KEY": "topsecret",
		"RESTIC_REST_PASSWORD":  "hunter2",
		"AWS_DEFAULT_REGION":    "us-east-1",
	}
	text := "failed against s3:https://example/bucket/host1 using topsecret / hunter2 in us-east-1"
	got := sanitizeResticText(text, repo, repoEnv)
	if strings.Contains(got, "topsecret") || strings.Contains(got, "hunter2") {
		t.Fatalf("secrets leaked: %q", got)
	}
	if strings.Contains(got, "s3:https://example/bucket/host1") {
		t.Fatalf("repo url leaked: %q", got)
	}
	if !strings.Contains(got, "us-east-1") {
		t.Fatalf("region should not be scrubbed: %q", got)
	}
}

func TestSanitizeResticTextScrubsOverlappingSecretsLongestFirst(t *testing.T) {
	repoEnv := map[string]string{
		"RESTIC_REST_USERNAME": "tenant",
		"RESTIC_REST_PASSWORD": "tenant-supersecret",
	}
	got := sanitizeResticText("login tenant with tenant-supersecret", Repo{}, repoEnv)
	for _, leaked := range []string{"tenant", "supersecret"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("overlapping secret leaked %q in %q", leaked, got)
		}
	}
}

func TestSanitizeResticTextUnwrapsLastExitError(t *testing.T) {
	text := "repository lookup failed\n" +
		`{"message_type":"exit_error","code":1,"message":"first failure"}` + "\n" +
		`{"message_type":"exit_error","code":1,"message":"no matching ID found for prefix \"ffffffff\""}`
	want := "repository lookup failed\nno matching ID found for prefix \"ffffffff\""
	if got := sanitizeResticText(text, Repo{}, nil); got != want {
		t.Fatalf("sanitizeResticText() = %q, want %q", got, want)
	}
}

func TestSanitizeResticTextLeavesNonJSONUnchanged(t *testing.T) {
	text := "unable to open repository: connection refused"
	if got := sanitizeResticText(text, Repo{}, nil); got != text {
		t.Fatalf("sanitizeResticText() = %q, want %q", got, text)
	}
}

func TestSanitizeResticTextScrubsExtractedExitErrorMessage(t *testing.T) {
	repo := Repo{URL: "rest:https://backup.example/repo"}
	repoEnv := map[string]string{"RESTIC_REST_PASSWORD": "supersecret"}
	text := `{"message_type":"exit_error","code":1,"message":"rest:https://backup.example/repo rejected supersecret"}`
	want := "[repository] rejected [credential]"
	if got := sanitizeResticText(text, repo, repoEnv); got != want {
		t.Fatalf("sanitizeResticText() = %q, want %q", got, want)
	}
}

func TestPathStyleThreadedThroughRun(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repos = []Repo{{
		Name:         "s3repo",
		URL:          "s3:https://minio.example/bucket/host1",
		PasswordFile: filepath.Join(t.TempDir(), "key"),
		S3PathStyle:  true,
	}}
	runner := newFakeResticRunner()
	if _, err := Run(context.Background(), cfg, Options{Runner: runner}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runner.calls) == 0 {
		t.Fatal("no restic calls recorded")
	}
	for _, call := range runner.calls {
		if len(call.Args) < 2 || call.Args[0] != "-o" || call.Args[1] != "s3.bucket-lookup=path" {
			t.Fatalf("path-style option missing from call: %#v", call.Args)
		}
	}
}

func TestRunWithFakeResticExecutable(t *testing.T) {
	cfg := testConfig(t)
	cfg.ResticPath = writeFakeResticExecutable(t)
	result, err := Run(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.AnySucceeded() {
		t.Fatalf("expected success: %#v", result)
	}
	status, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if len(status.Repos) != 1 || !status.Repos[0].Success || status.Repos[0].AddedBytes != 100 {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestExecRunnerStreamsLinesAndPreservesStdout(t *testing.T) {
	path := writeFakeResticExecutable(t)
	var lines [][]byte
	result, err := (ExecRunner{}).Run(context.Background(), Command{
		Path: path,
		Args: []string{"backup"},
		Env:  os.Environ(),
		OnStdoutLine: func(line []byte) {
			lines = append(lines, append([]byte(nil), line...))
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(lines) != 2 || !bytes.Contains(lines[0], []byte(`"message_type":"status"`)) {
		t.Fatalf("streamed lines = %q", lines)
	}
	if !bytes.Contains(result.Stdout, lines[0]) || !bytes.Contains(result.Stdout, lines[1]) {
		t.Fatalf("stdout = %q, lines = %q", result.Stdout, lines)
	}
}

func writeFakeResticExecutable(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "restic.cmd")
		body := `@echo off
if "%1"=="backup" (
  echo {"message_type":"status","percent_done":0.5,"bytes_done":100,"total_bytes":200}
  echo {"message_type":"summary","total_duration":4.4,"data_added":100,"total_bytes_processed":200}
  exit /b 0
)
if "%1"=="snapshots" (
  echo [{"id":"abcdef123456","time":"2026-07-03T02:00:00Z","paths":["/etc"]}]
  exit /b 0
)
if "%1"=="forget" exit /b 0
exit /b 1
`
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("write fake restic: %v", err)
		}
		return path
	}
	path := filepath.Join(dir, "restic")
	body := `#!/bin/sh
case "$1" in
backup)
printf '%s\n' '{"message_type":"status","percent_done":0.5,"bytes_done":100,"total_bytes":200}'
printf '%s\n' '{"message_type":"summary","total_duration":4.4,"data_added":100,"total_bytes_processed":200}'
exit 0
;;
snapshots)
printf '%s\n' '[{"id":"abcdef123456","time":"2026-07-03T02:00:00Z","paths":["/etc"]}]'
exit 0
;;
forget)
exit 0
;;
esac
exit 1
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake restic: %v", err)
	}
	return path
}
