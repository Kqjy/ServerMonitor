package wire

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestExternallyManagedPresenceTracking(t *testing.T) {
	managed, unmanaged := true, false

	encTrue, err := json.Marshal(HostInfo{ExternallyManaged: &managed})
	if err != nil {
		t.Fatal(err)
	}
	encFalse, err := json.Marshal(HostInfo{ExternallyManaged: &unmanaged})
	if err != nil {
		t.Fatal(err)
	}

	var gotTrue HostInfo
	if err := json.Unmarshal(encTrue, &gotTrue); err != nil {
		t.Fatal(err)
	}
	if gotTrue.ExternallyManaged == nil || !*gotTrue.ExternallyManaged {
		t.Fatalf("managed=true must round-trip as non-nil true, got %v", gotTrue.ExternallyManaged)
	}

	var gotFalse HostInfo
	if err := json.Unmarshal(encFalse, &gotFalse); err != nil {
		t.Fatal(err)
	}
	if gotFalse.ExternallyManaged == nil || *gotFalse.ExternallyManaged {
		t.Fatalf("managed=false must round-trip as non-nil false so a real unmanage flips the host, got %v", gotFalse.ExternallyManaged)
	}

	var legacy HostInfo
	if err := json.Unmarshal([]byte(`{"hostname":"old","os":"linux","agent_version":"0.1.8"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ExternallyManaged != nil {
		t.Fatalf("a payload predating the field must decode as nil so Touch leaves the DB value untouched, got %v", *legacy.ExternallyManaged)
	}
}

func TestBatchBackupRoundTrip(t *testing.T) {
	ok := true
	oneFileSystem := false
	started := time.Date(2026, 7, 3, 2, 30, 0, 0, time.UTC)
	finished := time.Date(2026, 7, 3, 2, 41, 12, 0, time.UTC)
	b := Batch{
		Host: HostInfo{Hostname: "host", OS: "linux", AgentVersion: "0.2.6"},
		Backups: []BackupRepoStatus{{
			Name:          "vps-a",
			Engine:        "borg",
			LastStarted:   &started,
			LastFinished:  &finished,
			LastSuccess:   &finished,
			Success:       true,
			DurationS:     672,
			AddedBytes:    104857600,
			TotalBytes:    42949672960,
			SnapshotCount: 87,
			CheckLast:     &finished,
			CheckSuccess:  &ok,
			Paths:         []string{"/etc", "/var/lib/docker/volumes"},
			Excludes:      []string{"/var/lib/docker/overlay2"},
			OneFileSystem: &oneFileSystem,
			PathStats: []BackupPathStat{{
				Path:  "/var/lib/docker/volumes",
				Bytes: 9126805504,
				Files: 421,
			}},
			StatsSnapshot: "1a2b3c4d",
			Snapshots: []BackupSnapshot{{
				ID:    "1a2b3c4d",
				Time:  started,
				Paths: []string{"/etc"},
			}},
		}},
		Sent: finished,
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var got Batch
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode with DisallowUnknownFields: %v", err)
	}
	if len(got.Backups) != 1 || got.Backups[0].Name != "vps-a" || got.Backups[0].Snapshots[0].ID != "1a2b3c4d" || len(got.Backups[0].Paths) != 2 || got.Backups[0].Excludes[0] != "/var/lib/docker/overlay2" || got.Backups[0].OneFileSystem == nil || *got.Backups[0].OneFileSystem || got.Backups[0].StatsSnapshot != "1a2b3c4d" || got.Backups[0].PathStats[0].Files != 421 {
		t.Fatalf("backup round-trip mismatch: %+v", got.Backups)
	}
}

func TestBackupBrowseWireRoundTrip(t *testing.T) {
	mtime := time.Date(2026, 7, 17, 8, 30, 0, 0, time.UTC)
	value := BackupBrowseResult{
		Entries:   []BackupBrowseEntry{{Name: "data", Type: "dir", Mtime: &mtime}, {Name: "db.sqlite", Type: "file", Size: 4096, Mtime: &mtime}},
		Truncated: true,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got BackupBrowseResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 || got.Entries[1].Size != 4096 || !got.Truncated || got.Entries[0].Mtime == nil || !got.Entries[0].Mtime.Equal(mtime) {
		t.Fatalf("browse round-trip mismatch: %+v", got)
	}
}

func TestIngestAckBackupBrowseFieldName(t *testing.T) {
	raw, err := json.Marshal(IngestAck{BackupBrowsePending: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"backup_browse_pending":true`)) {
		t.Fatalf("ack json = %s", raw)
	}
}

func TestBatchWithoutBackupsDisallowUnknownFields(t *testing.T) {
	raw := []byte(`{"host":{"hostname":"host","os":"linux","agent_version":"0.2.6"},"points":[],"sent":"2026-07-03T02:41:12Z"}`)
	var got Batch
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode legacy batch: %v", err)
	}
	if len(got.Backups) != 0 {
		t.Fatalf("legacy batch decoded backups: %+v", got.Backups)
	}
}
