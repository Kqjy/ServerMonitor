package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBackupTargetDeletable(t *testing.T) {
	cases := []struct {
		name        string
		storedUsed  int64
		hasObjects  bool
		repoChecked bool
		nodeHosted  bool
		want        bool
	}{
		{"empty, backend confirms empty", 0, false, true, false, true},
		{"empty, no backend configured", 0, false, false, false, true},
		{"stored counter shows data", 4096, false, true, false, false},
		{"counter clear but backend holds objects", 0, true, true, false, false},
		{"zero-byte objects only (bytes==0 but objects exist)", 0, true, true, false, false},
		{"counter clear, backend unknown, stays deletable", 0, true, false, false, true},
		{"node namespace is never hard-deletable", 0, false, false, true, false},
	}
	for _, c := range cases {
		if got := backupTargetDeletable(c.storedUsed, c.hasObjects, c.repoChecked, c.nodeHosted); got != c.want {
			t.Errorf("%s: backupTargetDeletable(%d,%v,%v,%v) = %v, want %v", c.name, c.storedUsed, c.hasObjects, c.repoChecked, c.nodeHosted, got, c.want)
		}
	}
}

func TestHostBackupsResponseShape(t *testing.T) {
	updatedAt := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)
	resp := hostBackupsResponse{Repos: []backupRepoSnapshot{
		{
			Repo:      "alpha",
			UpdatedAt: updatedAt,
			Status:    json.RawMessage(`{"name":"alpha","success":true,"snapshots":[{"id":"snap-a"}]}`),
		},
	}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	want := `{"repos":[{"repo":"alpha","updated_at":"2026-07-03T03:00:00Z","status":{"name":"alpha","success":true,"snapshots":[{"id":"snap-a"}]}}]}`
	if string(data) != want {
		t.Fatalf("response json = %s, want %s", data, want)
	}
	empty, err := json.Marshal(hostBackupsResponse{Repos: []backupRepoSnapshot{}})
	if err != nil {
		t.Fatalf("marshal empty response: %v", err)
	}
	if string(empty) != `{"repos":[]}` {
		t.Fatalf("empty response json = %s", empty)
	}
}
