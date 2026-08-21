package ipban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"servermonitor/pkg/wire"
)

const (
	evidenceWindow   = 7 * 24 * time.Hour
	cmdRetention     = 24 * time.Hour
	maxEventsPerPost = 500
	sweepInterval    = time.Minute
	manualBanMinTTL  = time.Minute
	manualBanMaxTTL  = 365 * 24 * time.Hour
)

var (
	ErrNotFound   = errors.New("not found")
	ErrValidation = errors.New("invalid")
)

type Settings struct {
	Enabled       bool      `json:"enabled"`
	Enforce       bool      `json:"enforce"`
	Contribute    bool      `json:"contribute"`
	ApplyFleet    bool      `json:"apply_fleet"`
	Mode          string    `json:"mode"`
	MaxRetry      int       `json:"max_retry"`
	FindTimeS     int       `json:"find_time_s"`
	BanTimeS      int       `json:"ban_time_s"`
	BanTimeMaxS   int       `json:"ban_time_max_s"`
	BanPrivate    bool      `json:"ban_private"`
	FleetMinHosts int       `json:"fleet_min_hosts"`
	FleetMinBans  int       `json:"fleet_min_bans"`
	FleetTTLS     int       `json:"fleet_ttl_s"`
	Allowlist     []string  `json:"allowlist"`
	Version       int64     `json:"version"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type HostPolicy struct {
	Detect     *bool `json:"detect"`
	Enforce    *bool `json:"enforce"`
	Contribute *bool `json:"contribute"`
	ApplyFleet *bool `json:"apply_fleet"`
}

type EffectivePolicy struct {
	Detect     bool `json:"detect"`
	Enforce    bool `json:"enforce"`
	Contribute bool `json:"contribute"`
	ApplyFleet bool `json:"apply_fleet"`
}

type HostView struct {
	HostID          int64                    `json:"host_id"`
	Hostname        string                   `json:"hostname"`
	OS              string                   `json:"os"`
	AgentVersion    string                   `json:"agent_version"`
	LastSeen        *time.Time               `json:"last_seen"`
	SampleIntervalS int                      `json:"sample_interval_s"`
	Archived        bool                     `json:"archived"`
	Override        HostPolicy               `json:"override"`
	Effective       EffectivePolicy          `json:"effective"`
	Supported       bool                     `json:"supported"`
	DetectState     string                   `json:"detect_state"`
	DetectMessage   string                   `json:"detect_message,omitempty"`
	EnforceState    string                   `json:"enforce_state"`
	EnforceMessage  string                   `json:"enforce_message,omitempty"`
	Sources         []wire.IPBanSourceStatus `json:"sources"`
	ActiveLocal     int                      `json:"active_local"`
	FleetApplied    int                      `json:"fleet_applied"`
	AppliedVersion  int64                    `json:"applied_version"`
	ReportedAt      *time.Time               `json:"reported_at"`
	ConfigStale     bool                     `json:"config_stale"`
}

type ActiveBan struct {
	HostID      int64     `json:"host_id"`
	Hostname    string    `json:"hostname"`
	IP          string    `json:"ip"`
	Source      string    `json:"source"`
	Failures    int       `json:"failures"`
	User        string    `json:"user,omitempty"`
	BannedAt    time.Time `json:"banned_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Enforced    bool      `json:"enforced"`
	RepeatCount int       `json:"repeat_count"`
	Fleet       bool      `json:"fleet"`
}

type FleetBan struct {
	IP          string    `json:"ip"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	HostCount   int       `json:"host_count"`
	BanCount    int       `json:"ban_count"`
	ExpiresAt   time.Time `json:"expires_at"`
	Source      string    `json:"source"`
	Note        string    `json:"note,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	ActiveHosts int       `json:"active_hosts"`
}

