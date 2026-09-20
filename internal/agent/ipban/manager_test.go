package ipban

import (
	"errors"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"servermonitor/pkg/wire"
)

type fakeEnforcer struct {
	removeErr error
	removed   []netip.Addr
}

func (f *fakeEnforcer) Setup() error                                  { return nil }
func (f *fakeEnforcer) AddLocal(netip.Addr, time.Duration) error      { return nil }
func (f *fakeEnforcer) FlushLocal() error                             { return nil }
func (f *fakeEnforcer) ReplaceFleet([]FleetEntry) error               { return nil }
func (f *fakeEnforcer) Active() ([]ActiveEntry, []ActiveEntry, error) { return nil, nil, nil }
func (f *fakeEnforcer) Close() error                                  { return nil }
func (f *fakeEnforcer) RemoveLocal(ip netip.Addr) error {
	if f.removeErr != nil {
		err := f.removeErr
		f.removeErr = nil
		return err
	}
	f.removed = append(f.removed, ip)
	return nil
}

func testManager(now time.Time, e Enforcer) *Manager {
	m := New(Options{Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }})
	m.supported = true
	m.havePolicy = true
	m.policy = wire.IPBanPolicy{Detect: true, Enforce: true, FindTimeS: 600}
	m.enforcer = e
	m.enforceState = enforceOK
	return m
}

func TestReportKeepsEventsUntilAcknowledged(t *testing.T) {
	now := time.Now().UTC()
	m := testManager(now, nil)
	m.pushEvent(wire.IPBanEvent{Time: now, IP: "203.0.113.1", Action: "ban"})
	m.pushEvent(wire.IPBanEvent{Time: now, IP: "203.0.113.2", Action: "ban"})
	first := m.Report()
	second := m.Report()
	if len(first.Events) != 2 || len(second.Events) != 2 {
		t.Fatalf("events were drained before acknowledgement: %d then %d", len(first.Events), len(second.Events))
	}
	if first.Events[0].ID == "" || first.Events[0].ID == first.Events[1].ID {
		t.Fatalf("event IDs are not stable and unique: %+v", first.Events)
	}
	m.AcknowledgeEvents(first.Events)
	if got := len(m.Report().Events); got != 0 {
		t.Fatalf("events after acknowledgement = %d", got)
	}
}

func TestFailedUnbanIsRetried(t *testing.T) {
	now := time.Now().UTC()
	ip := netip.MustParseAddr("203.0.113.9")
	e := &fakeEnforcer{removeErr: errors.New("temporary nft error")}
	m := testManager(now, e)
	m.tracker.Import(ip, now.Add(time.Hour))
	unban := []wire.IPBanUnban{{IP: ip.String(), At: now}}
	m.applyUnbans(unban)
	if !m.tracker.Banned(ip, now) || len(m.seenUnbans) != 0 {
		t.Fatal("failed kernel removal must remain pending")
	}
	m.enforceState = enforceOK
	m.applyUnbans(unban)
	if m.tracker.Banned(ip, now) || len(e.removed) != 1 || len(m.seenUnbans) != 1 {
		t.Fatalf("unban was not retried: banned=%v removed=%v seen=%d", m.tracker.Banned(ip, now), e.removed, len(m.seenUnbans))
	}
}

func TestUnbanWaitsForUnavailableEnforcer(t *testing.T) {
	now := time.Now().UTC()
	ip := netip.MustParseAddr("203.0.113.10")
	m := testManager(now, nil)
	m.enforceState = enforceError
	m.tracker.Import(ip, now.Add(time.Hour))
	unban := []wire.IPBanUnban{{IP: ip.String(), At: now}}

	m.applyUnbans(unban)
	if !m.tracker.Banned(ip, now) || len(m.seenUnbans) != 0 {
		t.Fatal("unban must remain pending while the kernel enforcer is unavailable")
	}

	e := &fakeEnforcer{}
	m.enforcer = e
	m.enforceState = enforceOK
	m.applyUnbans(unban)
	if m.tracker.Banned(ip, now) || len(e.removed) != 1 || len(m.seenUnbans) != 1 {
		t.Fatalf("pending unban was not applied after recovery: banned=%v removed=%v seen=%d", m.tracker.Banned(ip, now), e.removed, len(m.seenUnbans))
	}
}

func TestReconcileRemovesNewlyProtectedBan(t *testing.T) {
	now := time.Now().UTC()
	ip := netip.MustParseAddr("203.0.113.77")
	e := &fakeEnforcer{}
	m := testManager(now, e)
	m.tracker.Import(ip, now.Add(time.Hour))
	prefixes, _ := ParseAllowlist([]string{"203.0.113.0/24"})
	m.guard.SetAllowlist(prefixes)
	m.reconcileProtected()
	if m.tracker.Banned(ip, now) || len(e.removed) != 1 {
		t.Fatalf("protected ban not reconciled: banned=%v removed=%v", m.tracker.Banned(ip, now), e.removed)
	}
}
