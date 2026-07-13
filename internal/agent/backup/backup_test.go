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

func newFakeResticRunner() *fakeResticRunner {
	return &fakeResticRunner{responses: map[string][]fakeResticResponse{}}
}

func (r *fakeResticRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	r.calls = append(r.calls, Command{
		Path: command.Path,
		Args: append([]string(nil), command.Args...),
		Env:  append([]string(nil), command.Env...),
	})
	key := resticCallKey(command)
	queue := r.responses[key]
	if len(queue) > 0 {
		response := queue[0]
		r.responses[key] = queue[1:]
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
		cfg.Repos[0].URL: {"forget", "--prune", "--keep-daily", "30", "--keep-monthly", "6"},
		cfg.Repos[1].URL: {"forget", "--prune", "--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forget args by repo = %#v, want %#v", got, want)
	}
}

func TestForgetArgsOmitZeroRetentionValues(t *testing.T) {
	got := forgetArgs(Retention{Daily: 0, Weekly: 4, Monthly: 0})
	want := []string{"forget", "--prune", "--keep-weekly", "4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forget args = %#v, want %#v", got, want)
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
			{Name: "repo1", Engine: "restic", LastSuccess: &lastSuccess, CheckLast: &checkLast, CheckSuccess: &checkSuccess, Success: true},
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
	if got.Repos[1].Name != "foreign" {
		t.Fatalf("foreign repo not preserved: %#v", got.Repos)
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

func TestSnapshotsParseCountInventoryAndIDTruncation(t *testing.T) {
	var raw []map[string]any
	for i := 0; i < 55; i++ {
		raw = append(raw, map[string]any{
			"id":    fmt.Sprintf("%08dabcdef", i),
			"time":  "2026-07-03T02:00:00Z",
			"paths": []string{"/etc"},
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
	env := withResticEnv(repo, "/cache", repoEnv)
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
	env := withResticEnv(Repo{URL: "rest:https://example/repo", PasswordFile: "/key"}, "/cache", map[string]string{})
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

func writeFakeResticExecutable(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "restic.cmd")
		body := `@echo off
if "%1"=="backup" (
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
