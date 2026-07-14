package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLockFile(t *testing.T, path, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

func TestRunLockV2LivenessFirst(t *testing.T) {
	cfg := testConfig(t)
	lockPath := filepath.Join(filepath.Dir(cfg.StatusPath), "backup.lock")

	old := time.Now().Add(-72 * time.Hour)

	live := newFakeResticRunner()
	writeLockFile(t, lockPath, fmt.Sprintf("%s %d %d abc123\n", runLockVersion, os.Getpid(), selfStartTime()))
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	res, err := Run(context.Background(), cfg, Options{Runner: live})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.LockSkipped || len(live.calls) != 0 {
		t.Fatalf("an old but LIVE v2 owner must not be stolen: skipped=%v calls=%d", res.LockSkipped, len(live.calls))
	}

	dead := newFakeResticRunner()
	writeLockFile(t, lockPath, fmt.Sprintf("%s 2147483647 123456 abc123\n", runLockVersion))
	res, err = Run(context.Background(), cfg, Options{Runner: dead})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LockSkipped || len(dead.calls) == 0 {
		t.Fatalf("a dead v2 owner lock must be reclaimed and the run proceed: skipped=%v calls=%d", res.LockSkipped, len(dead.calls))
	}

	recycled := newFakeResticRunner()
	writeLockFile(t, lockPath, fmt.Sprintf("%s %d %d abc123\n", runLockVersion, os.Getpid(), selfStartTime()+9_000_000_000))
	res, err = Run(context.Background(), cfg, Options{Runner: recycled})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LockSkipped || len(recycled.calls) == 0 {
		t.Fatalf("a live pid with a mismatched start time (recycled/restarted) must be reclaimed: skipped=%v calls=%d", res.LockSkipped, len(recycled.calls))
	}
}

func TestNewRunLockTokenIsVersioned(t *testing.T) {
	token, err := newRunLockToken()
	if err != nil {
		t.Fatalf("newRunLockToken: %v", err)
	}
	if !isVersionedLockToken(token) {
		t.Fatalf("token is not versioned: %q", token)
	}
	alive, err := runLockOwnerAlive(token)
	if err != nil {
		t.Fatalf("runLockOwnerAlive: %v", err)
	}
	if !alive {
		t.Fatal("freshly minted token should report its own (live) process as alive")
	}
}
