package storage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrTombstoned    = errors.New("host deregistered")
	ErrArchived      = errors.New("host archived")
	ErrHostnameTaken = errors.New("hostname already in use")
)

type CollectorStatus struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type Host struct {
	ID                  int64
	Hostname            string
	OS                  string
	Arch                string
	Kernel              string
	CPUModel            string
	CPUCores            int
	CPUThreads          int
	AgentVersion        string
	SampleIntervalS     int
	EnabledCollectors   []string
	CollectorStatus     map[string]CollectorStatus
	Tags                map[string]string
	LastSeen            *time.Time
	CreatedAt           time.Time
	DeletedAt           *time.Time
	ArchivedAt          *time.Time
	AutoUpgrade         bool
	UpgradeRequestedAt  *time.Time
	ExternallyManaged   bool
	UpgradeStallSince   *time.Time
	UpgradeDispatchedAt *time.Time
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

type Hosts struct {
	db *DB

	mu           sync.RWMutex
	byToken      map[[32]byte]int64
	byID         map[int64]Host
	cachedAt     time.Time
	cacheTTL     time.Duration
	missRefresh  time.Time
	missCooldown time.Duration
}

func NewHosts(db *DB) *Hosts {
	return &Hosts{
		db:           db,
		byToken:      map[[32]byte]int64{},
		byID:         map[int64]Host{},
		cacheTTL:     30 * time.Second,
		missCooldown: time.Second,
	}
}

func (h *Hosts) Register(ctx context.Context, hostname, token string, intervalS int) (int64, error) {
	if intervalS <= 0 {
		intervalS = 10
	}
	hash := tokenHash(token)
	var id int64
	err := h.db.Pool.QueryRow(ctx, `
		INSERT INTO hosts (hostname, agent_token_hash, sample_interval_s)
		VALUES ($1, $2, $3)
		ON CONFLICT (hostname) WHERE deleted_at IS NULL AND archived_at IS NULL
		DO UPDATE SET agent_token_hash = EXCLUDED.agent_token_hash,
		              sample_interval_s = EXCLUDED.sample_interval_s
		WHERE hosts.last_seen IS NULL
		RETURNING id
	`, hostname, hash, intervalS).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrHostnameTaken
		}
		return 0, err
	}
	h.invalidate()
	return id, nil
}

type HostUpdate struct {
	Hostname        *string
	SampleIntervalS *int
	AutoUpgrade     *bool
}

func (h *Hosts) Update(ctx context.Context, id int64, u HostUpdate) error {
	if u.Hostname == nil && u.SampleIntervalS == nil && u.AutoUpgrade == nil {
		return nil
	}
	res, err := h.db.Pool.Exec(ctx, `
		UPDATE hosts SET
		  hostname = COALESCE($2, hostname),
		  sample_interval_s = COALESCE($3, sample_interval_s),
		  auto_upgrade = COALESCE($4, auto_upgrade),
		  upgrade_stall_since = CASE
		    WHEN $4::boolean IS FALSE AND upgrade_requested_at IS NULL THEN NULL
		    ELSE upgrade_stall_since
		  END,
		  upgrade_dispatched_at = CASE
		    WHEN $4::boolean IS FALSE AND upgrade_requested_at IS NULL THEN NULL
		    ELSE upgrade_dispatched_at
		  END
		WHERE id = $1 AND deleted_at IS NULL AND archived_at IS NULL
	`, id, u.Hostname, u.SampleIntervalS, u.AutoUpgrade)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrHostnameTaken
		}
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	h.invalidate()
	return nil
}

