package metrics

import "testing"

func TestBackupMetricsMeta(t *testing.T) {
	cases := []struct {
		id   ID
		name string
		unit string
	}{
		{BackupLastSuccessAgeS, "backup_last_success_age_s", "s"},
		{BackupLastRunOK, "backup_last_run_ok", "bool"},
		{BackupDurationS, "backup_duration_s", "s"},
		{BackupAddedBytes, "backup_added_bytes", "B"},
		{BackupTotalBytes, "backup_total_bytes", "B"},
		{BackupSnapshotCount, "backup_snapshot_count", "count"},
		{BackupCheckAgeS, "backup_check_age_s", "s"},
		{BackupCheckOK, "backup_check_ok", "bool"},
		{BackupAgentStale, "backup_agent_stale", "bool"},
		{BackupAgentUnexecutable, "backup_agent_unexecutable", "bool"},
	}
	for _, tc := range cases {
		got := tc.id.Meta()
		if got.Name != tc.name || got.Unit != tc.unit {
			t.Fatalf("Meta(%d) = {%q,%q}, want {%q,%q}", tc.id, got.Name, got.Unit, tc.name, tc.unit)
		}
		byName, ok := ByName(tc.name)
		if !ok || byName != tc.id {
			t.Fatalf("ByName(%q) = %d,%v, want %d,true", tc.name, byName, ok, tc.id)
		}
	}
}

func TestFSOverallMetricsMeta(t *testing.T) {
	cases := []struct {
		id   ID
		name string
		unit string
	}{
		{FSOverallTotal, "fs_overall_total", "B"},
		{FSOverallUsed, "fs_overall_used", "B"},
		{FSOverallUsedPct, "fs_overall_used_pct", "%"},
	}
	for _, tc := range cases {
		got := tc.id.Meta()
		if got.Name != tc.name || got.Unit != tc.unit {
			t.Fatalf("Meta(%d) = {%q,%q}, want {%q,%q}", tc.id, got.Name, got.Unit, tc.name, tc.unit)
		}
		byName, ok := ByName(tc.name)
		if !ok || byName != tc.id {
			t.Fatalf("ByName(%q) = %d,%v, want %d,true", tc.name, byName, ok, tc.id)
		}
	}
}
