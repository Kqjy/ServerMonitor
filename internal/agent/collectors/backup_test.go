package collectors

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/wire"
)

func testBackupCollector(path string, now time.Time) *backupCollector {
	c := newBackupCollector(path)
	c.now = func() time.Time { return now }
	c.queryNextRun = func(context.Context) *time.Time { return nil }
	return c
}

func writeBackupStatus(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write backup status: %v", err)
	}
}

func writeBackupProgress(t *testing.T, statusPath, body string) {
	t.Helper()
	writeBackupStatus(t, filepath.Join(filepath.Dir(statusPath), "backup-progress.json"), body)
}

func backupPointMap(points []wire.Point) map[string]float64 {
	out := map[string]float64{}
	for _, p := range points {
		out[p.Labels["repo"]+"|"+p.Metric.Meta().Name] = p.Value
	}
	return out
}

func TestBackupCollectorMissingFile(t *testing.T) {
	c := testBackupCollector(filepath.Join(t.TempDir(), "missing.json"), time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC))
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("points = %d, want 0", len(points))
	}
	if got := c.Status().State; got != backupStateNotConfigured {
		t.Fatalf("state = %q, want %q", got, backupStateNotConfigured)
	}
}

func TestBackupCollectorLiveProgressWithoutStatus(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupProgress(t, path, `{"version":1,"running":true,"repo":"vps-a","started_at":1783047300,"updated_at":1783047590,"percent":42,"bytes_done":1200,"total_bytes":3000}`)
	c := testBackupCollector(path, now)
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := backupPointMap(points)
	want := map[string]float64{
		"vps-a|backup_running":              1,
		"vps-a|backup_run_elapsed_s":        300,
		"vps-a|backup_progress_pct":         42,
		"vps-a|backup_progress_bytes":       1200,
		"vps-a|backup_progress_total_bytes": 3000,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("points = %#v, want %#v", got, want)
	}
}

func TestBackupCollectorStaleProgressIsNotLive(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupProgress(t, path, `{"version":1,"running":true,"repo":"vps-a","started_at":1783047000,"updated_at":1783047500}`)
	c := testBackupCollector(path, now)
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("stale progress points = %#v", points)
	}
}

func TestBackupCollectorEmitsTerminalRunningPointOnce(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	progressPath := filepath.Join(filepath.Dir(path), "backup-progress.json")
	writeBackupProgress(t, path, `{"version":1,"running":true,"repo":"vps-a","started_at":1783047300,"updated_at":1783047590}`)
	c := testBackupCollector(path, now)
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect live: %v", err)
	}
	if err := os.Remove(progressPath); err != nil {
		t.Fatalf("remove progress: %v", err)
	}
	terminal, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect terminal: %v", err)
	}
	got := backupPointMap(terminal)
	if got["vps-a|backup_running"] != 0 || len(got) != 1 {
		t.Fatalf("terminal points = %#v", terminal)
	}
	again, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("repeated terminal points = %#v", again)
	}
}

func TestBackupCollectorMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, path, "{")
	c := testBackupCollector(path, time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC))
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("points = %d, want 0", len(points))
	}
	if got := c.Status().State; got != backupStateError {
		t.Fatalf("state = %q, want %q", got, backupStateError)
	}
	if c.Status().Message == "" {
		t.Fatalf("error state should include a message")
	}
}

func TestBackupCollectorStaleStillEmits(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, path, `{"version":1,"repos":[{"name":"vps-a","last_success":"2026-07-03T02:00:00Z","success":true}]}`)
	stale := now.Add(-27 * time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	c := testBackupCollector(path, now)
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) == 0 {
		t.Fatalf("stale status should still emit points")
	}
	status := c.Status()
	if status.State != backupStateStale {
		t.Fatalf("state = %q, want %q", status.State, backupStateStale)
	}
	if status.Message == "" {
		t.Fatalf("stale state should include file age")
	}
}