func (h *Hosts) RequestUpgrade(ctx context.Context, id int64) error {
	res, err := h.db.Pool.Exec(ctx, `
		UPDATE hosts SET upgrade_requested_at = now(), upgrade_stall_since = NULL, upgrade_dispatched_at = NULL
		WHERE id = $1 AND deleted_at IS NULL AND archived_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	h.invalidate()
	return nil
}

func (h *Hosts) ClearUpgradeRequest(ctx context.Context, id int64) error {
	_, err := h.db.Pool.Exec(ctx, `
		UPDATE hosts SET upgrade_requested_at = NULL
		WHERE id = $1 AND upgrade_requested_at IS NOT NULL
	`, id)
	if err != nil {
		return err
	}
	h.invalidate()
	return nil
}

func (h *Hosts) Delete(ctx context.Context, id int64) error {
	res, err := h.db.Pool.Exec(ctx, `
		UPDATE hosts SET deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	h.invalidate()
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (h *Hosts) Archive(ctx context.Context, id int64) error {
	res, err := h.db.Pool.Exec(ctx, `
		UPDATE hosts SET archived_at = now(), auto_upgrade = FALSE, upgrade_requested_at = NULL,
		                 upgrade_stall_since = NULL, upgrade_dispatched_at = NULL
		WHERE id = $1 AND deleted_at IS NULL AND archived_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	h.invalidate()
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (h *Hosts) Unarchive(ctx context.Context, id int64) error {
	res, err := h.db.Pool.Exec(ctx, `
		UPDATE hosts SET archived_at = NULL
		WHERE id = $1 AND deleted_at IS NULL AND archived_at IS NOT NULL
	`, id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrHostnameTaken
		}
		return err
	}
	h.invalidate()
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (h *Hosts) ResolveToken(ctx context.Context, token string) (int64, error) {
	candidate := tokenHash(token)

	h.mu.RLock()
	cached := time.Since(h.cachedAt) < h.cacheTTL
	if cached {
		id, hit := constantTimeLookup(h.byToken, candidate)
		if hit {
			host, hostOK := h.byID[id]
			h.mu.RUnlock()
			if hostOK && host.DeletedAt != nil {
				return 0, ErrTombstoned
			}
			if hostOK && host.ArchivedAt != nil {
				return 0, ErrArchived
			}
			return id, nil
		}
	}
	h.mu.RUnlock()

	if !h.claimMissRefresh() {
		return 0, ErrNotFound
	}
	if err := h.refresh(ctx); err != nil {
		return 0, err
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	id, hit := constantTimeLookup(h.byToken, candidate)
	if !hit {
		return 0, ErrNotFound
	}
	if host, hostOK := h.byID[id]; hostOK {
		if host.DeletedAt != nil {
			return 0, ErrTombstoned
		}
		if host.ArchivedAt != nil {
			return 0, ErrArchived
		}
	}
	return id, nil
}

func constantTimeLookup(byToken map[[32]byte]int64, candidate []byte) (int64, bool) {
	var (
		matchedID int64
		matched   int
	)
	for stored, id := range byToken {
		key := stored
		if subtle.ConstantTimeCompare(key[:], candidate) == 1 {
			matchedID = id
			matched = 1
		}
	}
	return matchedID, matched == 1
}

func (h *Hosts) refresh(ctx context.Context) error {
	rows, err := h.db.Pool.Query(ctx, `
		SELECT id, hostname, agent_token_hash, COALESCE(os,''), COALESCE(arch,''),
		       COALESCE(kernel,''), COALESCE(cpu_model,''), COALESCE(cpu_cores,0), COALESCE(cpu_threads,0),
		       COALESCE(agent_version,''),
		       sample_interval_s, enabled_collectors, tags, collector_status,
		       last_seen, created_at, deleted_at, archived_at,
		       auto_upgrade, upgrade_requested_at, externally_managed, upgrade_stall_since,
		       upgrade_dispatched_at
		FROM hosts
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	byToken := make(map[[32]byte]int64)
	byID := make(map[int64]Host)
	for rows.Next() {
		var (
			host       Host
			hash       []byte
			tags       []byte
			collStatus []byte
		)
		if err := rows.Scan(
			&host.ID, &host.Hostname, &hash, &host.OS, &host.Arch,
			&host.Kernel, &host.CPUModel, &host.CPUCores, &host.CPUThreads, &host.AgentVersion,
			&host.SampleIntervalS, &host.EnabledCollectors, &tags, &collStatus,
			&host.LastSeen, &host.CreatedAt, &host.DeletedAt, &host.ArchivedAt,
			&host.AutoUpgrade, &host.UpgradeRequestedAt, &host.ExternallyManaged, &host.UpgradeStallSince,
			&host.UpgradeDispatchedAt,
		); err != nil {
			return err
		}
		if len(tags) > 0 {
			_ = json.Unmarshal(tags, &host.Tags)
		}
		if len(collStatus) > 0 {
			_ = json.Unmarshal(collStatus, &host.CollectorStatus)
		}
		var key [32]byte
		copy(key[:], hash)
		byToken[key] = host.ID
		byID[host.ID] = host
	}
	if err := rows.Err(); err != nil {
		return err
	}

	h.mu.Lock()
	h.byToken = byToken
	h.byID = byID
	h.cachedAt = time.Now()
	h.mu.Unlock()
	return nil
}

func (h *Hosts) claimMissRefresh() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cachedAt.IsZero() {
		return true
	}
	if time.Since(h.missRefresh) < h.missCooldown {
		return false
	}
	h.missRefresh = time.Now()
	return true
}

func (h *Hosts) invalidate() {
	h.mu.Lock()
	h.cachedAt = time.Time{}
	h.missRefresh = time.Time{}
	h.mu.Unlock()
}

func (h *Hosts) Get(ctx context.Context, id int64) (Host, error) {
	h.mu.RLock()
	if time.Since(h.cachedAt) < h.cacheTTL {
		if host, ok := h.byID[id]; ok {
			h.mu.RUnlock()
			if host.DeletedAt != nil {
				return Host{}, ErrNotFound
			}
			return host, nil
		}
	}
	h.mu.RUnlock()

	if err := h.refresh(ctx); err != nil {
		return Host{}, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if host, ok := h.byID[id]; ok && host.DeletedAt == nil {
		return host, nil
	}
	return Host{}, ErrNotFound
}

func (h *Hosts) List(ctx context.Context) ([]Host, error) {
	if err := h.refresh(ctx); err != nil {
		return nil, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Host, 0, len(h.byID))
	for _, host := range h.byID {
		if host.DeletedAt != nil {
			continue
		}
		out = append(out, host)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Hostname), strings.ToLower(out[j].Hostname)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (h *Hosts) Touch(ctx context.Context, id int64, info HostInfoUpdate) (*time.Time, error) {
	tags := []byte("{}")
	if len(info.Tags) > 0 {
		b, err := json.Marshal(info.Tags)
		if err != nil {
			return nil, err
		}
		tags = b
	}
	collStatus := []byte("{}")
	if len(info.CollectorStatus) > 0 {
		b, err := json.Marshal(info.CollectorStatus)
		if err != nil {
			return nil, err
		}
		collStatus = b
	}
	var stallSince *time.Time
	var dispatchedAt *time.Time
	err := h.db.Pool.QueryRow(ctx, `
		UPDATE hosts SET
		  os = COALESCE(NULLIF($2,''), os),
		  arch = COALESCE(NULLIF($3,''), arch),
		  kernel = COALESCE(NULLIF($4,''), kernel),
		  cpu_model = COALESCE(NULLIF($11::text,''), cpu_model),
		  cpu_cores = COALESCE(NULLIF($12::int,0), cpu_cores),
		  cpu_threads = COALESCE(NULLIF($13::int,0), cpu_threads),
		  agent_version = COALESCE(NULLIF($5,''), agent_version),
		  enabled_collectors = CASE WHEN cardinality($6::text[]) > 0 THEN $6 ELSE enabled_collectors END,
		  tags = CASE WHEN $7::jsonb <> '{}'::jsonb THEN $7::jsonb ELSE tags END,
		  collector_status = CASE WHEN $8::jsonb <> '{}'::jsonb THEN $8::jsonb ELSE collector_status END,
		  externally_managed = COALESCE($9::boolean, externally_managed),
		  upgrade_stall_since = CASE
		    WHEN $10::boolean AND NOT COALESCE($9::boolean, externally_managed)
		         AND (upgrade_dispatched_at IS NOT NULL OR auto_upgrade OR upgrade_requested_at IS NOT NULL)
		    THEN CASE WHEN agent_version IS DISTINCT FROM COALESCE(NULLIF($5,''), agent_version)
		              THEN now()
		              ELSE COALESCE(upgrade_stall_since, now()) END
		    ELSE NULL
		  END,
		  upgrade_dispatched_at = CASE
		    WHEN $10::boolean AND NOT COALESCE($9::boolean, externally_managed)
		         AND (upgrade_dispatched_at IS NOT NULL OR auto_upgrade OR upgrade_requested_at IS NOT NULL)
		    THEN COALESCE(upgrade_dispatched_at, now())
		    ELSE NULL
		  END,
		  last_seen = now()
		WHERE id = $1 AND deleted_at IS NULL AND archived_at IS NULL
		RETURNING upgrade_stall_since, upgrade_dispatched_at
	`, id, info.OS, info.Arch, info.Kernel, info.AgentVersion, info.Collectors, tags, collStatus, info.ExternallyManaged, info.ShouldSelfUpgrade,
		info.CPUModel, info.CPUCores, info.CPUThreads).Scan(&stallSince, &dispatchedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	h.mu.RLock()
	cached, cacheOK := h.byID[id]
	cacheValid := time.Since(h.cachedAt) < h.cacheTTL
	h.mu.RUnlock()
	if !cacheValid || !cacheOK {
		return stallSince, nil
	}
	tagsChanged := len(info.Tags) > 0 && !maps.Equal(info.Tags, cached.Tags)
	statusChanged := len(info.CollectorStatus) > 0 && !maps.Equal(info.CollectorStatus, cached.CollectorStatus)
	managedChanged := info.ExternallyManaged != nil && *info.ExternallyManaged != cached.ExternallyManaged
	stallChanged := !timePtrEqual(stallSince, cached.UpgradeStallSince)
	dispatchChanged := !timePtrEqual(dispatchedAt, cached.UpgradeDispatchedAt)
	if tagsChanged || statusChanged || managedChanged || stallChanged || dispatchChanged {
		h.invalidate()
	}
	return stallSince, nil
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

type HostInfoUpdate struct {
	OS                string
	Arch              string
	Kernel            string
	CPUModel          string
	CPUCores          int
	CPUThreads        int
	AgentVersion      string
	Collectors        []string
	CollectorStatus   map[string]CollectorStatus
	Tags              map[string]string
	ExternallyManaged *bool
	ShouldSelfUpgrade bool
}
