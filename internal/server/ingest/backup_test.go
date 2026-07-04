package ingest

import (
	"encoding/json"
	"testing"
	"time"

	"servermonitor/pkg/wire"
)

func TestConvertBackupsMarshalsPayloadAndRepoSet(t *testing.T) {
	updatedAt := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	started := time.Date(2026, 7, 3, 2, 0, 0, 0, time.UTC)
	success := false
	snapshots := make([]wire.BackupSnapshot, 51)
	for i := range snapshots {
		snapshots[i] = wire.BackupSnapshot{ID: "snap"}
	}
	rows, repos, err := ConvertBackups(42, []wire.BackupRepoStatus{
		{Name: "zeta", Engine: "restic", Success: true, DurationS: 9},
		{Name: "alpha", Engine: "borg", LastStarted: &started, CheckSuccess: &success, Snapshots: []wire.BackupSnapshot{{ID: "snap-a", Time: started, Paths: []string{"/srv"}}}},
		{Name: "   ", Engine: "ignored"},
		{Name: " zeta ", Engine: "restic", Success: false, Error: "last wins", Snapshots: snapshots},
	}, updatedAt)
	if err != nil {
		t.Fatalf("ConvertBackups: %v", err)
	}
	if len(repos) != 2 || repos[0] != "alpha" || repos[1] != "zeta" {
		t.Fatalf("repos = %#v, want sorted unique names", repos)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].HostID != 42 || rows[0].Repo != "alpha" || !rows[0].UpdatedAt.Equal(updatedAt) {
		t.Fatalf("first row metadata = %#v", rows[0])
	}
	var alpha wire.BackupRepoStatus
	if err := json.Unmarshal(rows[0].Payload, &alpha); err != nil {
		t.Fatalf("unmarshal alpha payload: %v", err)
	}
	if alpha.Name != "alpha" || alpha.Engine != "borg" || alpha.CheckSuccess == nil || *alpha.CheckSuccess || len(alpha.Snapshots) != 1 {
		t.Fatalf("alpha payload = %#v", alpha)
	}
	var zeta wire.BackupRepoStatus
	if err := json.Unmarshal(rows[1].Payload, &zeta); err != nil {
		t.Fatalf("unmarshal zeta payload: %v", err)
	}
	if zeta.Name != "zeta" || zeta.Success || zeta.Error != "last wins" || len(zeta.Snapshots) != 50 {
		t.Fatalf("zeta payload = %#v", zeta)
	}
}
