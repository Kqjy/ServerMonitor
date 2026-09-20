package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseInstance(t *testing.T) {
	cases := []struct {
		name     string
		instance string
		repo     string
		snapshot string
		includes []string
		wantErr  bool
	}{
		{name: "repo and snapshot", instance: "repo1:abcd1234", repo: "repo1", snapshot: "abcd1234"},
		{name: "with includes", instance: "repo1:abcd1234:/etc/nginx,/etc/hosts", repo: "repo1", snapshot: "abcd1234", includes: []string{"/etc/nginx", "/etc/hosts"}},
		{name: "trims blank includes", instance: "r:s:/a, ,/b", repo: "r", snapshot: "s", includes: []string{"/a", "/b"}},
		{name: "missing snapshot", instance: "repo1", wantErr: true},
		{name: "empty repo", instance: ":abcd", wantErr: true},
		{name: "empty snapshot", instance: "repo1:", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, snapshot, includes, err := parseInstance(tc.instance)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInstance: %v", err)
			}
			if repo != tc.repo || snapshot != tc.snapshot {
				t.Fatalf("repo/snapshot = %q/%q, want %q/%q", repo, snapshot, tc.repo, tc.snapshot)
			}
			if !reflect.DeepEqual(includes, tc.includes) {
				t.Fatalf("includes = %#v, want %#v", includes, tc.includes)
			}
		})
	}
}

func TestResolveRestoreOptionsExclusivity(t *testing.T) {
	if _, err := resolveRestoreOptions(RestoreOptions{Repo: "r", Snapshot: "s", InPlace: true, Target: "x"}); err == nil {
		t.Fatal("in-place with target should be rejected")
	}
	if _, err := resolveRestoreOptions(RestoreOptions{Instance: "r:s", Repo: "r", InPlace: true}); err == nil {
		t.Fatal("instance with repo should be rejected")
	}
	if _, err := resolveRestoreOptions(RestoreOptions{Instance: "r:s"}); err == nil {
		t.Fatal("instance without in-place should be rejected")
	}
	if _, err := resolveRestoreOptions(RestoreOptions{Snapshot: "s"}); err == nil {
		t.Fatal("missing repo should be rejected")
	}
	if _, err := resolveRestoreOptions(RestoreOptions{Repo: "r"}); err == nil {
		t.Fatal("missing snapshot should be rejected")
	}
	ro, err := resolveRestoreOptions(RestoreOptions{Instance: "r:s:/etc", InPlace: true})
	if err != nil {
		t.Fatalf("resolveRestoreOptions: %v", err)
	}
	if ro.Repo != "r" || ro.Snapshot != "s" || !reflect.DeepEqual(ro.Includes, []string{"/etc"}) {
		t.Fatalf("instance not applied: %#v", ro)
	}
}

func TestContainedRestoreTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and absolute-path semantics differ on windows")
	}
	root := filepath.Join(t.TempDir(), "restore")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := containedRestoreTarget(root, "abcd1234", "")
	if err != nil {
		t.Fatalf("default target: %v", err)
	}
	if got != filepath.Join(root, "abcd1234") {
		t.Fatalf("default target = %q", got)
	}

	if _, err := containedRestoreTarget(root, "abcd", "../../etc"); err == nil {
		t.Fatal("relative .. escape should be rejected")
	}
	if _, err := containedRestoreTarget(root, "abcd", "/etc/passwd"); err == nil {
		t.Fatal("absolute path outside root should be rejected")
	}
	if _, err := containedRestoreTarget(root, "with/slash", ""); err == nil {
		t.Fatal("snapshot id with separator should be rejected")
	}

	inside, err := containedRestoreTarget(root, "abcd", "nested/dir")
	if err != nil {
		t.Fatalf("nested target: %v", err)
	}
	if inside != filepath.Join(root, "nested", "dir") {
		t.Fatalf("nested target = %q", inside)
	}

	escape := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(escape, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(escape, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := containedRestoreTarget(root, "abcd", "link/data"); err == nil {
		t.Fatal("symlinked ancestor escaping the root should be rejected")
	}
}

func TestEnsureEmptyTarget(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	if err := ensureEmptyTarget(missing); err != nil {
		t.Fatalf("missing target should be allowed: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := ensureEmptyTarget(empty); err != nil {
		t.Fatalf("empty dir should be allowed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(empty, "f"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ensureEmptyTarget(empty); err == nil {
		t.Fatal("non-empty dir should be rejected")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ensureEmptyTarget(file); err == nil {
		t.Fatal("existing file should be rejected")
	}
}

func TestRestoreArgs(t *testing.T) {
	got := restoreArgs("abcd1234", "/var/lib/servermonitor/restore/abcd1234", []string{"/etc/nginx", " ", "/etc/hosts"})
	want := []string{"restore", "abcd1234", "--json", "--target", "/var/lib/servermonitor/restore/abcd1234", "--include", "/etc/nginx", "--include", "/etc/hosts"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restoreArgs = %#v, want %#v", got, want)
	}
}

func TestRestoreLockNoop(t *testing.T) {
	cfg := testConfig(t)
	lockPath := filepath.Join(filepath.Dir(cfg.StatusPath), "backup.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("123\n"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	runner := newFakeResticRunner()
	result, err := Restore(context.Background(), cfg, RestoreOptions{Repo: "repo1", Snapshot: "abcd"}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !result.LockSkipped || len(runner.calls) != 0 {
		t.Fatalf("expected lock skip with no calls: %#v %#v", result, runner.calls)
	}
	stagingDir := filepath.Join(filepath.Dir(cfg.StatusPath), "restore", "abcd")
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Fatalf("lock skip should not leave a staging dir behind, stat err=%v", err)
	}
}

func TestRestoreBuildsIncludeArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("restore target root semantics differ on windows")
	}
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "restore", fakeResticResponse{stdout: `{"message_type":"summary","files_restored":2,"total_files":2,"bytes_restored":10,"total_bytes":10,"files_skipped":0}` + "\n"})
	result, err := Restore(context.Background(), cfg, RestoreOptions{
		Repo:     "repo1",
		Snapshot: "abcd1234",
		Includes: []string{"/etc/hosts"},
	}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if result.Summary == nil || result.Summary.FilesRestored != 2 {
		t.Fatalf("summary = %#v", result.Summary)
	}
	var restoreCall *Command
	for i := range runner.calls {
		if len(runner.calls[i].Args) > 0 && runner.calls[i].Args[0] == "restore" {
			restoreCall = &runner.calls[i]
		}
	}
	if restoreCall == nil {
		t.Fatal("no restore call recorded")
	}
	if !containsArg(restoreCall.Args, "--include") || !containsArg(restoreCall.Args, "/etc/hosts") {
		t.Fatalf("include not passed through: %#v", restoreCall.Args)
	}
	if !containsArg(restoreCall.Args, "abcd1234") {
		t.Fatalf("snapshot not passed: %#v", restoreCall.Args)
	}
	wantTarget := filepath.Join(filepath.Dir(cfg.StatusPath), "restore", "abcd1234")
	if !containsArg(restoreCall.Args, wantTarget) {
		t.Fatalf("target not passed: %#v want %q", restoreCall.Args, wantTarget)
	}
	if _, err := os.Stat(wantTarget); err != nil {
		t.Fatalf("target dir not created: %v", err)
	}
}

func TestRestoreInPlaceUsesRootTarget(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "restore", fakeResticResponse{stdout: ""})
	result, err := Restore(context.Background(), cfg, RestoreOptions{Repo: "repo1", Snapshot: "abcd", InPlace: true}, Options{Runner: runner, GOOS: "linux"})
	if err != nil {
		t.Fatalf("Restore in-place: %v", err)
	}
	if !result.InPlace {
		t.Fatal("result should be in-place")
	}
	var restoreCall *Command
	for i := range runner.calls {
		if len(runner.calls[i].Args) > 0 && runner.calls[i].Args[0] == "restore" {
			restoreCall = &runner.calls[i]
		}
	}
	if restoreCall == nil || !containsArg(restoreCall.Args, "/") {
		t.Fatalf("in-place restore should target /: %#v", restoreCall)
	}
}

func TestRestoreInPlaceRefusedOnWindows(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	_, err := Restore(context.Background(), cfg, RestoreOptions{Repo: "repo1", Snapshot: "abcd", InPlace: true}, Options{Runner: runner, GOOS: "windows"})
	if err == nil {
		t.Fatal("in-place restore on windows should be refused")
	}
	if !strings.Contains(err.Error(), "not supported on Windows") {
		t.Fatalf("error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("restic should not run: %#v", runner.calls)
	}
}

func TestDrillLivePath(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		goos     string
		hostRoot string
		want     string
	}{
		{name: "windows drive", path: "/C/Users/foo/file.txt", goos: "windows", want: `C:\Users\foo\file.txt`},
		{name: "windows lowercase drive", path: "/d/data/x", goos: "windows", want: `d:\data\x`},
		{name: "windows drive root", path: "/C", goos: "windows", want: `C:\`},
		{name: "windows non-drive prefix", path: "/CC/Users/foo", goos: "windows", want: `\CC\Users\foo`},
		{name: "linux untouched", path: "/etc/nginx/nginx.conf", goos: "linux", want: "/etc/nginx/nginx.conf"},
		{name: "linux single-letter dir untouched", path: "/C/data", goos: "linux", want: "/C/data"},
		{name: "linux host root prefix", path: "/etc/hosts", goos: "linux", hostRoot: "/host", want: "/host/etc/hosts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := drillLivePath(tc.path, tc.goos, tc.hostRoot); got != tc.want {
				t.Fatalf("drillLivePath(%q, %q, %q) = %q, want %q", tc.path, tc.goos, tc.hostRoot, got, tc.want)
			}
		})
	}
}

func TestValidateReadDataSubset(t *testing.T) {
	valid := []string{"", "5%", "12.5%", "250K", "1M", "3G", "2T", "1/5", "5/5", "10m"}
	for _, v := range valid {
		if err := validateReadDataSubset(v); err != nil {
			t.Fatalf("validateReadDataSubset(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{"abc", "5", "%", "5%%", "1/", "/5", "0/5", "6/5", "1/0", "5X", "-5%", "5 %"}
	for _, v := range invalid {
		if err := validateReadDataSubset(v); err == nil {
			t.Fatalf("validateReadDataSubset(%q) = nil, want error", v)
		}
	}
}

func TestCheckArgs(t *testing.T) {
	if got := checkArgs(""); !reflect.DeepEqual(got, []string{"check"}) {
		t.Fatalf("checkArgs empty = %#v", got)
	}
	if got := checkArgs("5%"); !reflect.DeepEqual(got, []string{"check", "--read-data-subset", "5%"}) {
		t.Fatalf("checkArgs = %#v", got)
	}
}

func TestCheckUnknownRepo(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	if _, err := Check(context.Background(), cfg, CheckOptions{Repo: "nope"}, Options{Runner: runner}); err == nil {
		t.Fatal("unknown repo should error")
	}
}

func TestCheckContinuesAndExitLogic(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repos = append(cfg.Repos, Repo{Name: "repo2", URL: "rest:https://secret@example.com/repo2", PasswordFile: filepath.Join(t.TempDir(), "key2")})
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "check", fakeResticResponse{stderr: "check failed for " + cfg.Repos[0].URL, err: errors.New("exit status 1")})
	runner.enqueue(cfg.Repos[1].URL, "check", fakeResticResponse{})
	result, err := Check(context.Background(), cfg, CheckOptions{}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(result.Repos) != 2 {
		t.Fatalf("repos = %d", len(result.Repos))
	}
	if result.Repos[0].Success || !result.Repos[1].Success {
		t.Fatalf("unexpected success flags: %#v", result.Repos)
	}
	if result.AllSucceeded() {
		t.Fatal("a failed repo should make the run fail")
	}
	if strings.Contains(result.Repos[0].Error, cfg.Repos[0].URL) {
		t.Fatalf("check error leaked repo url: %q", result.Repos[0].Error)
	}
}

func TestCheckStatusMergePreservesBackupFields(t *testing.T) {
	cfg := testConfig(t)
	started := time.Date(2026, 7, 2, 2, 0, 0, 0, time.UTC)
	lastSuccess := time.Date(2026, 7, 2, 2, 5, 0, 0, time.UTC)
	existing := StatusFile{
		Version: statusVersion,
		Repos: []RepoStatus{
			{
				Name:          "repo1",
				Engine:        "restic",
				LastStarted:   &started,
				LastFinished:  &lastSuccess,
				LastSuccess:   &lastSuccess,
				Success:       true,
				Error:         "prior backup error",
				DurationS:     42,
				AddedBytes:    1000,
				TotalBytes:    2000,
				SnapshotCount: 7,
				Snapshots:     []Snapshot{{ID: "abcd1234", Time: started, Paths: []string{"/etc"}}},
			},
			{Name: "foreign", Engine: "borg", Success: true},
		},
	}
	if err := writeStatusAtomic(cfg.StatusPath, existing); err != nil {
		t.Fatalf("writeStatusAtomic: %v", err)
	}
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "check", fakeResticResponse{})
	now := time.Date(2026, 7, 3, 4, 0, 0, 0, time.UTC)
	if _, err := Check(context.Background(), cfg, CheckOptions{}, Options{Runner: runner, Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		t.Fatalf("readStatusFile: %v", err)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("repos = %#v", got.Repos)
	}
	repo := got.Repos[0]
	if repo.CheckLast == nil || !repo.CheckLast.Equal(now.Truncate(time.Second)) {
		t.Fatalf("check_last = %v", repo.CheckLast)
	}
	if repo.CheckSuccess == nil || !*repo.CheckSuccess {
		t.Fatalf("check_success = %v", repo.CheckSuccess)
	}
	if repo.SnapshotCount != 7 || len(repo.Snapshots) != 1 || repo.DurationS != 42 || repo.AddedBytes != 1000 {
		t.Fatalf("backup fields clobbered: %#v", repo)
	}
	if repo.LastSuccess == nil || !repo.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("last_success not preserved: %v", repo.LastSuccess)
	}
	if repo.Error != "prior backup error" {
		t.Fatalf("prior backup error erased: %q", repo.Error)
	}
	if got.Repos[1].Name != "foreign" {
		t.Fatalf("foreign repo not preserved: %#v", got.Repos)
	}
}

func TestCheckCreatesMinimalEntryForNewRepo(t *testing.T) {
	cfg := testConfig(t)
	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "check", fakeResticResponse{})
	if _, err := Check(context.Background(), cfg, CheckOptions{}, Options{Runner: runner}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		t.Fatalf("readStatusFile: %v", err)
	}
	if len(got.Repos) != 1 || got.Repos[0].Name != "repo1" || got.Repos[0].Engine != "restic" {
		t.Fatalf("minimal entry not created: %#v", got.Repos)
	}
	if got.Repos[0].CheckLast == nil || got.Repos[0].CheckSuccess == nil {
		t.Fatalf("check fields missing: %#v", got.Repos[0])
	}
}

func TestCheckRemovesAbandonedLockAndRetries(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	url := cfg.Repos[0].URL
	runner := newFakeResticRunner()
	runner.enqueue(url, "check", fakeResticResponse{stderr: "unable to create lock in backend: repository is already locked by PID 1450852 on ovh-sg", err: errors.New("exit status 11")})
	runner.enqueue(url, "list", fakeResticResponse{stdout: "acbb474cd1\n"})
	runner.enqueue(url, "cat", fakeResticResponse{stdout: `{"time":"2026-08-21T15:34:17.068522493Z","exclusive":false,"hostname":"ovh-sg","pid":1450852}`})
	runner.enqueue(url, "unlock", fakeResticResponse{stdout: "successfully removed 1 locks"})
	runner.enqueue(url, "check", fakeResticResponse{})
	result, err := Check(context.Background(), cfg, CheckOptions{}, Options{Runner: runner, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(result.Repos) != 1 || !result.Repos[0].Success {
		t.Fatalf("check should succeed after the abandoned lock is removed: %#v", result.Repos)
	}
	unlocked := false
	for _, call := range runner.calls {
		if resticSubcommand(call.Args) == "unlock" && containsArg(call.Args, "--remove-all") {
			unlocked = true
		}
	}
	if !unlocked {
		t.Fatalf("expected an unlock --remove-all call: %#v", runner.calls)
	}
	status, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		t.Fatalf("readStatusFile: %v", err)
	}
	if status.Repos[0].CheckError != "" {
		t.Fatalf("check error should be cleared after a passing retry: %q", status.Repos[0].CheckError)
	}
}

func TestCheckKeepsRecentLock(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	url := cfg.Repos[0].URL
	runner := newFakeResticRunner()
	runner.enqueue(url, "check", fakeResticResponse{stderr: "unable to create lock in backend: repository is already locked", err: errors.New("exit status 11")})
	runner.enqueue(url, "list", fakeResticResponse{stdout: "acbb474cd1"})
	runner.enqueue(url, "cat", fakeResticResponse{stdout: `{"time":"2026-09-07T07:40:00Z","exclusive":false,"hostname":"ovh-sg","pid":42}`})
	result, err := Check(context.Background(), cfg, CheckOptions{}, Options{Runner: runner, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Repos[0].Success {
		t.Fatal("a recent lock must not be removed, so the check stays failed")
	}
	for _, call := range runner.calls {
		if resticSubcommand(call.Args) == "unlock" {
			t.Fatalf("a lock younger than the threshold must not be removed: %#v", call.Args)
		}
	}
	status, err := readStatusFile(cfg.StatusPath)
	if err != nil {
		t.Fatalf("readStatusFile: %v", err)
	}
	if !strings.Contains(status.Repos[0].CheckError, "already locked") {
		t.Fatalf("check error not recorded: %q", status.Repos[0].CheckError)
	}
}

func TestUnlockRemovesOnlyOldLocks(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	url := cfg.Repos[0].URL
	runner := newFakeResticRunner()
	runner.enqueue(url, "list", fakeResticResponse{stdout: "aaaa1111 bbbb2222"})
	runner.enqueue(url, "cat", fakeResticResponse{stdout: `{"time":"2026-08-21T15:34:17Z","hostname":"ovh-sg","pid":1}`})
	runner.enqueue(url, "cat", fakeResticResponse{stdout: `{"time":"2026-09-07T07:59:00Z","hostname":"ovh-sg","pid":2}`})
	result, err := Unlock(context.Background(), cfg, UnlockOptions{MinAge: 24 * time.Hour}, Options{Runner: runner, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if len(result.Repos) != 1 {
		t.Fatalf("repos = %d", len(result.Repos))
	}
	if result.Repos[0].Total != 2 || result.Repos[0].Removed != 0 || result.Repos[0].Held != 1 {
		t.Fatalf("unexpected sweep: %#v", result.Repos[0])
	}
	for _, call := range runner.calls {
		if resticSubcommand(call.Args) == "unlock" {
			t.Fatal("no lock may be removed while one is still recent")
		}
	}
}

func TestUnlockRemoveAllSkipsInspection(t *testing.T) {
	cfg := testConfig(t)
	url := cfg.Repos[0].URL
	runner := newFakeResticRunner()
	runner.enqueue(url, "list", fakeResticResponse{stdout: "aaaa1111"})
	runner.enqueue(url, "unlock", fakeResticResponse{})
	result, err := Unlock(context.Background(), cfg, UnlockOptions{MinAge: 24 * time.Hour, RemoveAll: true}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if result.Repos[0].Removed != 1 {
		t.Fatalf("unexpected sweep: %#v", result.Repos[0])
	}
	for _, call := range runner.calls {
		if resticSubcommand(call.Args) == "cat" {
			t.Fatal("--remove-all must not inspect lock ages")
		}
	}
}

func TestLockIDs(t *testing.T) {
	got := lockIDs([]byte("acbb474cd1\nnot-a-lock\n bbbb2222 \n"))
	if !reflect.DeepEqual(got, []string{"acbb474cd1", "bbbb2222"}) {
		t.Fatalf("lockIDs = %#v", got)
	}
}

func TestCheckLockNoop(t *testing.T) {
	cfg := testConfig(t)
	lockPath := filepath.Join(filepath.Dir(cfg.StatusPath), "backup.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("123\n"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	runner := newFakeResticRunner()
	result, err := Check(context.Background(), cfg, CheckOptions{}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("Check locked: %v", err)
	}
	if !result.LockSkipped || len(runner.calls) != 0 {
		t.Fatalf("expected lock skip with no calls: %#v %#v", result, runner.calls)
	}
}

func TestSelectDrillFiles(t *testing.T) {
	nodes := []lsNode{
		{Path: "/etc/hosts", Size: 100, Type: "file"},
		{Path: "/etc", Size: 0, Type: "dir"},
		{Path: "/etc/big", Size: drillSizeLimit + 1, Type: "file"},
		{Path: "/etc/aaa", Size: 10, Type: "file"},
		{Path: "/etc/link", Size: 10, Type: "symlink"},
		{Path: "/etc/bbb", Size: drillSizeLimit, Type: "file"},
		{Path: "/etc/ccc", Size: 1, Type: "file"},
		{Path: "/etc/ddd", Size: 1, Type: "file"},
		{Path: "/etc/eee", Size: 1, Type: "file"},
		{Path: "/etc/fff", Size: 1, Type: "file"},
	}
	got := selectDrillFiles(nodes, 5, drillSizeLimit)
	wantPaths := []string{"/etc/aaa", "/etc/bbb", "/etc/ccc", "/etc/ddd", "/etc/eee"}
	if len(got) != len(wantPaths) {
		t.Fatalf("selected %d files: %#v", len(got), got)
	}
	for i, p := range wantPaths {
		if got[i].Path != p {
			t.Fatalf("selected[%d] = %q, want %q", i, got[i].Path, p)
		}
	}
}

func TestSelectDrillFilesDeterministicUnsorted(t *testing.T) {
	a := selectDrillFiles([]lsNode{
		{Path: "/z", Size: 1, Type: "file"},
		{Path: "/a", Size: 1, Type: "file"},
		{Path: "/m", Size: 1, Type: "file"},
	}, 5, drillSizeLimit)
	if len(a) != 3 || a[0].Path != "/a" || a[1].Path != "/m" || a[2].Path != "/z" {
		t.Fatalf("not sorted deterministically: %#v", a)
	}
}

func TestDecideDrill(t *testing.T) {
	snap := time.Date(2026, 7, 3, 2, 0, 0, 0, time.UTC)
	older := snap.Add(-time.Hour)
	newer := snap.Add(time.Hour)
	cases := []struct {
		name       string
		liveExists bool
		mtime      time.Time
		bytesEqual bool
		want       drillDecision
	}{
		{name: "missing live", liveExists: false, want: drillSkip},
		{name: "newer live skipped", liveExists: true, mtime: newer, bytesEqual: false, want: drillSkip},
		{name: "unchanged equal", liveExists: true, mtime: older, bytesEqual: true, want: drillMatch},
		{name: "unchanged differs", liveExists: true, mtime: older, bytesEqual: false, want: drillMismatch},
		{name: "same mtime differs", liveExists: true, mtime: snap, bytesEqual: false, want: drillMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideDrill(tc.liveExists, tc.mtime, snap, tc.bytesEqual); got != tc.want {
				t.Fatalf("decideDrill = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseLsNodes(t *testing.T) {
	stream := `{"message_type":"snapshot","time":"2026-07-03T02:00:00Z","paths":["/etc"]}
{"message_type":"node","type":"dir","path":"/etc","size":0}
{"message_type":"node","type":"file","path":"/etc/hosts","size":128}
{"struct_type":"node","type":"file","path":"/etc/legacy","size":64}
{"message_type":"node","type":"symlink","path":"/etc/link","size":8}
`
	nodes, err := parseLsNodes([]byte(stream))
	if err != nil {
		t.Fatalf("parseLsNodes: %v", err)
	}
	if len(nodes) != 4 {
		t.Fatalf("nodes = %#v", nodes)
	}
	files := selectDrillFiles(nodes, 5, drillSizeLimit)
	if len(files) != 2 || files[0].Path != "/etc/hosts" || files[1].Path != "/etc/legacy" {
		t.Fatalf("files = %#v", files)
	}
}

func TestParseLatestSnapshot(t *testing.T) {
	data := []byte(`[{"id":"aaaa","time":"2026-07-01T02:00:00Z"},{"id":"bbbb","time":"2026-07-03T02:00:00Z"},{"id":"cccc","time":"2026-07-02T02:00:00Z"}]`)
	snap, ok, err := parseLatestSnapshot(data)
	if err != nil || !ok {
		t.Fatalf("parseLatestSnapshot ok=%v err=%v", ok, err)
	}
	if snap.ID != "bbbb" {
		t.Fatalf("latest id = %q, want bbbb", snap.ID)
	}
	_, ok, err = parseLatestSnapshot([]byte(`[]`))
	if err != nil || ok {
		t.Fatalf("empty snapshots should report not found: ok=%v err=%v", ok, err)
	}
}

func TestCheckDrillDetectsMismatch(t *testing.T) {
	cfg := testConfig(t)
	statusDir := filepath.Dir(cfg.StatusPath)
	liveDir := filepath.Join(statusDir, "live")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatalf("mkdir live: %v", err)
	}
	livePath := filepath.Join(liveDir, "sample.txt")
	if err := os.WriteFile(livePath, []byte("LIVE-CONTENT"), 0o600); err != nil {
		t.Fatalf("write live: %v", err)
	}
	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(livePath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	slashLive := resticLsForm(livePath)

	runner := newFakeResticRunner()
	runner.enqueue(cfg.Repos[0].URL, "check", fakeResticResponse{})
	runner.enqueue(cfg.Repos[0].URL, "snapshots", fakeResticResponse{stdout: `[{"id":"abcd1234ef","time":"2026-07-03T02:00:00Z"}]`})
	runner.enqueue(cfg.Repos[0].URL, "ls", fakeResticResponse{stdout: `{"message_type":"node","type":"file","path":"` + slashLive + `","size":12}` + "\n"})

	restoreRunner := &drillRestoreRunner{fake: runner, slashLive: slashLive, content: []byte("DIFFERENT!!!")}
	result, err := Check(context.Background(), cfg, CheckOptions{Drill: true}, Options{Runner: restoreRunner})
	if err != nil {
		t.Fatalf("Check drill: %v", err)
	}
	if result.AllSucceeded() {
		t.Fatalf("drill mismatch should fail the check: %#v", result.Repos)
	}
}

func resticLsForm(path string) string {
	slash := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(slash) > 1 && slash[1] == ':' {
		return "/" + string(slash[0]) + slash[2:]
	}
	return slash
}

type drillRestoreRunner struct {
	fake      *fakeResticRunner
	slashLive string
	content   []byte
}

func (r *drillRestoreRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	if len(command.Args) > 0 && command.Args[0] == "restore" {
		var target string
		for i, a := range command.Args {
			if a == "--target" && i+1 < len(command.Args) {
				target = command.Args[i+1]
			}
		}
		dest := filepath.Join(target, filepath.FromSlash(r.slashLive))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return CommandResult{}, err
		}
		if err := os.WriteFile(dest, r.content, 0o600); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, nil
	}
	return r.fake.Run(ctx, command)
}
