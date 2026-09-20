package ipban

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func baseSettings() Settings {
	return Settings{
		Enabled: true, Contribute: true, ApplyFleet: true, Mode: "normal",
		MaxRetry: 5, FindTimeS: 600, BanTimeS: 3600, BanTimeMaxS: 604800,
		FleetMinHosts: 2, FleetMinBans: 3, FleetTTLS: 86400, Allowlist: []string{},
	}
}

func TestValidateDefaultsPass(t *testing.T) {
	if _, err := Validate(baseSettings()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsEnforceWithoutAllowlist(t *testing.T) {
	st := baseSettings()
	st.Enforce = true
	_, err := Validate(st)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	st.Allowlist = []string{"203.0.113.7"}
	out, err := Validate(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Allowlist) != 1 || out.Allowlist[0] != "203.0.113.7/32" {
		t.Fatalf("allowlist = %v", out.Allowlist)
	}
}

func TestValidateRanges(t *testing.T) {
	cases := []func(*Settings){
		func(s *Settings) { s.Mode = "loud" },
		func(s *Settings) { s.MaxRetry = 0 },
		func(s *Settings) { s.FindTimeS = 30 },
		func(s *Settings) { s.BanTimeS = 10 },
		func(s *Settings) { s.BanTimeMaxS = s.BanTimeS - 1 },
		func(s *Settings) { s.FleetMinHosts = 0 },
		func(s *Settings) { s.FleetMinBans = 0 },
		func(s *Settings) { s.FleetTTLS = 10 },
		func(s *Settings) { s.Allowlist = []string{"not-an-ip"} },
	}
	for i, mutate := range cases {
		st := baseSettings()
		mutate(&st)
		if _, err := Validate(st); !errors.Is(err, ErrValidation) {
			t.Errorf("case %d: expected validation error, got %v", i, err)
		}
	}
}

func TestNormalizeAllowlist(t *testing.T) {
	out, err := NormalizeAllowlist([]string{" 203.0.113.7 ", "203.0.113.7/32", "198.51.100.77/24", "2001:db8::1", "", "::ffff:203.0.113.8", "::ffff:192.0.2.0/120"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"203.0.113.7/32", "198.51.100.0/24", "2001:db8::1/128", "203.0.113.8/32", "192.0.2.0/24"}
	if len(out) != len(want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("got %v, want %v", out, want)
		}
	}
}

func TestApplyInputOnlyTouchesProvidedFields(t *testing.T) {
	st := baseSettings()
	retry := 9
	mode := " Aggressive "
	next := applyInput(st, SettingsInput{MaxRetry: &retry, Mode: &mode})
	if next.MaxRetry != 9 || next.Mode != "aggressive" || next.FindTimeS != 600 || !next.Enabled {
		t.Fatalf("unexpected %+v", next)
	}
}

func TestIsRoutable(t *testing.T) {
	for _, addr := range []string{"10.0.0.1", "127.0.0.1", "fe80::1", "100.64.1.1", "::", "0.0.0.0", "224.0.0.1", "255.255.255.255", "fd00::5", "192.168.1.1", "172.31.0.1"} {
		if IsRoutable(netip.MustParseAddr(addr)) {
			t.Errorf("%s should not be routable", addr)
		}
	}
	for _, addr := range []string{"203.0.113.5", "2001:db8::9", "8.8.8.8", "::ffff:203.0.113.5"} {
		if !IsRoutable(netip.MustParseAddr(addr)) {
			t.Errorf("%s should be routable", addr)
		}
	}
}

func TestEffectivePolicyOverrides(t *testing.T) {
	s := &Service{}
	st := baseSettings()
	st.Enforce = true
	off := false
	on := true
	eff := s.effectiveFor(st, HostPolicy{Enforce: &off, Contribute: nil, ApplyFleet: &on})
	if eff.Enforce || !eff.Contribute || !eff.ApplyFleet || !eff.Detect {
		t.Fatalf("effective = %+v", eff)
	}
}

func eventPagingTestService(t *testing.T) *Service {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed event paging test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return New(pool, nil, 90*24*time.Hour)
}

func TestEventsKeysetPagingCoversTiesAndGaps(t *testing.T) {
	svc := eventPagingTestService(t)
	ctx := context.Background()
	ip := "203.0.113.77"
	if _, err := svc.pool.Exec(ctx, `DELETE FROM ipban_events WHERE ip = $1::inet`, ip); err != nil {
		t.Fatalf("clean: %v", err)
	}
	t.Cleanup(func() {
		_, _ = svc.pool.Exec(context.Background(), `DELETE FROM ipban_events WHERE ip = $1::inet`, ip)
	})
	base := time.Now().UTC().Truncate(time.Second)
	const total = 11
	for i := 0; i < total; i++ {
		at := base.Add(-time.Duration(i/3) * time.Minute)
		if _, err := svc.pool.Exec(ctx, `
			INSERT INTO ipban_events (time, host_id, ip, action, source, failures)
			VALUES ($1, NULL, $2::inet, 'ban', 'test', $3)
		`, at, ip, i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	seen := make([]Event, 0, total)
	ids := map[int64]bool{}
	f := EventFilter{IP: ip, Limit: 2}
	for pages := 0; pages < 20; pages++ {
		batch, err := svc.Events(ctx, f)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, e := range batch {
			if ids[e.ID] {
				t.Fatalf("event %d returned twice", e.ID)
			}
			ids[e.ID] = true
			seen = append(seen, e)
		}
		last := batch[len(batch)-1]
		f.Before = last.Time
		f.BeforeID = last.ID
	}
	if len(seen) != total {
		t.Fatalf("paged %d events, want %d", len(seen), total)
	}
	for i := 1; i < len(seen); i++ {
		prev, cur := seen[i-1], seen[i]
		if cur.Time.After(prev.Time) || (cur.Time.Equal(prev.Time) && cur.ID >= prev.ID) {
			t.Fatalf("order broken at %d: %v/%d then %v/%d", i, prev.Time, prev.ID, cur.Time, cur.ID)
		}
	}
}
