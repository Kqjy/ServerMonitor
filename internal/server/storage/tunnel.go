package storage

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrTunnelKeyTaken = errors.New("wireguard public key already enrolled by another host")

type TunnelPeer struct {
	HostID     int64
	Hostname   string
	PublicKey  []byte
	TunnelIP   netip.Addr
	EnrolledAt time.Time
}

type BackupTunnelStore struct {
	db *DB
}

func NewBackupTunnel(db *DB) *BackupTunnelStore {
	return &BackupTunnelStore{db: db}
}

func (s *BackupTunnelStore) EnsureIdentity(ctx context.Context, subnet string, generate func() ([]byte, error)) ([]byte, error) {
	var key []byte
	err := s.db.Pool.QueryRow(ctx, `SELECT private_key FROM backup_tunnel WHERE id = 1`).Scan(&key)
	if err == nil {
		_, err = s.db.Pool.Exec(ctx, `UPDATE backup_tunnel SET subnet = $1 WHERE id = 1 AND subnet <> $1`, subnet)
		return key, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	fresh, err := generate()
	if err != nil {
		return nil, err
	}
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO backup_tunnel (id, private_key, subnet)
		VALUES (1, $1, $2)
		ON CONFLICT (id) DO NOTHING
	`, fresh, subnet)
	if err != nil {
		return nil, err
	}
	if err := s.db.Pool.QueryRow(ctx, `SELECT private_key FROM backup_tunnel WHERE id = 1`).Scan(&key); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *BackupTunnelStore) EnrollPeer(ctx context.Context, hostID int64, publicKey []byte, subnet netip.Prefix, reserved netip.Addr) (netip.Addr, error) {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `LOCK TABLE backup_tunnel_peers IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return netip.Addr{}, err
	}

	var existing netip.Addr
	err = tx.QueryRow(ctx, `SELECT tunnel_ip FROM backup_tunnel_peers WHERE host_id = $1`, hostID).Scan(&existing)
	switch {
	case err == nil:
		if _, err := tx.Exec(ctx, `
			UPDATE backup_tunnel_peers SET public_key = $2, enrolled_at = now()
			WHERE host_id = $1
		`, hostID, publicKey); err != nil {
			return netip.Addr{}, tunnelPeerError(err)
		}
		return existing, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return netip.Addr{}, err
	}

	rows, err := tx.Query(ctx, `SELECT tunnel_ip FROM backup_tunnel_peers`)
	if err != nil {
		return netip.Addr{}, err
	}
	used := map[netip.Addr]bool{reserved: true}
	for rows.Next() {
		var addr netip.Addr
		if err := rows.Scan(&addr); err != nil {
			rows.Close()
			return netip.Addr{}, err
		}
		used[addr.Unmap()] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return netip.Addr{}, err
	}

	allocated, err := firstFreeAddr(subnet, used)
	if err != nil {
		return netip.Addr{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO backup_tunnel_peers (host_id, public_key, tunnel_ip)
		VALUES ($1, $2, $3)
	`, hostID, publicKey, allocated); err != nil {
		return netip.Addr{}, tunnelPeerError(err)
	}
	return allocated, tx.Commit(ctx)
}

func tunnelPeerError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrTunnelKeyTaken
	}
	return err
}

func firstFreeAddr(subnet netip.Prefix, used map[netip.Addr]bool) (netip.Addr, error) {
	subnet = subnet.Masked()
	addr := subnet.Addr().Next()
	for i := 0; i < 1<<20; i++ {
		if !subnet.Contains(addr) {
			return netip.Addr{}, fmt.Errorf("tunnel subnet %s is exhausted", subnet)
		}
		if !used[addr] {
			return addr, nil
		}
		addr = addr.Next()
	}
	return netip.Addr{}, fmt.Errorf("tunnel subnet %s is exhausted", subnet)
}

func (s *BackupTunnelStore) ListPeers(ctx context.Context) ([]TunnelPeer, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT p.host_id, h.hostname, p.public_key, p.tunnel_ip, p.enrolled_at
		FROM backup_tunnel_peers p
		JOIN hosts h ON h.id = p.host_id AND h.deleted_at IS NULL
		ORDER BY h.hostname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TunnelPeer{}
	for rows.Next() {
		var p TunnelPeer
		if err := rows.Scan(&p.HostID, &p.Hostname, &p.PublicKey, &p.TunnelIP, &p.EnrolledAt); err != nil {
			return nil, err
		}
		p.TunnelIP = p.TunnelIP.Unmap()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *BackupTunnelStore) GetPeer(ctx context.Context, hostID int64) (TunnelPeer, error) {
	var p TunnelPeer
	err := s.db.Pool.QueryRow(ctx, `
		SELECT p.host_id, COALESCE(h.hostname, ''), p.public_key, p.tunnel_ip, p.enrolled_at
		FROM backup_tunnel_peers p
		LEFT JOIN hosts h ON h.id = p.host_id
		WHERE p.host_id = $1
	`, hostID).Scan(&p.HostID, &p.Hostname, &p.PublicKey, &p.TunnelIP, &p.EnrolledAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TunnelPeer{}, ErrNotFound
		}
		return TunnelPeer{}, err
	}
	p.TunnelIP = p.TunnelIP.Unmap()
	return p, nil
}

func (s *BackupTunnelStore) DeletePeer(ctx context.Context, hostID int64) error {
	res, err := s.db.Pool.Exec(ctx, `DELETE FROM backup_tunnel_peers WHERE host_id = $1`, hostID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
