package storage

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

var touchHostSeq int64

func touchTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed Touch test")
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func newTouchHost(t *testing.T, h *Hosts, db *DB) int64 {
	t.Helper()
	name := fmt.Sprintf("_smtest_touch_%d_%d", os.Getpid(), atomic.AddInt64(&touchHostSeq, 1))
	id, err := h.Register(context.Background(), name, name+"-token", 10)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM hosts WHERE id = $1`, id)
	})
	return id
}

func readUpgradeCols(t *testing.T, db *DB, id int64) (stall, dispatched *time.Time) {
	t.Helper()
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT upgrade_stall_since, upgrade_dispatched_at FROM hosts WHERE id = $1`, id,
	).Scan(&stall, &dispatched); err != nil {
		t.Fatalf("read upgrade cols: %v", err)
	}
	return stall, dispatched
}

func seedVersion(t *testing.T, h *Hosts, id int64, v string) {
	t.Helper()
	if _, err := h.Touch(context.Background(), id, HostInfoUpdate{AgentVersion: v}); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

func TestTouchUpgradeStateMachine(t *testing.T) {
	db := touchTestDB(t)
	hosts := NewHosts(db)
	ctx := context.Background()

	t.Run("auto-on out-of-date arms stall and dispatch", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		stall, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true})
		if err != nil {
			t.Fatalf("touch: %v", err)
		}
		if stall == nil {
			t.Fatal("expected stall_since armed for auto-on out-of-date host")
		}
		dbStall, dbDisp := readUpgradeCols(t, db, id)
		if dbStall == nil || dbDisp == nil {
			t.Fatalf("expected both columns armed; got stall=%v dispatched=%v", dbStall, dbDisp)
		}
	})

	t.Run("manual upgrade with auto-off arms stall", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		autoOff := false
		if err := hosts.Update(ctx, id, HostUpdate{AutoUpgrade: &autoOff}); err != nil {
			t.Fatalf("disable auto_upgrade: %v", err)
		}
		if err := hosts.RequestUpgrade(ctx, id); err != nil {
			t.Fatalf("request upgrade: %v", err)
		}
		stall, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true})
		if err != nil {
			t.Fatalf("touch: %v", err)
		}
		if stall == nil {
			t.Fatal("manual upgrade on auto-off host must arm stall_since so it can later show stalled")
		}
		if _, dbDisp := readUpgradeCols(t, db, id); dbDisp == nil {
			t.Fatal("manual upgrade must arm dispatched_at")
		}
	})

	t.Run("stall survives the request being cleared", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		autoOff := false
		if err := hosts.Update(ctx, id, HostUpdate{AutoUpgrade: &autoOff}); err != nil {
			t.Fatalf("disable auto_upgrade: %v", err)
		}
		if err := hosts.RequestUpgrade(ctx, id); err != nil {
			t.Fatalf("request upgrade: %v", err)
		}
		first, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true})
		if err != nil || first == nil {
			t.Fatalf("first touch: stall=%v err=%v", first, err)
		}
		if err := hosts.ClearUpgradeRequest(ctx, id); err != nil {
			t.Fatalf("clear request: %v", err)
		}
		second, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true})
		if err != nil {
			t.Fatalf("second touch: %v", err)
		}
		if second == nil {
			t.Fatal("stall must persist after the request is cleared (dispatched_at keeps it in flight)")
		}
		if !second.Equal(*first) {
			t.Fatalf("stall_since must be stable across same-version ingests: %v != %v", second, first)
		}
	})

	t.Run("up-to-date clears stall and dispatch", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		if _, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true}); err != nil {
			t.Fatalf("arm: %v", err)
		}
		stall, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: false})
		if err != nil {
			t.Fatalf("touch: %v", err)
		}
		if stall != nil {
			t.Fatalf("up-to-date host must clear stall_since, got %v", stall)
		}
		dbStall, dbDisp := readUpgradeCols(t, db, id)
		if dbStall != nil || dbDisp != nil {
			t.Fatalf("up-to-date must clear both; got stall=%v dispatched=%v", dbStall, dbDisp)
		}
	})

	t.Run("externally managed clears stall and dispatch", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		if _, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true}); err != nil {
			t.Fatalf("arm: %v", err)
		}
		managed := true
		stall, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true, ExternallyManaged: &managed})
		if err != nil {
			t.Fatalf("touch: %v", err)
		}
		if stall != nil {
			t.Fatalf("externally managed host must clear stall_since, got %v", stall)
		}
		if _, dbDisp := readUpgradeCols(t, db, id); dbDisp != nil {
			t.Fatalf("externally managed must clear dispatched_at, got %v", dbDisp)
		}
	})

	t.Run("version progress resets the stall clock", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		first, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true})
		if err != nil || first == nil {
			t.Fatalf("first touch: stall=%v err=%v", first, err)
		}
		time.Sleep(10 * time.Millisecond)
		second, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.5", ShouldSelfUpgrade: true})
		if err != nil {
			t.Fatalf("second touch: %v", err)
		}
		if second == nil || !second.After(*first) {
			t.Fatalf("progress to a newer version must reset the stall clock forward: first=%v second=%v", first, second)
		}
	})

	t.Run("disabling auto-upgrade clears an armed stall", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		if _, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true}); err != nil {
			t.Fatalf("arm: %v", err)
		}
		if dbStall, dbDisp := readUpgradeCols(t, db, id); dbStall == nil || dbDisp == nil {
			t.Fatalf("precondition: expected armed; got stall=%v dispatched=%v", dbStall, dbDisp)
		}
		autoOff := false
		if err := hosts.Update(ctx, id, HostUpdate{AutoUpgrade: &autoOff}); err != nil {
			t.Fatalf("disable auto_upgrade: %v", err)
		}
		if dbStall, dbDisp := readUpgradeCols(t, db, id); dbStall != nil || dbDisp != nil {
			t.Fatalf("disabling auto-upgrade with no pending request must clear stall+dispatch; got stall=%v dispatched=%v", dbStall, dbDisp)
		}
	})

	t.Run("disabling auto-upgrade keeps stall while a request is pending", func(t *testing.T) {
		id := newTouchHost(t, hosts, db)
		seedVersion(t, hosts, id, "0.1.0")
		if err := hosts.RequestUpgrade(ctx, id); err != nil {
			t.Fatalf("request upgrade: %v", err)
		}
		if _, err := hosts.Touch(ctx, id, HostInfoUpdate{AgentVersion: "0.1.0", ShouldSelfUpgrade: true}); err != nil {
			t.Fatalf("arm: %v", err)
		}
		autoOff := false
		if err := hosts.Update(ctx, id, HostUpdate{AutoUpgrade: &autoOff}); err != nil {
			t.Fatalf("disable auto_upgrade: %v", err)
		}
		if dbStall, dbDisp := readUpgradeCols(t, db, id); dbStall == nil || dbDisp == nil {
			t.Fatalf("a pending request must keep stall+dispatch armed despite auto-off; got stall=%v dispatched=%v", dbStall, dbDisp)
		}
	})
}
