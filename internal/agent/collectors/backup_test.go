package collectors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"servermonitor/pkg/metrics"
	"servermonitor/pkg/version"
	"servermonitor/pkg/wire"
)

func testBackupCollector(path string, now time.Time) *backupCollector {
	c := newBackupCollector(path)
	c.now = func() time.Time { return now }
	c.queryNextRun = func(context.Context) *time.Time { return nil }
	c.privPath = ""
	c.execVersion = func(context.Context, string) (string, error) { return "", nil }
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

func backupMetricValue(points []wire.Point, id metrics.ID) (float64, bool) {
	for _, p := range points {
		if p.Metric == id {
			return p.Value, true
		}
	}
	return 0, false
}

func enableBackupAgentDrift(t *testing.T, c *backupCollector, rawVersion string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sm-agent")
	if err := os.WriteFile(path, []byte("agent"), 0o700); err != nil {
		t.Fatalf("write privileged agent: %v", err)
	}
	c.privPath = path
	c.execVersion = func(context.Context, string) (string, error) { return rawVersion, nil }
	return path
}

func TestParseAgentVersion(t *testing.T) {
	got, ok := parseAgentVersion("sm-agent 0.2.0\n")
	if !ok || got != "0.2.0" {
		t.Fatalf("parseAgentVersion = %q,%v, want 0.2.0,true", got, ok)
	}
	if got, ok := parseAgentVersion("garbage"); ok || got != "" {
		t.Fatalf("parseAgentVersion garbage = %q,%v, want empty,false", got, ok)
	}
}

func TestBackupCollectorOlderPrivilegedAgent(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	statusPath := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, statusPath, `{"version":1,"repos":[]}`)
	c := testBackupCollector(statusPath, now)
	enableBackupAgentDrift(t, c, "sm-agent 0.1.0\n")
	points, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if value, ok := backupMetricValue(points, metrics.BackupAgentStale); !ok || value != 1 {
		t.Fatalf("backup_agent_stale = %v,%v, want 1,true", value, ok)
	}
	status := c.Status()
	if status.State != backupStateStaleAgent {
		t.Fatalf("state = %q, want %q", status.State, backupStateStaleAgent)
	}
	if !strings.Contains(status.Message, "v0.1.0") || !strings.Contains(status.Message, "v"+version.Version) {
		t.Fatalf("message = %q, want both versions", status.Message)
	}
}

func TestBackupCollectorCurrentPrivilegedAgent(t *testing.T) {
	for _, privilegedVersion := range []string{version.Version, "999.0.0"} {
		t.Run(privilegedVersion, func(t *testing.T) {
			now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
			statusPath := filepath.Join(t.TempDir(), "backup-status.json")
			writeBackupStatus(t, statusPath, `{"version":1,"repos":[]}`)
			c := testBackupCollector(statusPath, now)
			enableBackupAgentDrift(t, c, "sm-agent "+privilegedVersion+"\n")
			points, err := c.Collect(context.Background())
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if value, ok := backupMetricValue(points, metrics.BackupAgentStale); !ok || value != 0 {
				t.Fatalf("backup_agent_stale = %v,%v, want 0,true", value, ok)
			}
			if state := c.Status().State; state != backupStateOK {
				t.Fatalf("state = %q, want %q", state, backupStateOK)
			}
		})
	}
}

func TestBackupCollectorUnavailablePrivilegedAgent(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	statusPath := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, statusPath, `{"version":1,"repos":[]}`)
	tests := []struct {
		name      string
		configure func(*backupCollector)
	}{
		{"missing", func(c *backupCollector) { c.privPath = filepath.Join(t.TempDir(), "missing") }},
		{"resident", func(c *backupCollector) {
			path, err := os.Executable()
			if err != nil {
				t.Fatalf("os.Executable: %v", err)
			}
			c.privPath = path
		}},
		{"exec error", func(c *backupCollector) {
			enableBackupAgentDrift(t, c, "")
			c.execVersion = func(context.Context, string) (string, error) { return "", errors.New("failed") }
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testBackupCollector(statusPath, now)
			tc.configure(c)
			points, err := c.Collect(context.Background())
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if _, ok := backupMetricValue(points, metrics.BackupAgentStale); ok {
				t.Fatalf("unexpected backup_agent_stale point: %#v", points)
			}
			if state := c.Status().State; state != backupStateOK {
				t.Fatalf("state = %q, want %q", state, backupStateOK)
			}
		})
	}
}

