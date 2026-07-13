CREATE TABLE backup_nodes (
    host_id BIGINT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
    udp_port INT NOT NULL DEFAULT 51821,
    endpoint TEXT NOT NULL,
    store_dir TEXT NOT NULL DEFAULT '',
    max_blob_bytes BIGINT NOT NULL DEFAULT 1073741824,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE backup_targets ADD COLUMN node_host_id BIGINT REFERENCES hosts(id);
CREATE INDEX backup_targets_node_host_id_idx ON backup_targets (node_host_id) WHERE node_host_id IS NOT NULL;