type Event struct {
	ID          int64      `json:"id"`
	Time        time.Time  `json:"time"`
	HostID      *int64     `json:"host_id"`
	Hostname    string     `json:"hostname,omitempty"`
	IP          string     `json:"ip"`
	Action      string     `json:"action"`
	Source      string     `json:"source,omitempty"`
	Failures    int        `json:"failures,omitempty"`
	User        string     `json:"user,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Enforced    bool       `json:"enforced"`
	RepeatCount int        `json:"repeat_count,omitempty"`
	Actor       string     `json:"actor,omitempty"`
	Note        string     `json:"note,omitempty"`
}

type EventFilter struct {
	HostID int64
	IP     string
	Limit  int
	Before time.Time
}

type Summary struct {
	HostsTotal      int `json:"hosts_total"`
	HostsDetecting  int `json:"hosts_detecting"`
	HostsEnforcing  int `json:"hosts_enforcing"`
	HostsObserving  int `json:"hosts_observing"`
	HostsBlocked    int `json:"hosts_blocked"`
	HostsNotCapable int `json:"hosts_not_capable"`
	ActiveLocal     int `json:"active_local"`
	FleetSize       int `json:"fleet_size"`
	Bans24h         int `json:"bans_24h"`
	FleetBans24h    int `json:"fleet_bans_24h"`
}

type Service struct {
	pool           *pgxpool.Pool
	logger         *slog.Logger
	eventRetention time.Duration
	version        atomic.Int64
	mu             sync.RWMutex
	settings       Settings
}

func New(pool *pgxpool.Pool, logger *slog.Logger, eventRetention time.Duration) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, logger: logger, eventRetention: eventRetention}
}

func (s *Service) Load(ctx context.Context) error {
	st, err := s.readSettings(ctx, s.pool)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = st
	s.mu.Unlock()
	s.version.Store(st.Version)
	return nil
}

func (s *Service) Version() int64 {
	return s.version.Load()
}

func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSettings(s.settings)
}

func cloneSettings(st Settings) Settings {
	st.Allowlist = append(make([]string, 0, len(st.Allowlist)), st.Allowlist...)
	return st
}

func (s *Service) readSettings(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}) (Settings, error) {
	var st Settings
	err := q.QueryRow(ctx, `
		SELECT enabled, enforce, contribute, apply_fleet, mode, max_retry, find_time_s, ban_time_s, ban_time_max_s,
		       ban_private, fleet_min_hosts, fleet_min_bans, fleet_ttl_s, allowlist, version, updated_at
		FROM ipban_settings WHERE id = 1
	`).Scan(&st.Enabled, &st.Enforce, &st.Contribute, &st.ApplyFleet, &st.Mode, &st.MaxRetry, &st.FindTimeS, &st.BanTimeS, &st.BanTimeMaxS,
		&st.BanPrivate, &st.FleetMinHosts, &st.FleetMinBans, &st.FleetTTLS, &st.Allowlist, &st.Version, &st.UpdatedAt)
	if err != nil {
		return Settings{}, err
	}
	if st.Allowlist == nil {
		st.Allowlist = []string{}
	}
	return st, nil
}

func (s *Service) bumpVersion(ctx context.Context, tx pgx.Tx) (int64, error) {
	var v int64
	if err := tx.QueryRow(ctx, `UPDATE ipban_settings SET version = version + 1, updated_at = now() WHERE id = 1 RETURNING version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func (s *Service) publishVersion(v int64) {
	s.version.Store(v)
	s.mu.Lock()
	s.settings.Version = v
	s.mu.Unlock()
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *Service) sweep(ctx context.Context) {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	statements := []string{
		`DELETE FROM ipban_active WHERE expires_at < now()`,
		`DELETE FROM ipban_fleet WHERE expires_at < now()`,
		`DELETE FROM ipban_suppress WHERE until < now()`,
		`DELETE FROM ipban_host_cmds WHERE created_at < now() - $1::interval`,
	}
	for _, stmt := range statements {
		var err error
		if strings.Contains(stmt, "$1") {
			_, err = s.pool.Exec(sctx, stmt, cmdRetention)
		} else {
			_, err = s.pool.Exec(sctx, stmt)
		}
		if err != nil {
			s.logger.Warn("ipban sweep", "err", err)
		}
	}
	if s.eventRetention > 0 {
		if _, err := s.pool.Exec(sctx, `DELETE FROM ipban_events WHERE time < now() - $1::interval`, s.eventRetention); err != nil {
			s.logger.Warn("ipban event retention", "err", err)
		}
	}
}

func (s *Service) effectiveFor(st Settings, p HostPolicy) EffectivePolicy {
	pick := func(override *bool, base bool) bool {
		if override != nil {
			return *override
		}
		return base
	}
	return EffectivePolicy{
		Detect:     pick(p.Detect, st.Enabled),
		Enforce:    pick(p.Enforce, st.Enforce),
		Contribute: pick(p.Contribute, st.Contribute),
		ApplyFleet: pick(p.ApplyFleet, st.ApplyFleet),
	}
}

func (s *Service) hostPolicy(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, hostID int64) (HostPolicy, error) {
	var p HostPolicy
	err := q.QueryRow(ctx, `SELECT detect, enforce, contribute, apply_fleet FROM ipban_hosts WHERE host_id = $1`, hostID).Scan(&p.Detect, &p.Enforce, &p.Contribute, &p.ApplyFleet)
	if errors.Is(err, pgx.ErrNoRows) {
		return HostPolicy{}, nil
	}
	return p, err
}

func (s *Service) AgentConfig(ctx context.Context, hostID int64) (*wire.IPBanConfig, error) {
	st := s.Settings()
	p, err := s.hostPolicy(ctx, s.pool, hostID)
	if err != nil {
		return nil, err
	}
	eff := s.effectiveFor(st, p)
	cfg := &wire.IPBanConfig{
		Version: s.Version(),
		Policy: wire.IPBanPolicy{
			Detect:      eff.Detect,
			Enforce:     eff.Enforce,
			ApplyFleet:  eff.ApplyFleet,
			Mode:        st.Mode,
			MaxRetry:    st.MaxRetry,
			FindTimeS:   st.FindTimeS,
			BanTimeS:    st.BanTimeS,
			BanTimeMaxS: st.BanTimeMaxS,
			BanPrivate:  st.BanPrivate,
		},
		Allowlist: st.Allowlist,
	}
	rows, err := s.pool.Query(ctx, `SELECT ip, expires_at FROM ipban_fleet WHERE expires_at > now() ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ip netip.Addr
		var exp time.Time
		if err := rows.Scan(&ip, &exp); err != nil {
			rows.Close()
			return nil, err
		}
		cfg.Fleet = append(cfg.Fleet, wire.IPBanFleetEntry{IP: ip.Unmap().String(), ExpiresAt: exp})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT ip, created_at FROM ipban_host_cmds WHERE host_id = $1 AND action = 'unban' ORDER BY created_at`, hostID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ip netip.Addr
		var at time.Time
		if err := rows.Scan(&ip, &at); err != nil {
			rows.Close()
			return nil, err
		}
		cfg.Unban = append(cfg.Unban, wire.IPBanUnban{IP: ip.Unmap().String(), At: at})
	}
	rows.Close()
	return cfg, rows.Err()
}

func (s *Service) Ingest(ctx context.Context, hostID int64, report *wire.IPBanReport) error {
	if report == nil {
		return nil
	}
	sources := report.Sources
	if sources == nil {
		sources = []wire.IPBanSourceStatus{}
	}
	sourcesJSON, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO ipban_hosts (host_id, supported, detect_state, detect_message, enforce_state, enforce_message, sources, active_local, fleet_applied, applied_version, reported_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, now(), now())
		ON CONFLICT (host_id) DO UPDATE SET
		  supported = EXCLUDED.supported,
		  detect_state = EXCLUDED.detect_state,
		  detect_message = EXCLUDED.detect_message,
		  enforce_state = EXCLUDED.enforce_state,
		  enforce_message = EXCLUDED.enforce_message,
		  sources = EXCLUDED.sources,
		  active_local = EXCLUDED.active_local,
		  fleet_applied = EXCLUDED.fleet_applied,
		  applied_version = EXCLUDED.applied_version,
		  reported_at = now(),
		  updated_at = now()
	`, hostID, report.Supported, report.Detect, report.DetectMessage, report.Enforce, report.EnforceMessage, sourcesJSON, report.ActiveLocal, report.FleetApplied, report.AppliedVersion); err != nil {
		return err
	}
	if len(report.Events) == 0 {
		return nil
	}
	events := report.Events
	if len(events) > maxEventsPerPost {
		events = events[:maxEventsPerPost]
	}
	st := s.Settings()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	policy, err := s.hostPolicy(ctx, tx, hostID)
	if err != nil {
		return err
	}
	contribute := s.effectiveFor(st, policy).Contribute
	allow := parsePrefixes(st.Allowlist)
	bumped := false
	now := time.Now()
	for _, ev := range events {
		ip, err := netip.ParseAddr(ev.IP)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		at := ev.Time
		if at.IsZero() || at.After(now.Add(2*time.Minute)) {
			at = now
		}
		switch ev.Action {
		case "ban":
			exp := ev.ExpiresAt
			if exp == nil {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO ipban_events (time, host_id, ip, action, source, failures, user_sample, expires_at, enforced, repeat_count)
				VALUES ($1, $2, $3::inet, 'ban', $4, $5, $6, $7, $8, $9)
			`, at, hostID, ip.String(), ev.Source, ev.Failures, ev.User, *exp, ev.Enforced, ev.Count); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO ipban_active (host_id, ip, source, failures, user_sample, banned_at, expires_at, enforced, repeat_count)
				VALUES ($1, $2::inet, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT (host_id, ip) DO UPDATE SET
				  source = EXCLUDED.source, failures = EXCLUDED.failures, user_sample = EXCLUDED.user_sample,
				  banned_at = EXCLUDED.banned_at, expires_at = EXCLUDED.expires_at, enforced = EXCLUDED.enforced, repeat_count = EXCLUDED.repeat_count
			`, hostID, ip.String(), ev.Source, ev.Failures, ev.User, at, *exp, ev.Enforced, ev.Count); err != nil {
				return err
			}
			if contribute && IsRoutable(ip) && !matchesAny(ip, allow) {
				changed, err := s.propagate(ctx, tx, st, ip, now)
				if err != nil {
					return err
				}
				bumped = bumped || changed
			}
		case "unban":
			if _, err := tx.Exec(ctx, `
				INSERT INTO ipban_events (time, host_id, ip, action, source)
				VALUES ($1, $2, $3::inet, 'unban', $4)
			`, at, hostID, ip.String(), ev.Source); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM ipban_active WHERE host_id = $1 AND ip = $2::inet`, hostID, ip.String()); err != nil {
				return err
			}
		}
	}
	var newVersion int64
	if bumped {
		if newVersion, err = s.bumpVersion(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if bumped {
		s.publishVersion(newVersion)
	}
	return nil
}

func (s *Service) propagate(ctx context.Context, tx pgx.Tx, st Settings, ip netip.Addr, now time.Time) (bool, error) {
	var hosts, bans int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT host_id), count(*)
		FROM ipban_events
		WHERE ip = $1::inet AND action = 'ban' AND host_id IS NOT NULL AND time > $2
	`, ip.String(), now.Add(-evidenceWindow)).Scan(&hosts, &bans); err != nil {
		return false, err
	}
	if hosts < st.FleetMinHosts && bans < st.FleetMinBans {
		return false, nil
	}
	var suppressed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ipban_suppress WHERE ip = $1::inet AND until > now())`, ip.String()).Scan(&suppressed); err != nil {
		return false, err
	}
	if suppressed {
		return false, nil
	}
	ttl := time.Duration(st.FleetTTLS) * time.Second
	var previous *time.Time
	var previousSource string
	err := tx.QueryRow(ctx, `SELECT expires_at, source FROM ipban_fleet WHERE ip = $1::inet`, ip.String()).Scan(&previous, &previousSource)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if previous != nil && previousSource != "auto" {
		return false, nil
	}
	expires := now.Add(ttl)
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_fleet (ip, first_seen, last_seen, host_count, ban_count, expires_at, source)
		VALUES ($1::inet, $2, $2, $3, $4, $5, 'auto')
		ON CONFLICT (ip) DO UPDATE SET
		  last_seen = EXCLUDED.last_seen,
		  host_count = EXCLUDED.host_count,
		  ban_count = EXCLUDED.ban_count,
		  expires_at = GREATEST(ipban_fleet.expires_at, EXCLUDED.expires_at)
	`, ip.String(), now, hosts, bans, expires); err != nil {
		return false, err
	}
	if previous == nil {
		note := fmt.Sprintf("banned on %d host(s), %d time(s) in the last 7 days", hosts, bans)
		if _, err := tx.Exec(ctx, `
			INSERT INTO ipban_events (time, ip, action, expires_at, note)
			VALUES ($1, $2::inet, 'fleet_ban', $3, $4)
		`, now, ip.String(), expires, note); err != nil {
			return false, err
		}
		return true, nil
	}
	return previous.Before(now.Add(ttl / 2)), nil
}

type SettingsInput struct {
	Enabled       *bool     `json:"enabled"`
	Enforce       *bool     `json:"enforce"`
	Contribute    *bool     `json:"contribute"`
	ApplyFleet    *bool     `json:"apply_fleet"`
	Mode          *string   `json:"mode"`
	MaxRetry      *int      `json:"max_retry"`
	FindTimeS     *int      `json:"find_time_s"`
	BanTimeS      *int      `json:"ban_time_s"`
	BanTimeMaxS   *int      `json:"ban_time_max_s"`
	BanPrivate    *bool     `json:"ban_private"`
	FleetMinHosts *int      `json:"fleet_min_hosts"`
	FleetMinBans  *int      `json:"fleet_min_bans"`
	FleetTTLS     *int      `json:"fleet_ttl_s"`
	Allowlist     *[]string `json:"allowlist"`
}

func applyInput(st Settings, in SettingsInput) Settings {
	if in.Enabled != nil {
		st.Enabled = *in.Enabled
	}
	if in.Enforce != nil {
		st.Enforce = *in.Enforce
	}
	if in.Contribute != nil {
		st.Contribute = *in.Contribute
	}
	if in.ApplyFleet != nil {
		st.ApplyFleet = *in.ApplyFleet
	}
	if in.Mode != nil {
		st.Mode = strings.ToLower(strings.TrimSpace(*in.Mode))
	}
	if in.MaxRetry != nil {
		st.MaxRetry = *in.MaxRetry
	}
	if in.FindTimeS != nil {
		st.FindTimeS = *in.FindTimeS
	}
	if in.BanTimeS != nil {
		st.BanTimeS = *in.BanTimeS
	}
	if in.BanTimeMaxS != nil {
		st.BanTimeMaxS = *in.BanTimeMaxS
	}
	if in.BanPrivate != nil {
		st.BanPrivate = *in.BanPrivate
	}
	if in.FleetMinHosts != nil {
		st.FleetMinHosts = *in.FleetMinHosts
	}
	if in.FleetMinBans != nil {
		st.FleetMinBans = *in.FleetMinBans
	}
	if in.FleetTTLS != nil {
		st.FleetTTLS = *in.FleetTTLS
	}
	if in.Allowlist != nil {
		st.Allowlist = *in.Allowlist
	}
	return st
}

func Validate(st Settings) (Settings, error) {
	if st.Mode != "normal" && st.Mode != "aggressive" {
		return st, fmt.Errorf("%w: mode must be normal or aggressive", ErrValidation)
	}
	if st.MaxRetry < 1 || st.MaxRetry > 100 {
		return st, fmt.Errorf("%w: max_retry must be between 1 and 100", ErrValidation)
	}
	if st.FindTimeS < 60 || st.FindTimeS > 86400 {
		return st, fmt.Errorf("%w: find_time_s must be between 60 and 86400", ErrValidation)
	}
	if st.BanTimeS < 60 || st.BanTimeS > 30*86400 {
		return st, fmt.Errorf("%w: ban_time_s must be between 60 and 2592000", ErrValidation)
	}
	if st.BanTimeMaxS < st.BanTimeS || st.BanTimeMaxS > 365*86400 {
		return st, fmt.Errorf("%w: ban_time_max_s must be between ban_time_s and 31536000", ErrValidation)
	}
	if st.FleetMinHosts < 1 || st.FleetMinHosts > 1000 {
		return st, fmt.Errorf("%w: fleet_min_hosts must be between 1 and 1000", ErrValidation)
	}
	if st.FleetMinBans < 1 || st.FleetMinBans > 10000 {
		return st, fmt.Errorf("%w: fleet_min_bans must be between 1 and 10000", ErrValidation)
	}
	if st.FleetTTLS < 300 || st.FleetTTLS > 30*86400 {
		return st, fmt.Errorf("%w: fleet_ttl_s must be between 300 and 2592000", ErrValidation)
	}
	normalized, err := NormalizeAllowlist(st.Allowlist)
	if err != nil {
		return st, err
	}
	st.Allowlist = normalized
	if st.Enforce && len(st.Allowlist) == 0 {
		return st, fmt.Errorf("%w: add at least one allowlisted address or network before turning enforcement on", ErrValidation)
	}
	return st, nil
}

func NormalizeAllowlist(entries []string) ([]string, error) {
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		var text string
		if p, err := netip.ParsePrefix(e); err == nil {
			text = p.Masked().String()
		} else if a, err := netip.ParseAddr(e); err == nil {
			a = a.Unmap()
			text = netip.PrefixFrom(a, a.BitLen()).String()
		} else {
			return nil, fmt.Errorf("%w: allowlist entry %q is not an IP address or CIDR", ErrValidation, raw)
		}
		if _, dup := seen[text]; dup {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	if len(out) > 1000 {
		return nil, fmt.Errorf("%w: allowlist is limited to 1000 entries", ErrValidation)
	}
	return out, nil
}

func (s *Service) UpdateSettings(ctx context.Context, in SettingsInput) (Settings, error) {
	current := s.Settings()
	next, err := Validate(applyInput(current, in))
	if err != nil {
		return Settings{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE ipban_settings SET
		  enabled = $1, enforce = $2, contribute = $3, apply_fleet = $4, mode = $5, max_retry = $6, find_time_s = $7,
		  ban_time_s = $8, ban_time_max_s = $9, ban_private = $10, fleet_min_hosts = $11, fleet_min_bans = $12, fleet_ttl_s = $13,
		  allowlist = $14, updated_at = now()
		WHERE id = 1
	`, next.Enabled, next.Enforce, next.Contribute, next.ApplyFleet, next.Mode, next.MaxRetry, next.FindTimeS,
		next.BanTimeS, next.BanTimeMaxS, next.BanPrivate, next.FleetMinHosts, next.FleetMinBans, next.FleetTTLS, next.Allowlist); err != nil {
		return Settings{}, err
	}
	v, err := s.bumpVersion(ctx, tx)
	if err != nil {
		return Settings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Settings{}, err
	}
	next.Version = v
	next.UpdatedAt = time.Now()
	s.mu.Lock()
	s.settings = cloneSettings(next)
	s.mu.Unlock()
	s.version.Store(v)
	return cloneSettings(next), nil
}

