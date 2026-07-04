CREATE TABLE backup_status (
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    repo TEXT NOT NULL,
    payload JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (host_id, repo)
);
