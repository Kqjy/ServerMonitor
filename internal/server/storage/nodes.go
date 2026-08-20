package storage

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrNodeHasTargets = errors.New("backup node still has repositories; delete or reassign them first")

type BackupNode struct {
	HostID       int64
	Hostname     string
	UDPPort      int
	Endpoint     string
	StoreDir     string
	MaxBlobBytes int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	TunnelIP     *netip.Addr
	PublicKey    []byte
	TargetCount  int
	UsedBytes    int64
}

type NodeTarget struct {
	Name       string
	SecretHash []byte
	QuotaBytes int64
	Revoked    bool
}

type NodePeer struct {
	HostID    int64
	PublicKey []byte
	TunnelIP  netip.Addr
}

type NodeConfig struct {
	Enabled      bool
	UDPPort      int
	Endpoint     string
	StoreDir     string
	MaxBlobBytes int64
	TunnelIP     *netip.Addr
	Targets      []NodeTarget
	Peers        []NodePeer
}

type BackupNodes struct {
	db *DB
}

func NewBackupNodes(db *DB) *BackupNodes {
	return &BackupNodes{db: db}
}

func (n *BackupNodes) Promote(ctx context.Context, hostID int64, udpPort int, endpoint, storeDir string, maxBlobBytes int64) error {
	_, err := n.db.Pool.Exec(ctx, `
		INSERT INTO backup_nodes (host_id, udp_port, endpoint, store_dir, max_blob_bytes)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (host_id) DO UPDATE
		SET udp_port = $2, endpoint = $3, store_dir = $4, max_blob_bytes = $5, updated_at = now()
	`, hostID, udpPort, endpoint, storeDir, maxBlobBytes)
	return err
}

func (n *BackupNodes) UpdateEndpoint(ctx context.Context, hostID int64, udpPort int, endpoint string) error {
	res, err := n.db.Pool.Exec(ctx, `
		UPDATE backup_nodes SET udp_port = $2, endpoint = $3, updated_at = now()
		WHERE host_id = $1
	`, hostID, udpPort, endpoint)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (n *BackupNodes) HasActiveTargets(ctx context.Context, hostID int64) (bool, error) {
	var targetCount int
	err := n.db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM backup_targets WHERE node_host_id = $1 AND revoked_at IS NULL
	`, hostID).Scan(&targetCount)
	if err != nil {
		return false, err
	}
	return targetCount > 0, nil
}

func (n *BackupNodes) Demote(ctx context.Context, hostID int64) error {
	hasTargets, err := n.HasActiveTargets(ctx, hostID)
	if err != nil {
		return err
	}
	if hasTargets {
		return ErrNodeHasTargets
	}
	res, err := n.db.Pool.Exec(ctx, `DELETE FROM backup_nodes WHERE host_id = $1`, hostID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (n *BackupNodes) List(ctx context.Context) ([]BackupNode, error) {
	rows, err := n.db.Pool.Query(ctx, `
		SELECT b.host_id, h.hostname, b.udp_port, b.endpoint, b.store_dir, b.max_blob_bytes,
		       b.created_at, b.updated_at, p.tunnel_ip, p.public_key,
		       (SELECT count(*) FROM backup_targets t WHERE t.node_host_id = b.host_id AND t.revoked_at IS NULL),
		       COALESCE((SELECT sum(t.used_bytes) FROM backup_targets t WHERE t.node_host_id = b.host_id), 0)
		FROM backup_nodes b
		JOIN hosts h ON h.id = b.host_id AND h.deleted_at IS NULL
		LEFT JOIN backup_tunnel_peers p ON p.host_id = b.host_id
		ORDER BY h.hostname
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupNode{}
	for rows.Next() {
		var node BackupNode
		if err := rows.Scan(&node.HostID, &node.Hostname, &node.UDPPort, &node.Endpoint, &node.StoreDir,
			&node.MaxBlobBytes, &node.CreatedAt, &node.UpdatedAt, &node.TunnelIP, &node.PublicKey, &node.TargetCount, &node.UsedBytes); err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

func (n *BackupNodes) Get(ctx context.Context, hostID int64) (BackupNode, error) {
	var node BackupNode
	err := n.db.Pool.QueryRow(ctx, `
		SELECT b.host_id, COALESCE(h.hostname, ''), b.udp_port, b.endpoint, b.store_dir, b.max_blob_bytes,
		       b.created_at, b.updated_at, p.tunnel_ip
		FROM backup_nodes b
		LEFT JOIN hosts h ON h.id = b.host_id AND h.deleted_at IS NULL
		LEFT JOIN backup_tunnel_peers p ON p.host_id = b.host_id
		WHERE b.host_id = $1
	`, hostID).Scan(&node.HostID, &node.Hostname, &node.UDPPort, &node.Endpoint, &node.StoreDir,
		&node.MaxBlobBytes, &node.CreatedAt, &node.UpdatedAt, &node.TunnelIP)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BackupNode{}, ErrNotFound
		}
		return BackupNode{}, err
	}
	return node, nil
}

func (n *BackupNodes) ConfigForHost(ctx context.Context, hostID int64) (NodeConfig, error) {
	node, err := n.Get(ctx, hostID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return NodeConfig{Enabled: false}, nil
		}
		return NodeConfig{}, err
	}
	cfg := NodeConfig{
		Enabled:      true,
		UDPPort:      node.UDPPort,
		Endpoint:     node.Endpoint,
		StoreDir:     node.StoreDir,
		MaxBlobBytes: node.MaxBlobBytes,
		TunnelIP:     node.TunnelIP,
	}

	targetRows, err := n.db.Pool.Query(ctx, `
		SELECT name, secret_hash, COALESCE(quota_bytes, 0), revoked_at IS NOT NULL
		FROM backup_targets
		WHERE node_host_id = $1
		ORDER BY name
	`, hostID)
	if err != nil {
		return NodeConfig{}, err
	}
	defer targetRows.Close()
	for targetRows.Next() {
		var t NodeTarget
		if err := targetRows.Scan(&t.Name, &t.SecretHash, &t.QuotaBytes, &t.Revoked); err != nil {
			return NodeConfig{}, err
		}
		cfg.Targets = append(cfg.Targets, t)
	}
	if err := targetRows.Err(); err != nil {
		return NodeConfig{}, err
	}

	peerRows, err := n.db.Pool.Query(ctx, `
		SELECT DISTINCT p.host_id, p.public_key, p.tunnel_ip
		FROM backup_tunnel_peers p
		JOIN backup_targets t ON t.host_id = p.host_id
		JOIN hosts h ON h.id = p.host_id AND h.deleted_at IS NULL
		WHERE t.node_host_id = $1 AND t.revoked_at IS NULL
	`, hostID)
	if err != nil {
		return NodeConfig{}, err
	}
	defer peerRows.Close()
	for peerRows.Next() {
		var p NodePeer
		if err := peerRows.Scan(&p.HostID, &p.PublicKey, &p.TunnelIP); err != nil {
			return NodeConfig{}, err
		}
		p.TunnelIP = p.TunnelIP.Unmap()
		cfg.Peers = append(cfg.Peers, p)
	}
	return cfg, peerRows.Err()
}

func (n *BackupNodes) SetTargetUsage(ctx context.Context, nodeHostID int64, name string, used int64) error {
	_, err := n.db.Pool.Exec(ctx, `
		UPDATE backup_targets SET used_bytes = GREATEST($3, 0), usage_measured_at = now()
		WHERE node_host_id = $1 AND name = $2
	`, nodeHostID, name, used)
	return err
}
