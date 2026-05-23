CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE hosts (
    id BIGSERIAL PRIMARY KEY,
    hostname TEXT NOT NULL,
    agent_token_hash BYTEA NOT NULL,
    os TEXT,
    arch TEXT,
    kernel TEXT,
    agent_version TEXT,
    sample_interval_s INT NOT NULL DEFAULT 10,
    enabled_collectors TEXT[] NOT NULL DEFAULT '{}',
    tags JSONB NOT NULL DEFAULT '{}',
    last_seen TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX hosts_last_seen_idx ON hosts (last_seen DESC NULLS LAST);
CREATE UNIQUE INDEX hosts_hostname_alive_idx ON hosts (hostname) WHERE deleted_at IS NULL;
CREATE INDEX hosts_deleted_at_idx ON hosts (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE metric_points (
    time TIMESTAMPTZ NOT NULL,
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    metric SMALLINT NOT NULL,
    labels JSONB NOT NULL DEFAULT '{}',
    value DOUBLE PRECISION NOT NULL
);
SELECT create_hypertable('metric_points', 'time', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX metric_points_host_metric_time_idx ON metric_points (host_id, metric, time DESC);
CREATE INDEX metric_points_labels_idx ON metric_points USING GIN (labels);

ALTER TABLE metric_points SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'host_id,metric',
    timescaledb.compress_orderby = 'time DESC'
);

CREATE MATERIALIZED VIEW metric_points_5m
WITH (timescaledb.continuous) AS
SELECT time_bucket('5 minutes', time) AS bucket,
       host_id, metric, labels,
       avg(value) AS avg,
       min(value) AS min,
       max(value) AS max,
       last(value, time) AS last_val
FROM metric_points
GROUP BY bucket, host_id, metric, labels
WITH NO DATA;

SELECT add_continuous_aggregate_policy(
    'metric_points_5m',
    start_offset => INTERVAL '1 day',
    end_offset => INTERVAL '5 minutes',
    schedule_interval => INTERVAL '5 minutes'
);

CREATE TABLE processes (
    time TIMESTAMPTZ NOT NULL,
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    pid INT NOT NULL,
    name TEXT NOT NULL,
    user_ TEXT,
    cmdline TEXT,
    cpu_pct REAL,
    mem_rss BIGINT,
    status TEXT,
    nthreads INT
);
SELECT create_hypertable('processes', 'time', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX processes_host_time_idx ON processes (host_id, time DESC);

CREATE TABLE containers (
    time TIMESTAMPTZ NOT NULL,
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    cid TEXT NOT NULL,
    name TEXT,
    image TEXT,
    state TEXT,
    cpu_pct REAL,
    mem_used BIGINT,
    mem_limit BIGINT,
    rx_bytes BIGINT,
    tx_bytes BIGINT
);
SELECT create_hypertable('containers', 'time', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX containers_host_time_idx ON containers (host_id, time DESC);

CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash BYTEA NOT NULL,
    role TEXT NOT NULL DEFAULT 'admin',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    failed_login_count INT NOT NULL DEFAULT 0,
    locked_until TIMESTAMPTZ
);

CREATE TABLE sessions (
    token BYTEA PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE notification_channels (
    id SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    kind TEXT NOT NULL,
    config JSONB NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE alert_rules (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    host_selector JSONB NOT NULL DEFAULT '{"all":true}',
    metric SMALLINT NOT NULL,
    label_selector JSONB NOT NULL DEFAULT '{}',
    comparator TEXT NOT NULL,
    threshold DOUBLE PRECISION NOT NULL,
    window_s INT NOT NULL,
    for_s INT NOT NULL,
    agg TEXT NOT NULL DEFAULT 'avg',
    severity TEXT NOT NULL DEFAULT 'warning',
    cooldown_s INT NOT NULL DEFAULT 600,
    channel_ids INT[] NOT NULL DEFAULT '{}',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE alert_states (
    rule_id INT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    label_key TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    since TIMESTAMPTZ NOT NULL,
    last_notified TIMESTAMPTZ,
    last_value DOUBLE PRECISION,
    PRIMARY KEY (rule_id, host_id, label_key)
);
CREATE INDEX alert_states_active_idx ON alert_states (host_id) WHERE state IN ('pending', 'firing');

CREATE TABLE alert_history (
    id BIGSERIAL PRIMARY KEY,
    rule_id INT NOT NULL,
    host_id BIGINT NOT NULL,
    label_key TEXT NOT NULL DEFAULT '',
    fired_at TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ,
    value DOUBLE PRECISION,
    labels JSONB NOT NULL DEFAULT '{}',
    severity TEXT
);
CREATE INDEX alert_history_fired_idx ON alert_history (fired_at DESC);

CREATE TABLE dashboards (
    id SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    definition JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE archive_manifests (
    id BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL,
    bucket_from TIMESTAMPTZ NOT NULL,
    bucket_to TIMESTAMPTZ NOT NULL,
    host_id BIGINT,
    s3_key TEXT NOT NULL,
    row_count BIGINT NOT NULL DEFAULT 0,
    byte_size BIGINT NOT NULL DEFAULT 0,
    uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX archive_manifests_range_idx ON archive_manifests (bucket_from, bucket_to);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id BIGSERIAL PRIMARY KEY,
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    remote_ip TEXT,
    status INT,
    extra JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