func (s *Service) UpdateHostPolicy(ctx context.Context, hostID int64, patch HostPolicy) (HostPolicy, error) {
	st := s.Settings()
	if patch.Enforce != nil && *patch.Enforce && len(st.Allowlist) == 0 {
		return HostPolicy{}, fmt.Errorf("%w: add at least one allowlisted address or network before turning enforcement on", ErrValidation)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return HostPolicy{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hosts WHERE id = $1 AND deleted_at IS NULL)`, hostID).Scan(&exists); err != nil {
		return HostPolicy{}, err
	}
	if !exists {
		return HostPolicy{}, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_hosts (host_id, detect, enforce, contribute, apply_fleet, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (host_id) DO UPDATE SET
		  detect = $2, enforce = $3, contribute = $4, apply_fleet = $5, updated_at = now()
	`, hostID, patch.Detect, patch.Enforce, patch.Contribute, patch.ApplyFleet); err != nil {
		return HostPolicy{}, err
	}
	v, err := s.bumpVersion(ctx, tx)
	if err != nil {
		return HostPolicy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return HostPolicy{}, err
	}
	s.publishVersion(v)
	return patch, nil
}

func (s *Service) Hosts(ctx context.Context) ([]HostView, error) {
	st := s.Settings()
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.hostname, COALESCE(h.os, ''), COALESCE(h.agent_version, ''), h.last_seen, h.sample_interval_s, h.archived_at IS NOT NULL,
		       p.detect, p.enforce, p.contribute, p.apply_fleet,
		       COALESCE(p.supported, false), COALESCE(p.detect_state, ''), COALESCE(p.detect_message, ''),
		       COALESCE(p.enforce_state, ''), COALESCE(p.enforce_message, ''), COALESCE(p.sources, '[]'::jsonb),
		       COALESCE(p.active_local, 0), COALESCE(p.fleet_applied, 0), COALESCE(p.applied_version, 0), p.reported_at
		FROM hosts h
		LEFT JOIN ipban_hosts p ON p.host_id = h.id
		WHERE h.deleted_at IS NULL
		ORDER BY h.hostname, h.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]HostView, 0)
	for rows.Next() {
		var v HostView
		var sources []wire.IPBanSourceStatus
		if err := rows.Scan(&v.HostID, &v.Hostname, &v.OS, &v.AgentVersion, &v.LastSeen, &v.SampleIntervalS, &v.Archived,
			&v.Override.Detect, &v.Override.Enforce, &v.Override.Contribute, &v.Override.ApplyFleet,
			&v.Supported, &v.DetectState, &v.DetectMessage, &v.EnforceState, &v.EnforceMessage, &sources,
			&v.ActiveLocal, &v.FleetApplied, &v.AppliedVersion, &v.ReportedAt); err != nil {
			return nil, err
		}
		if sources == nil {
			sources = []wire.IPBanSourceStatus{}
		}
		v.Sources = sources
		v.Effective = s.effectiveFor(st, v.Override)
		v.ConfigStale = v.ReportedAt != nil && v.AppliedVersion < st.Version && time.Since(v.ReportedAt.Add(0)) < 10*time.Minute && time.Since(st.UpdatedAt) > 2*time.Minute
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) Active(ctx context.Context, hostID int64, limit int) ([]ActiveBan, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.host_id, COALESCE(h.hostname, ''), a.ip, a.source, a.failures, a.user_sample, a.banned_at, a.expires_at, a.enforced, a.repeat_count,
		       EXISTS (SELECT 1 FROM ipban_fleet f WHERE f.ip = a.ip AND f.expires_at > now())
		FROM ipban_active a
		LEFT JOIN hosts h ON h.id = a.host_id
		WHERE a.expires_at > now() AND ($1 = 0 OR a.host_id = $1)
		ORDER BY a.banned_at DESC
		LIMIT $2
	`, hostID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ActiveBan, 0)
	for rows.Next() {
		var b ActiveBan
		var ip netip.Addr
		if err := rows.Scan(&b.HostID, &b.Hostname, &ip, &b.Source, &b.Failures, &b.User, &b.BannedAt, &b.ExpiresAt, &b.Enforced, &b.RepeatCount, &b.Fleet); err != nil {
			return nil, err
		}
		b.IP = ip.Unmap().String()
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) Fleet(ctx context.Context) ([]FleetBan, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.ip, f.first_seen, f.last_seen, f.host_count, f.ban_count, f.expires_at, f.source, f.note, f.created_by,
		       (SELECT count(*) FROM ipban_active a WHERE a.ip = f.ip AND a.expires_at > now())
		FROM ipban_fleet f
		WHERE f.expires_at > now()
		ORDER BY f.last_seen DESC
		LIMIT 5000
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FleetBan, 0)
	for rows.Next() {
		var b FleetBan
		var ip netip.Addr
		if err := rows.Scan(&ip, &b.FirstSeen, &b.LastSeen, &b.HostCount, &b.BanCount, &b.ExpiresAt, &b.Source, &b.Note, &b.CreatedBy, &b.ActiveHosts); err != nil {
			return nil, err
		}
		b.IP = ip.Unmap().String()
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) ManualBan(ctx context.Context, rawIP string, ttl time.Duration, note, actor string) (FleetBan, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return FleetBan{}, fmt.Errorf("%w: %q is not an IP address", ErrValidation, rawIP)
	}
	ip = ip.Unmap()
	if !IsRoutable(ip) {
		return FleetBan{}, fmt.Errorf("%w: %s is a private, loopback, link-local or multicast address and cannot be banned fleet-wide", ErrValidation, ip)
	}
	st := s.Settings()
	if matchesAny(ip, parsePrefixes(st.Allowlist)) {
		return FleetBan{}, fmt.Errorf("%w: %s is allowlisted", ErrValidation, ip)
	}
	if ttl < manualBanMinTTL || ttl > manualBanMaxTTL {
		return FleetBan{}, fmt.Errorf("%w: ttl must be between 1 minute and 365 days", ErrValidation)
	}
	note = strings.TrimSpace(note)
	if len(note) > 200 {
		note = note[:200]
	}
	now := time.Now()
	expires := now.Add(ttl)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return FleetBan{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM ipban_suppress WHERE ip = $1::inet`, ip.String()); err != nil {
		return FleetBan{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_fleet (ip, first_seen, last_seen, host_count, ban_count, expires_at, source, note, created_by)
		VALUES ($1::inet, $2, $2, 0, 0, $3, 'manual', $4, $5)
		ON CONFLICT (ip) DO UPDATE SET
		  last_seen = EXCLUDED.last_seen, expires_at = EXCLUDED.expires_at, source = 'manual', note = EXCLUDED.note, created_by = EXCLUDED.created_by
	`, ip.String(), now, expires, note, actor); err != nil {
		return FleetBan{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_events (time, ip, action, expires_at, actor, note)
		VALUES ($1, $2::inet, 'manual_ban', $3, $4, $5)
	`, now, ip.String(), expires, actor, note); err != nil {
		return FleetBan{}, err
	}
	v, err := s.bumpVersion(ctx, tx)
	if err != nil {
		return FleetBan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FleetBan{}, err
	}
	s.publishVersion(v)
	return FleetBan{IP: ip.String(), FirstSeen: now, LastSeen: now, ExpiresAt: expires, Source: "manual", Note: note, CreatedBy: actor}, nil
}

