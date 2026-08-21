package storage

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"servermonitor/internal/server/secretbox"
)

var ErrBackupTargetNameTaken = errors.New("backup target name already in use")

type BackupTarget struct {
	ID                          int64
	Name                        string
	HostID                      *int64
	Hostname                    string
	NodeHostID                  *int64
	NodeHostname                string
	DestinationID               *int64
	DestinationName             string
	DestinationKind             string
	NamespacePrefix             string
	DirectCredentialsConfigured bool
	DirectCredentialsScoped     bool
	QuotaBytes                  *int64
	UsedBytes                   int64
	UsageMeasuredAt             *time.Time
	CreatedAt                   time.Time
	RevokedAt                   *time.Time
}

type backupTargetAuth struct {
	id      int64
	hash    [32]byte
	quota   int64
	used    int64
	revoked bool
}

type BackupTargets struct {
	db      *DB
	secrets *secretbox.Box

	mu       sync.RWMutex
	byName   map[string]backupTargetAuth
	cachedAt time.Time
	cacheTTL time.Duration
}

func NewBackupTargets(db *DB, boxes ...*secretbox.Box) *BackupTargets {
	b := &BackupTargets{
		db:       db,
		byName:   map[string]backupTargetAuth{},
		cacheTTL: 30 * time.Second,
	}
	if len(boxes) > 0 {
		b.secrets = boxes[0]
	}
	return b
}

func (b *BackupTargets) Create(ctx context.Context, name string, hostID *int64, quota *int64, secret string, nodeHostID *int64) (int64, error) {
	var id int64
	err := b.db.Pool.QueryRow(ctx, `
		INSERT INTO backup_targets (name, secret_hash, host_id, quota_bytes, node_host_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, name, tokenHash(secret), hostID, quota, nodeHostID).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, ErrBackupTargetNameTaken
		}
		return 0, err
	}
	b.invalidate()
	return id, nil
}

func (b *BackupTargets) Rotate(ctx context.Context, id int64, secret string) error {
	res, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET secret_hash = $2, revoked_at = NULL, updated_at = now()
		WHERE id = $1
	`, id, tokenHash(secret))
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	b.invalidate()
	return nil
}

func (b *BackupTargets) Revoke(ctx context.Context, id int64) error {
	res, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET revoked_at = now(), updated_at = now()
		WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return err
	}
	b.invalidate()
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (b *BackupTargets) Delete(ctx context.Context, id int64) error {
	res, err := b.db.Pool.Exec(ctx, `DELETE FROM backup_targets WHERE id = $1`, id)
	if err != nil {
		return err
	}
	b.invalidate()
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (b *BackupTargets) SetUsage(ctx context.Context, id int64, used int64) error {
	res, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET used_bytes = $2, usage_measured_at = now()
		WHERE id = $1
	`, id, used)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	b.mu.Lock()
	for name, a := range b.byName {
		if a.id == id {
			a.used = used
			b.byName[name] = a
		}
	}
	b.mu.Unlock()
	return nil
}

func (b *BackupTargets) SetQuota(ctx context.Context, id int64, quota *int64) error {
	res, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET quota_bytes = $2, updated_at = now()
		WHERE id = $1
	`, id, quota)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	b.invalidate()
	return nil
}

func (b *BackupTargets) ReserveUsage(ctx context.Context, id int64, delta int64) (bool, error) {
	if delta < 0 {
		return false, errors.New("backup usage reservation must be non-negative")
	}
	if delta == 0 {
		return true, nil
	}
	tag, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET used_bytes = used_bytes + $2
		WHERE id = $1 AND revoked_at IS NULL
		  AND (quota_bytes IS NULL OR quota_bytes = 0 OR used_bytes <= quota_bytes - $2)
	`, id, delta)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	b.mu.Lock()
	for name, a := range b.byName {
		if a.id == id {
			a.used += delta
			if a.used < 0 {
				a.used = 0
			}
			b.byName[name] = a
		}
	}
	b.mu.Unlock()
	return true, nil
}

func (b *BackupTargets) ReleaseUsage(ctx context.Context, id int64, delta int64) error {
	if delta <= 0 {
		return nil
	}
	tag, err := b.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET used_bytes = GREATEST(used_bytes - $2, 0)
		WHERE id = $1
	`, id, delta)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	b.mu.Lock()
	for name, a := range b.byName {
		if a.id == id {
			a.used -= delta
			if a.used < 0 {
				a.used = 0
			}
			b.byName[name] = a
		}
	}
	b.mu.Unlock()
	return nil
}

func (b *BackupTargets) ResolveTarget(ctx context.Context, repo, secret string) (id int64, quotaBytes int64, usedBytes int64, ok bool, err error) {
	candidate := tokenHash(secret)

	b.mu.RLock()
	fresh := time.Since(b.cachedAt) < b.cacheTTL
	if fresh {
		a, hit := b.byName[repo]
		b.mu.RUnlock()
		if !hit {
			return 0, 0, 0, false, nil
		}
		return matchTarget(a, candidate)
	}
	b.mu.RUnlock()

	if err := b.refresh(ctx); err != nil {
		return 0, 0, 0, false, err
	}
	b.mu.RLock()
	a, hit := b.byName[repo]
	b.mu.RUnlock()
	if !hit {
		return 0, 0, 0, false, nil
	}
	return matchTarget(a, candidate)
}