func TestBackupCollectorMultiRepoValues(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, path, `{
		"version": 1,
		"repos": [
			{
				"name": "vps-a",
				"last_success": "2026-07-03T02:30:00Z",
				"success": true,
				"duration_s": 672,
				"added_bytes": 104857600,
				"total_bytes": 42949672960,
				"snapshot_count": 87,
				"check_last": "2026-06-28T04:00:00Z",
				"check_success": true
			},
			{
				"name": "vps-b",
				"success": false,
				"duration_s": 12,
				"snapshot_count": 4,
				"check_success": false
			}
		]
	}`)
	c := testBackupCollector(path, now)
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := backupPointMap(points)
	want := map[string]float64{
		"vps-a|backup_last_success_age_s": 1800,
		"vps-a|backup_last_run_ok":        1,
		"vps-a|backup_duration_s":         672,
		"vps-a|backup_added_bytes":        104857600,
		"vps-a|backup_total_bytes":        42949672960,
		"vps-a|backup_snapshot_count":     87,
		"vps-a|backup_check_age_s":        now.Sub(time.Date(2026, 6, 28, 4, 0, 0, 0, time.UTC)).Seconds(),
		"vps-a|backup_check_ok":           1,
		"vps-b|backup_last_run_ok":        0,
		"vps-b|backup_duration_s":         12,
		"vps-b|backup_snapshot_count":     4,
		"vps-b|backup_check_ok":           0,
	}
	if len(got) != len(want) {
		t.Fatalf("points = %#v, want %#v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["vps-b|backup_last_success_age_s"]; ok {
		t.Fatalf("absent last_success should not emit an age")
	}
	if _, ok := got["vps-b|backup_check_age_s"]; ok {
		t.Fatalf("absent check_last should not emit an age")
	}
	if gotState := c.Status().State; gotState != backupStateOK {
		t.Fatalf("state = %q, want %q", gotState, backupStateOK)
	}
}

func TestBackupCollectorInventoryThrottle(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, path, `{"version":1,"repos":[{"name":"vps-a","success":true}]}`)
	mod1 := now.Add(-time.Minute)
	if err := os.Chtimes(path, mod1, mod1); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	c := newBackupCollector(path)
	c.now = func() time.Time { return now }
	c.queryNextRun = func(context.Context) *time.Time { return nil }

	first, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups first: %v", err)
	}
	if len(first) != 1 || first[0].Name != "vps-a" {
		t.Fatalf("first backups = %#v", first)
	}

	second, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups second: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second backups = %#v, want none", second)
	}

	now = now.Add(14*time.Minute + 59*time.Second)
	third, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups third: %v", err)
	}
	if len(third) != 0 {
		t.Fatalf("third backups = %#v, want none", third)
	}

	now = now.Add(time.Second)
	fourth, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups fourth: %v", err)
	}
	if len(fourth) != 1 || fourth[0].Name != "vps-a" {
		t.Fatalf("fourth backups = %#v", fourth)
	}

	now = now.Add(time.Minute)
	writeBackupStatus(t, path, `{"version":1,"repos":[{"name":"vps-b","success":false,"snapshot_count":2}]}`)
	mod2 := mod1.Add(2 * time.Second)
	if err := os.Chtimes(path, mod2, mod2); err != nil {
		t.Fatalf("chtimes changed: %v", err)
	}
	fifth, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups fifth: %v", err)
	}
	if len(fifth) != 1 || fifth[0].Name != "vps-b" || fifth[0].SnapshotCount != 2 {
		t.Fatalf("fifth backups = %#v", fifth)
	}
}

func TestParseSystemdNextElapse(t *testing.T) {
	want := time.UnixMicro(1783130400000000).UTC()
	if got := parseSystemdNextElapse("1783130400000000\n"); got == nil || !got.Equal(want) {
		t.Fatalf("parsed time = %#v, want %v", got, want)
	}
	for _, raw := range []string{"0", "", "  \n", "infinity", "garbage", "-5", "18446744073709551615"} {
		if got := parseSystemdNextElapse(raw); got != nil {
			t.Fatalf("parseSystemdNextElapse(%q) = %v, want nil", raw, got)
		}
	}
}

func TestParseWindowsNextRun(t *testing.T) {
	want := time.Date(2026, 7, 15, 2, 30, 0, 0, time.UTC)
	if got := parseWindowsNextRun("2026-07-15T02:30:00.0000000Z\r\n"); got == nil || !got.Equal(want) {
		t.Fatalf("parsed time = %#v, want %v", got, want)
	}
	for _, raw := range []string{"", "garbage", "0001-01-01T00:00:00.0000000Z"} {
		if got := parseWindowsNextRun(raw); got != nil {
			t.Fatalf("parseWindowsNextRun(%q) = %v, want nil", raw, got)
		}
	}
}

func TestBackupCollectorInventoryNextRun(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, path, `{"version":1,"repos":[{"name":"vps-a"},{"name":"vps-b"}]}`)
	c := testBackupCollector(path, now)
	next := time.Date(2026, 7, 4, 2, 0, 0, 0, time.UTC)
	c.queryNextRun = func(context.Context) *time.Time { return &next }
	got, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups: %v", err)
	}
	if len(got) != 2 || got[0].NextRun != &next || got[1].NextRun != &next {
		t.Fatalf("next runs = %#v", got)
	}
	c.lastInventoryAttached = time.Time{}
	c.queryNextRun = func(context.Context) *time.Time { return nil }
	got, err = c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups nil: %v", err)
	}
	if len(got) != 2 || got[0].NextRun != nil || got[1].NextRun != nil {
		t.Fatalf("nil next runs = %#v", got)
	}
}