func TestBackupCollectorDriftDoesNotOverrideStatus(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		body      string
		mtime     time.Time
		wantState string
	}{
		{"error", "{", time.Time{}, backupStateError},
		{"stale", `{"version":1,"repos":[]}`, now.Add(-27 * time.Hour), backupStateStale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			statusPath := filepath.Join(t.TempDir(), "backup-status.json")
			if tc.body != "" {
				writeBackupStatus(t, statusPath, tc.body)
				if !tc.mtime.IsZero() {
					if err := os.Chtimes(statusPath, tc.mtime, tc.mtime); err != nil {
						t.Fatalf("chtimes: %v", err)
					}
				}
			}
			c := testBackupCollector(statusPath, now)
			enableBackupAgentDrift(t, c, "sm-agent 0.1.0\n")
			points, err := c.Collect(context.Background())
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if value, ok := backupMetricValue(points, metrics.BackupAgentStale); !ok || value != 1 {
				t.Fatalf("backup_agent_stale = %v,%v, want 1,true", value, ok)
			}
			if state := c.Status().State; state != tc.wantState {
				t.Fatalf("state = %q, want %q", state, tc.wantState)
			}
		})
	}
}

func TestBackupCollectorDriftWithoutStatus(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		statusPath     func(t *testing.T) string
		scheduledRepos []string
		wantState      string
	}{
		{
			name: "missing",
			statusPath: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "backup-status.json")
			},
			wantState: backupStateStaleAgent,
		},
		{
			name: "scheduled",
			statusPath: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "backup-status.json")
			},
			scheduledRepos: []string{"vps-a"},
			wantState:      backupStateStaleAgent,
		},
		{
			name: "read error",
			statusPath: func(t *testing.T) string {
				return t.TempDir()
			},
			scheduledRepos: []string{"vps-a"},
			wantState:      backupStateError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testBackupCollector(tc.statusPath(t), now)
			c.scheduledRepos = tc.scheduledRepos
			enableBackupAgentDrift(t, c, "sm-agent 0.1.0\n")
			points, err := c.Collect(context.Background())
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if value, ok := backupMetricValue(points, metrics.BackupAgentStale); !ok || value != 1 {
				t.Fatalf("backup_agent_stale = %v,%v, want 1,true", value, ok)
			}
			if state := c.Status().State; state != tc.wantState {
				t.Fatalf("state = %q, want %q", state, tc.wantState)
			}
		})
	}
}

func TestBackupCollectorPrivilegedAgentFingerprintCache(t *testing.T) {
	now := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	statusPath := filepath.Join(t.TempDir(), "backup-status.json")
	writeBackupStatus(t, statusPath, `{"version":1,"repos":[]}`)
	c := testBackupCollector(statusPath, now)
	privPath := enableBackupAgentDrift(t, c, "sm-agent 0.1.0\n")
	calls := 0
	c.execVersion = func(context.Context, string) (string, error) {
		calls++
		return "sm-agent 0.1.0\n", nil
	}
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect first: %v", err)
	}
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect second: %v", err)
	}
	if calls != 1 {
		t.Fatalf("execVersion calls = %d, want 1", calls)
	}
	touched := now.Add(time.Minute)
	if err := os.Chtimes(privPath, touched, touched); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect touched: %v", err)
	}
	if calls != 2 {
		t.Fatalf("execVersion calls after touch = %d, want 2", calls)
	}
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
	c.privPath = ""
	c.execVersion = func(context.Context, string) (string, error) { return "", nil }

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
	unixSeconds := time.Unix(1752613800, 0).UTC()
	unixMicros := time.UnixMicro(1783130400000000).UTC()
	formatted := time.Date(2026, 7, 15, 21, 57, 12, 0, time.UTC)
	tests := []struct {
		name string
		raw  string
		want *time.Time
	}{
		{"empty", "", nil},
		{"whitespace", "  \n", nil},
		{"not available", "n/a\n", nil},
		{"zero", "0", nil},
		{"infinity", "infinity", nil},
		{"unix seconds", "@1752613800\n", &unixSeconds},
		{"microseconds", "1783130400000000\n", &unixMicros},
		{"formatted", "Wed 2026-07-15 21:57:12 UTC\n", &formatted},
		{"empty unix seconds", "@", nil},
		{"negative unix seconds", "@-5", nil},
		{"negative microseconds", "-5", nil},
		{"overflow", "18446744073709551615", nil},
		{"garbage", "garbage", nil},
		{"malformed formatted", "Wed 2026-07-15 21:57 UTC", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSystemdNextElapse(tc.raw)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("parseSystemdNextElapse(%q) = %v, want nil", tc.raw, got)
				}
				return
			}
			if got == nil || !got.Equal(*tc.want) {
				t.Fatalf("parseSystemdNextElapse(%q) = %v, want %v", tc.raw, got, *tc.want)
			}
		})
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
	if metrics.BackupLastSuccessAgeS != 1000 || metrics.BackupCheckOK != 1007 || metrics.BackupAgentStale != 1013 {
		t.Fatalf("backup metric ID block changed")
	}
}
