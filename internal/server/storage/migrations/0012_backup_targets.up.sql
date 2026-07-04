CREATE TABLE backup_targets (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    secret_hash BYTEA NOT NULL,
    host_id BIGINT REFERENCES hosts(id) ON DELETE SET NULL,
    quota_bytes BIGINT,
    used_bytes BIGINT NOT NULL DEFAULT 0,
    usage_measured_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);
