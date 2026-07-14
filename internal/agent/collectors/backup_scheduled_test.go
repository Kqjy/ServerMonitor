package collectors

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledInventoryBeforeFirstStatusFile(t *testing.T) {
	c := newBackupCollector(filepath.Join(t.TempDir(), "no-such-backup-status.json"))
	future := time.Now().Add(time.Hour)
	c.queryNextRun = func(context.Context) *time.Time { return &future }
	c.scheduledRepos = []string{"web01", "web02"}

	out, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 pending repos, got %d", len(out))
	}
	if out[0].Name != "web01" || out[0].Success || out[0].NextRun == nil {
		t.Fatalf("pending repo should carry name + next run but no success: %#v", out[0])
	}
	if out[0].LastSuccess != nil || out[0].LastFinished != nil || out[0].Error != "" {
		t.Fatalf("pending repo must not claim any run happened: %#v", out[0])
	}

	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := c.Status().State; got != backupStateScheduled {
		t.Fatalf("state = %q, want %q", got, backupStateScheduled)
	}
}

func TestNoScheduledReposStaysNotConfigured(t *testing.T) {
	c := newBackupCollector(filepath.Join(t.TempDir(), "no-such-backup-status.json"))
	out, err := c.CollectBackups(context.Background())
	if err != nil {
		t.Fatalf("CollectBackups: %v", err)
	}
	if out != nil {
		t.Fatalf("no status + no scheduled repos should report nothing, got %#v", out)
	}
}