func matchTarget(a backupTargetAuth, candidate []byte) (int64, int64, int64, bool, error) {
	key := a.hash
	if subtle.ConstantTimeCompare(key[:], candidate) != 1 {
		return 0, 0, 0, false, nil
	}
	if a.revoked {
		return 0, 0, 0, false, nil
	}
	return a.id, a.quota, a.used, true, nil
}

func (b *BackupTargets) refresh(ctx context.Context) error {
	rows, err := b.db.Pool.Query(ctx, `
		SELECT id, name, secret_hash, COALESCE(quota_bytes, 0), used_bytes, revoked_at IS NOT NULL
		FROM backup_targets
		WHERE node_host_id IS NULL AND destination_id IS NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byName := make(map[string]backupTargetAuth)
	for rows.Next() {
		var (
			a    backupTargetAuth
			name string
			hash []byte
		)
		if err := rows.Scan(&a.id, &name, &hash, &a.quota, &a.used, &a.revoked); err != nil {
			return err
		}
		copy(a.hash[:], hash)
		byName[name] = a
	}
	if err := rows.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	b.byName = byName
	b.cachedAt = time.Now()
	b.mu.Unlock()
	return nil
}

func (b *BackupTargets) invalidate() {
	b.mu.Lock()
	b.cachedAt = time.Time{}
	b.mu.Unlock()
}

func (b *BackupTargets) List(ctx context.Context) ([]BackupTarget, error) {
	rows, err := b.db.Pool.Query(ctx, `
		SELECT t.id, t.name, t.host_id, COALESCE(h.hostname, ''), t.node_host_id, COALESCE(nh.hostname, ''),
		       t.destination_id, COALESCE(d.name, ''), COALESCE(d.kind, ''), COALESCE(t.namespace_prefix, ''),
		       (t.direct_credentials_ciphertext IS NOT NULL OR d.credentials_ciphertext IS NOT NULL),
		       t.direct_credentials_ciphertext IS NOT NULL, t.quota_bytes,
		       t.used_bytes, t.usage_measured_at, t.created_at, t.revoked_at
		FROM backup_targets t
		LEFT JOIN hosts h ON h.id = t.host_id AND h.deleted_at IS NULL
		LEFT JOIN hosts nh ON nh.id = t.node_host_id AND nh.deleted_at IS NULL
		LEFT JOIN backup_destinations d ON d.id = t.destination_id
		ORDER BY t.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupTarget{}
	for rows.Next() {
		var t BackupTarget
		if err := rows.Scan(&t.ID, &t.Name, &t.HostID, &t.Hostname, &t.NodeHostID, &t.NodeHostname,
			&t.DestinationID, &t.DestinationName, &t.DestinationKind, &t.NamespacePrefix, &t.DirectCredentialsConfigured,
			&t.DirectCredentialsScoped, &t.QuotaBytes,
			&t.UsedBytes, &t.UsageMeasuredAt, &t.CreatedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *BackupTargets) Get(ctx context.Context, id int64) (BackupTarget, error) {
	var t BackupTarget
	err := b.db.Pool.QueryRow(ctx, `
		SELECT t.id, t.name, t.host_id, COALESCE(h.hostname, ''), t.node_host_id, COALESCE(nh.hostname, ''),
		       t.destination_id, COALESCE(d.name, ''), COALESCE(d.kind, ''), COALESCE(t.namespace_prefix, ''),
		       (t.direct_credentials_ciphertext IS NOT NULL OR d.credentials_ciphertext IS NOT NULL),
		       t.direct_credentials_ciphertext IS NOT NULL, t.quota_bytes,
		       t.used_bytes, t.usage_measured_at, t.created_at, t.revoked_at
		FROM backup_targets t
		LEFT JOIN hosts h ON h.id = t.host_id AND h.deleted_at IS NULL
		LEFT JOIN hosts nh ON nh.id = t.node_host_id AND nh.deleted_at IS NULL
		LEFT JOIN backup_destinations d ON d.id = t.destination_id
		WHERE t.id = $1
	`, id).Scan(&t.ID, &t.Name, &t.HostID, &t.Hostname, &t.NodeHostID, &t.NodeHostname,
		&t.DestinationID, &t.DestinationName, &t.DestinationKind, &t.NamespacePrefix, &t.DirectCredentialsConfigured,
		&t.DirectCredentialsScoped, &t.QuotaBytes,
		&t.UsedBytes, &t.UsageMeasuredAt, &t.CreatedAt, &t.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BackupTarget{}, ErrNotFound
		}
		return BackupTarget{}, err
	}
	return t, nil
}

func normalizeBackupTargetName(name string) string {
	return strings.TrimSpace(name)
}
