package backupsched

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata"
)

func noJitter(time.Duration) time.Duration { return 0 }

func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestParseHHMM(t *testing.T) {
	if h, m, err := parseHHMM("02:30"); err != nil || h != 2 || m != 30 {
		t.Fatalf("parseHHMM 02:30 = %d %d %v", h, m, err)
	}
	for _, bad := range []string{"", "2", "24:00", "01:60", "aa:bb", "-1:00"} {
		if _, _, err := parseHHMM(bad); err == nil {
			t.Fatalf("parseHHMM(%q) should error", bad)
		}
	}
}

func TestDailyOccurrence(t *testing.T) {
	now := at(2026, 7, 14, 3, 0)
	if got := mostRecentDaily(now, 2, 30); !got.Equal(at(2026, 7, 14, 2, 30)) {
		t.Fatalf("mostRecentDaily after time-of-day = %v", got)
	}
	if got := nextDaily(now, 2, 30); !got.Equal(at(2026, 7, 15, 2, 30)) {
		t.Fatalf("nextDaily after time-of-day = %v", got)
	}
	early := at(2026, 7, 14, 1, 0)
	if got := mostRecentDaily(early, 2, 30); !got.Equal(at(2026, 7, 13, 2, 30)) {
		t.Fatalf("mostRecentDaily before time-of-day = %v", got)
	}
	if got := nextDaily(early, 2, 30); !got.Equal(at(2026, 7, 14, 2, 30)) {
		t.Fatalf("nextDaily before time-of-day = %v", got)
	}
}

func TestWeeklyOccurrence(t *testing.T) {
	now := at(2026, 7, 14, 12, 0)
	if now.Weekday() != time.Tuesday {
		t.Fatalf("fixture weekday = %v, expected Tuesday", now.Weekday())
	}
	if got := mostRecentWeekly(now, time.Monday, 0, 0); !got.Equal(at(2026, 7, 13, 0, 0)) {
		t.Fatalf("mostRecentWeekly = %v", got)
	}
	if got := nextWeekly(now, time.Monday, 0, 0); !got.Equal(at(2026, 7, 20, 0, 0)) {
		t.Fatalf("nextWeekly = %v", got)
	}
}

func TestOccurrenceDSTIsSaneAndBounded(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	bases := []time.Time{
		time.Date(2026, 3, 8, 1, 0, 0, 0, loc),
		time.Date(2026, 3, 8, 2, 45, 0, 0, loc),
		time.Date(2026, 11, 1, 1, 0, 0, 0, loc),
	}
	for _, base := range bases {
		next := nextDaily(base, 2, 30)
		if !next.After(base) {
			t.Fatalf("nextDaily(%v) = %v must be strictly after base", base, next)
		}
		if next.Sub(base) > 48*time.Hour {
			t.Fatalf("nextDaily(%v) = %v is unreasonably far (occurrence math wedged near a DST edge)", base, next)
		}
		if recent := mostRecentDaily(base, 2, 30); recent.After(base) {
			t.Fatalf("mostRecentDaily(%v) = %v must be <= base", base, recent)
		}
		wnext := nextWeekly(base, time.Monday, 2, 30)
		if !wnext.After(base) || wnext.Sub(base) > 8*24*time.Hour {
			t.Fatalf("nextWeekly(%v) = %v is not a sane bounded future time", base, wnext)
		}
	}
}

func TestArmCatchUpVsNext(t *testing.T) {
	s := New(Config{}, Options{Jitter: noJitter})
	now := at(2026, 7, 14, 3, 0)
	recent := func(tt time.Time) time.Time { return mostRecentDaily(tt, 2, 30) }
	next := func(tt time.Time) time.Time { return nextDaily(tt, 2, 30) }

	missed := s.arm(now, at(2026, 7, 10, 2, 30).Unix(), recent, next, 0)
	if !missed.Equal(now) {
		t.Fatalf("a missed occurrence should arm at now, got %v", missed)
	}
	upToDate := s.arm(now, at(2026, 7, 14, 2, 30).Unix(), recent, next, 0)
	if !upToDate.Equal(at(2026, 7, 15, 2, 30)) {
		t.Fatalf("an up-to-date job should arm at the next occurrence, got %v", upToDate)
	}
}

func TestSchedulerCatchUpFiresBackup(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "backup-schedule.json")
	if err := os.WriteFile(statePath, []byte(`{"version":1,"backup_last_trigger":1,"check_last_trigger":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fired := make(chan struct{}, 1)
	s := New(Config{BackupTime: "00:00", CheckTime: "00:00", CheckWeekday: time.Monday}, Options{
		StatePath: statePath,
		Jitter:    noJitter,
		RunBackup: func(context.Context) error {
			select {
			case fired <- struct{}{}:
			default:
			}
			return nil
		},
		RunCheck: func(context.Context) error { return nil },
	})
	go s.Run(context.Background())
	defer func() { s.Stop(); s.Wait() }()
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("catch-up backup did not fire")
	}
}

func TestSchedulerFirstEnableWaits(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "backup-schedule.json")
	fired := make(chan struct{}, 1)
	s := New(Config{BackupTime: "00:00", CheckTime: "00:00", CheckWeekday: time.Monday}, Options{
		StatePath: statePath,
		Jitter:    noJitter,
		RunBackup: func(context.Context) error {
			select {
			case fired <- struct{}{}:
			default:
			}
			return nil
		},
		RunCheck: func(context.Context) error { return nil },
	})
	go s.Run(context.Background())
	defer func() { s.Stop(); s.Wait() }()
	select {
	case <-fired:
		t.Fatal("first enable (no prior state) must NOT fire immediately")
	case <-time.After(500 * time.Millisecond):
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("first enable should have written a state file: %v", err)
	}
}
