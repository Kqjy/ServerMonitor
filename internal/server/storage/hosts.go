package storage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrTombstoned    = errors.New("host deregistered")
	ErrHostnameTaken = errors.New("hostname already in use")
)

type CollectorStatus struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type Host struct {
	ID                 int64
	Hostname           string
	OS                 string
	Arch               string
	Kernel             string
	AgentVersion       string
	SampleIntervalS    int
	EnabledCollectors  []string
	CollectorStatus    map[string]CollectorStatus
	Tags               map[string]string
	LastSeen           *time.Time
	CreatedAt          time.Time
	DeletedAt          *time.Time
	AutoUpgrade        bool
	UpgradeRequestedAt *time.Time
	ExternallyManaged  bool
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

type Hosts struct {
	db *DB

	mu       sync.RWMutex
	byToken  map[[32]byte]int64
	byID     map[int64]Host
	cachedAt time.Time
	cacheTTL time.Duration
}

func NewHosts(db *DB) *Hosts {
	return &Hosts{
		db:       db,
		byToken:  map[[32]byte]int64{},
		byID:     map[int64]Host{},
		cacheTTL: 30 * time.Second,
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
		ON CONFLICT (hostname) WHERE deleted_at IS NULL
		DO UPDATE SET agent_token_hash = EXCLUDED.agent_token_hash,
		              sample_interval_s = EXCLUDED.sample_interval_s
		RETURNING id
	`, hostname, hash, intervalS).Scan(&id)
	if err != nil {
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
		  auto_upgrade = COALESCE($4, auto_upgrade)
		WHERE id = $1 AND deleted_at IS NULL
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
		UPDATE hosts SET upgrade_requested_at = now()
		WHERE id = $1 AND deleted_at IS NULL
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
			return id, nil
		}
	}
	h.mu.RUnlock()

	if err := h.refresh(ctx); err != nil {
		return 0, err
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	id, hit := constantTimeLookup(h.byToken, candidate)
	if !hit {
		return 0, ErrNotFound
	}
	if host, hostOK := h.byID[id]; hostOK && host.DeletedAt != nil {
		return 0, ErrTombstoned
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
		       COALESCE(kernel,''), COALESCE(agent_version,''),
		       sample_interval_s, enabled_collectors, tags, collector_status,
		       last_seen, created_at, deleted_at,
		       auto_upgrade, upgrade_requested_at, externally_managed
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
			&host.Kernel, &host.AgentVersion,
			&host.SampleIntervalS, &host.EnabledCollectors, &tags, &collStatus,
			&host.LastSeen, &host.CreatedAt, &host.DeletedAt,
			&host.AutoUpgrade, &host.UpgradeRequestedAt, &host.ExternallyManaged,
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

func (h *Hosts) invalidate() {
	h.mu.Lock()
	h.cachedAt = time.Time{}
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
	return out, nil
}

func (h *Hosts) Touch(ctx context.Context, id int64, info HostInfoUpdate) error {
	tags := []byte("{}")
	if len(info.Tags) > 0 {
		b, err := json.Marshal(info.Tags)
		if err != nil {
			return err
		}
		tags = b
	}
	collStatus := []byte("{}")
	if len(info.CollectorStatus) > 0 {
		b, err := json.Marshal(info.CollectorStatus)
		if err != nil {
			return err
		}
		collStatus = b
	}
	_, err := h.db.Pool.Exec(ctx, `
		UPDATE hosts SET
		  os = COALESCE(NULLIF($2,''), os),
		  arch = COALESCE(NULLIF($3,''), arch),
		  kernel = COALESCE(NULLIF($4,''), kernel),
		  agent_version = COALESCE(NULLIF($5,''), agent_version),
		  enabled_collectors = CASE WHEN cardinality($6::text[]) > 0 THEN $6 ELSE enabled_collectors END,
		  tags = CASE WHEN $7::jsonb <> '{}'::jsonb THEN $7::jsonb ELSE tags END,
		  collector_status = CASE WHEN $8::jsonb <> '{}'::jsonb THEN $8::jsonb ELSE collector_status END,
		  externally_managed = COALESCE($9::boolean, externally_managed),
		  last_seen = now()
		WHERE id = $1 AND deleted_at IS NULL
	`, id, info.OS, info.Arch, info.Kernel, info.AgentVersion, info.Collectors, tags, collStatus, info.ExternallyManaged)
	if err != nil {
		return err
	}
	h.mu.RLock()
	cached, cacheOK := h.byID[id]
	cacheValid := time.Since(h.cachedAt) < h.cacheTTL
	h.mu.RUnlock()
	if !cacheValid || !cacheOK {
		return nil
	}
	tagsChanged := len(info.Tags) > 0 && !maps.Equal(info.Tags, cached.Tags)
	statusChanged := len(info.CollectorStatus) > 0 && !maps.Equal(info.CollectorStatus, cached.CollectorStatus)
	managedChanged := info.ExternallyManaged != nil && *info.ExternallyManaged != cached.ExternallyManaged
	if tagsChanged || statusChanged || managedChanged {
		h.invalidate()
	}
	return nil
}

type HostInfoUpdate struct {
	OS                string
	Arch              string
	Kernel            string
	AgentVersion      string
	Collectors        []string
	CollectorStatus   map[string]CollectorStatus
	Tags              map[string]string
	ExternallyManaged *bool
}
