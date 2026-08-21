package ipban

import (
	"net/netip"
	"testing"
	"time"
)

var testPolicy = Policy{MaxRetry: 3, FindTime: 10 * time.Minute, BanTime: time.Hour, BanTimeMax: 4 * time.Hour}

func TestTrackerBansAtMaxRetry(t *testing.T) {
	tr := NewTracker(testPolicy)
	ip := netip.MustParseAddr("203.0.113.5")
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if _, banned := tr.Observe(ip, "root", base.Add(time.Duration(i)*time.Second)); banned {
			t.Fatalf("banned after %d failures", i+1)
		}
	}
	ban, banned := tr.Observe(ip, "admin", base.Add(2*time.Second))
	if !banned {
		t.Fatal("expected a ban at max_retry")
	}
	if ban.Failures != 3 || ban.User != "admin" || ban.Count != 1 {
		t.Fatalf("unexpected ban %+v", ban)
	}
	if !ban.Until.Equal(base.Add(2*time.Second + time.Hour)) {
		t.Fatalf("until %v", ban.Until)
	}
	if !tr.Banned(ip, base.Add(30*time.Minute)) {
		t.Fatal("should be banned")
	}
	if _, again := tr.Observe(ip, "root", base.Add(3*time.Second)); again {
		t.Fatal("failures during an active ban must not re-ban")
	}
	if tr.Banned(ip, base.Add(2*time.Hour)) {
		t.Fatal("ban should have expired")
	}
}

func TestTrackerFindTimeWindow(t *testing.T) {
	tr := NewTracker(testPolicy)
	ip := netip.MustParseAddr("203.0.113.6")
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr.Observe(ip, "", base)
	tr.Observe(ip, "", base.Add(time.Minute))
	if _, banned := tr.Observe(ip, "", base.Add(11*time.Minute)); banned {
		t.Fatal("old failures outside find_time must not count")
	}
	if _, banned := tr.Observe(ip, "", base.Add(12*time.Minute)); banned {
		t.Fatal("only two failures inside the window")
	}
	if _, banned := tr.Observe(ip, "", base.Add(13*time.Minute)); !banned {
		t.Fatal("third failure inside the window should ban")
	}
}

func TestTrackerEscalation(t *testing.T) {
	tr := NewTracker(testPolicy)
	ip := netip.MustParseAddr("203.0.113.7")
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	now := base
	want := []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 4 * time.Hour}
	for round, dur := range want {
		var ban Ban
		var banned bool
		for i := 0; i < 3; i++ {
			ban, banned = tr.Observe(ip, "", now.Add(time.Duration(i)*time.Second))
		}
		if !banned {
			t.Fatalf("round %d: expected ban", round)
		}
		if got := ban.Until.Sub(now.Add(2 * time.Second)); got != dur {
			t.Fatalf("round %d: duration %v, want %v", round, got, dur)
		}
		if ban.Count != round+1 {
			t.Fatalf("round %d: count %d", round, ban.Count)
		}
		now = ban.Until.Add(time.Minute)
	}
	now = now.Add(escalationWindow + time.Hour)
	var ban Ban
	for i := 0; i < 3; i++ {
		ban, _ = tr.Observe(ip, "", now.Add(time.Duration(i)*time.Second))
	}
	if got := ban.Until.Sub(now.Add(2 * time.Second)); got != time.Hour {
		t.Fatalf("escalation should reset after the window: got %v", got)
	}
}

func TestTrackerUnbanAndImport(t *testing.T) {
	tr := NewTracker(testPolicy)
	ip := netip.MustParseAddr("2001:db8::5")
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tr.Import(ip, base.Add(time.Hour))
	if !tr.Banned(ip, base) {
		t.Fatal("imported ban should be active")
	}
	if !tr.Unban(ip) {
		t.Fatal("unban should report a previous ban")
	}
	if tr.Banned(ip, base) || tr.Len() != 0 {
		t.Fatal("unban should clear state")
	}
	if tr.Unban(ip) {
		t.Fatal("second unban should report nothing")
	}
}

func TestTrackerPruneAndActive(t *testing.T) {
	tr := NewTracker(testPolicy)
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	idle := netip.MustParseAddr("203.0.113.8")
	banned := netip.MustParseAddr("203.0.113.9")
	tr.Observe(idle, "", base)
	for i := 0; i < 3; i++ {
		tr.Observe(banned, "", base.Add(time.Duration(i)*time.Second))
	}
	if removed := tr.Prune(base.Add(time.Minute)); removed != 0 {
		t.Fatalf("nothing should be pruned yet, removed %d", removed)
	}
	if removed := tr.Prune(base.Add(11 * time.Minute)); removed != 1 {
		t.Fatalf("idle entry should be pruned, removed %d", removed)
	}
	active := tr.ActiveBans(base.Add(11 * time.Minute))
	if len(active) != 1 || active[0].IP != banned {
		t.Fatalf("active bans %+v", active)
	}
	if removed := tr.Prune(base.Add(2 * time.Hour)); removed != 0 {
		t.Fatal("an expired ban inside the escalation window keeps its counter")
	}
	if tr.Len() != 1 {
		t.Fatalf("len %d", tr.Len())
	}
	if removed := tr.Prune(base.Add(escalationWindow + 3*time.Hour)); removed != 1 {
		t.Fatalf("escalation state should expire, removed %d", removed)
	}
}

func TestRateWindow(t *testing.T) {
	w := newRateWindow(time.Minute)
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		w.Add(base.Add(time.Duration(i) * 10 * time.Second))
	}
	if got := w.Count(base.Add(40 * time.Second)); got != 5 {
		t.Fatalf("count %d", got)
	}
	if got := w.Count(base.Add(65 * time.Second)); got != 4 {
		t.Fatalf("count after expiry %d", got)
	}
	if got := w.Count(base.Add(2 * time.Minute)); got != 0 {
		t.Fatalf("count after full expiry %d", got)
	}
}