func (s *Service) FleetUnban(ctx context.Context, rawIP, actor string) error {
	ip, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return fmt.Errorf("%w: %q is not an IP address", ErrValidation, rawIP)
	}
	ip = ip.Unmap()
	st := s.Settings()
	now := time.Now()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM ipban_fleet WHERE ip = $1::inet`, ip.String())
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_suppress (ip, until) VALUES ($1::inet, $2)
		ON CONFLICT (ip) DO UPDATE SET until = EXCLUDED.until
	`, ip.String(), now.Add(time.Duration(st.FleetTTLS)*time.Second)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_host_cmds (host_id, ip, action, created_at)
		SELECT a.host_id, a.ip, 'unban', $2 FROM ipban_active a WHERE a.ip = $1::inet AND a.expires_at > now()
	`, ip.String(), now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_events (time, ip, action, actor)
		VALUES ($1, $2::inet, 'fleet_unban', $3)
	`, now, ip.String(), actor); err != nil {
		return err
	}
	v, err := s.bumpVersion(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.publishVersion(v)
	if tag.RowsAffected() == 0 {
		return nil
	}
	return nil
}

func (s *Service) HostUnban(ctx context.Context, hostID int64, rawIP, actor string) error {
	ip, err := netip.ParseAddr(strings.TrimSpace(rawIP))
	if err != nil {
		return fmt.Errorf("%w: %q is not an IP address", ErrValidation, rawIP)
	}
	ip = ip.Unmap()
	now := time.Now()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hosts WHERE id = $1 AND deleted_at IS NULL)`, hostID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ipban_host_cmds (host_id, ip, action, created_at) VALUES ($1, $2::inet, 'unban', $3)`, hostID, ip.String(), now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ipban_events (time, host_id, ip, action, actor)
		VALUES ($1, $2, $3::inet, 'manual_unban', $4)
	`, now, hostID, ip.String(), actor); err != nil {
		return err
	}
	v, err := s.bumpVersion(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.publishVersion(v)
	return nil
}

func (s *Service) Events(ctx context.Context, f EventFilter) ([]Event, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var ipFilter *string
	if strings.TrimSpace(f.IP) != "" {
		ip, err := netip.ParseAddr(strings.TrimSpace(f.IP))
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not an IP address", ErrValidation, f.IP)
		}
		text := ip.Unmap().String()
		ipFilter = &text
	}
	var before *time.Time
	if !f.Before.IsZero() {
		before = &f.Before
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.time, e.host_id, COALESCE(h.hostname, ''), e.ip, e.action, e.source, e.failures, e.user_sample, e.expires_at, e.enforced, e.repeat_count, e.actor, e.note
		FROM ipban_events e
		LEFT JOIN hosts h ON h.id = e.host_id
		WHERE ($1 = 0 OR e.host_id = $1)
		  AND ($2::inet IS NULL OR e.ip = $2::inet)
		  AND ($3::timestamptz IS NULL OR e.time < $3::timestamptz)
		ORDER BY e.time DESC, e.id DESC
		LIMIT $4
	`, f.HostID, ipFilter, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Event, 0)
	for rows.Next() {
		var e Event
		var ip netip.Addr
		if err := rows.Scan(&e.ID, &e.Time, &e.HostID, &e.Hostname, &ip, &e.Action, &e.Source, &e.Failures, &e.User, &e.ExpiresAt, &e.Enforced, &e.RepeatCount, &e.Actor, &e.Note); err != nil {
			return nil, err
		}
		e.IP = ip.Unmap().String()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) Summary(ctx context.Context) (Summary, error) {
	hosts, err := s.Hosts(ctx)
	if err != nil {
		return Summary{}, err
	}
	var sum Summary
	for _, h := range hosts {
		if h.Archived {
			continue
		}
		sum.HostsTotal++
		if !h.Supported || h.DetectState == "" {
			continue
		}
		switch h.DetectState {
		case "ok":
			sum.HostsDetecting++
		case "no_permission", "unavailable":
			sum.HostsNotCapable++
		case "error":
			sum.HostsBlocked++
		}
		if h.DetectState == "ok" {
			switch {
			case h.Effective.Enforce && h.EnforceState == "ok":
				sum.HostsEnforcing++
			case !h.Effective.Enforce:
				sum.HostsObserving++
			case h.EnforceState == "no_permission" || h.EnforceState == "unsupported":
				sum.HostsNotCapable++
			case h.EnforceState == "error":
				sum.HostsBlocked++
			}
		}
		sum.ActiveLocal += h.ActiveLocal
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ipban_fleet WHERE expires_at > now()`).Scan(&sum.FleetSize); err != nil {
		return Summary{}, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE action = 'ban'), count(*) FILTER (WHERE action IN ('fleet_ban', 'manual_ban'))
		FROM ipban_events WHERE time > now() - interval '24 hours'
	`).Scan(&sum.Bans24h, &sum.FleetBans24h); err != nil {
		return Summary{}, err
	}
	return sum, nil
}

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
}

func IsRoutable(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip.Is4() && (ip.As4()[0] == 0 || ip == netip.MustParseAddr("255.255.255.255")) {
		return false
	}
	for _, p := range privateRanges {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func parsePrefixes(entries []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p)
		} else if a, err := netip.ParseAddr(e); err == nil {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

func matchesAny(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
