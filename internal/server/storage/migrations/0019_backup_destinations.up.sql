CREATE TABLE backup_destinations (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('direct_s3')),
    endpoint TEXT NOT NULL DEFAULT '',
    bucket TEXT NOT NULL,
    region TEXT NOT NULL DEFAULT '',
    prefix_template TEXT NOT NULL DEFAULT 'backups/hosts/{host_id}-{hostname}/{repository}',
    use_path_style BOOLEAN NOT NULL DEFAULT FALSE,
    credentials_ciphertext BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX backup_destinations_name_ci_idx ON backup_destinations (lower(name));

ALTER TABLE backup_targets
    ADD COLUMN destination_id BIGINT REFERENCES backup_destinations(id) ON DELETE RESTRICT,
    ADD COLUMN namespace_prefix TEXT,
    ADD COLUMN direct_credentials_ciphertext BYTEA,
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD CONSTRAINT backup_targets_one_destination_chk
        CHECK (node_host_id IS NULL OR destination_id IS NULL);

CREATE INDEX backup_targets_destination_id_idx
    ON backup_targets (destination_id)
    WHERE destination_id IS NOT NULL;

CREATE INDEX backup_targets_direct_host_idx
    ON backup_targets (host_id, updated_at)
    WHERE destination_id IS NOT NULL AND revoked_at IS NULL;