func TestBackupCollectorInventoryMappingAndNilNumbers(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, path, `{
		"version": 1,
		"repos": [
			{
				"name": "vps-a",
				"engine": "restic",
				"last_started": "2026-07-03T01:50:00Z",
				"last_finished": "2026-07-03T02:01:12Z",
				"last_success": "2026-07-03T02:01:12Z",
				"success": true,
				"error": "scrubbed failure",
				"duration_s": 672,
				"added_bytes": 104857600,
				"total_bytes": 42949672960,
				"snapshot_count": 87,
				"check_last": "2026-06-28T04:00:00Z",
				"check_success": false,
				"snapshots": [
					{"id":"snap-a","time":"2026-07-03T02:01:12Z","paths":["/srv","/etc"]},
					{"id":"snap-b","time":"2026-07-02T02:01:12Z"}
				]
			},
			{
				"name": "vps-b",
				"engine": "borg"
			}
		]
	}`)
	c := testBackupCollector(path, now)
	got, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("backups = %#v", got)
	}
	a := got[0]
	if a.Name != "vps-a" || a.Engine != "restic" || !a.Success || a.Error != "scrubbed failure" {
		t.Fatalf("mapped repo fields = %#v", a)
	}
	if a.LastStarted == nil || a.LastFinished == nil || a.LastSuccess == nil || a.CheckLast == nil {
		t.Fatalf("expected time fields to be mapped: %#v", a)
	}
	if a.DurationS != 672 || a.AddedBytes != 104857600 || a.TotalBytes != 42949672960 || a.SnapshotCount != 87 {
		t.Fatalf("numeric fields = %#v", a)
	}
	if a.CheckSuccess == nil || *a.CheckSuccess {
		t.Fatalf("check_success = %#v, want false pointer", a.CheckSuccess)
	}
	if len(a.Snapshots) != 2 || a.Snapshots[0].ID != "snap-a" || len(a.Snapshots[0].Paths) != 2 || a.Snapshots[1].ID != "snap-b" {
		t.Fatalf("snapshots = %#v", a.Snapshots)
	}
	b := got[1]
	if b.Name != "vps-b" || b.Engine != "borg" {
		t.Fatalf("second repo = %#v", b)
	}
	if b.Success || b.DurationS != 0 || b.AddedBytes != 0 || b.TotalBytes != 0 || b.SnapshotCount != 0 {
		t.Fatalf("nil numeric and bool fields should map to zero values: %#v", b)
	}
	if b.CheckSuccess != nil || b.LastStarted != nil || b.LastFinished != nil || b.LastSuccess != nil || b.CheckLast != nil {
		t.Fatalf("nil pointer fields should stay nil: %#v", b)
	}
}

func TestBackupCollectorInventoryMissingOrMalformed(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "backup-status.json")
	c := testBackupCollector(path, now)
	missing, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups missing: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing backups = %#v, want none", missing)
	}
	if got := c.Status().State; got != backupStateNotConfigured {
		t.Fatalf("missing state = %q, want %q", got, backupStateNotConfigured)
	}
	writeBackupStatus(t, path, "{")
	bad, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups malformed: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("malformed backups = %#v, want none", bad)
	}
	if got := c.Status().State; got != backupStateError {
		t.Fatalf("malformed state = %q, want %q", got, backupStateError)
	}
}

func TestBackupStatusTruncatesSnapshots(t *testing.T) {
	data := `{"version":1,"repos":[{"name":"vps-a","snapshots":[`
	for i := 0; i < 55; i++ {
		if i > 0 {
			data += ","
		}
		data += `{"id":"x"}`
	}
	data += `]}]}`
	status, err := parseBackupStatus([]byte(data))
	if err != nil {
		t.Fatalf("parseBackupStatus: %v", err)
	}
	if got := len(status.Repos[0].Snapshots); got != backupSnapshotLimit {
		t.Fatalf("snapshots = %d, want %d", got, backupSnapshotLimit)
	}
}

func TestBackupMetricIDs(t *testing.T) {
	if metrics.BackupLastSuccessAgeS != 1000 || metrics.BackupCheckOK != 1007 {
		t.Fatalf("backup metric ID block changed")
	}
}
