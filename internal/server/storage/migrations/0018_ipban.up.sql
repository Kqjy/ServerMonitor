CREATE TABLE ipban_settings (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    enforce BOOLEAN NOT NULL DEFAULT FALSE,
    contribute BOOLEAN NOT NULL DEFAULT TRUE,
    apply_fleet BOOLEAN NOT NULL DEFAULT TRUE,
    mode TEXT NOT NULL DEFAULT 'normal',
    max_retry INT NOT NULL DEFAULT 5,
    find_time_s INT NOT NULL DEFAULT 600,
    ban_time_s INT NOT NULL DEFAULT 3600,
    ban_time_max_s INT NOT NULL DEFAULT 604800,
    ban_private BOOLEAN NOT NULL DEFAULT FALSE,
    fleet_min_hosts INT NOT NULL DEFAULT 2,
    fleet_min_bans INT NOT NULL DEFAULT 3,
    fleet_ttl_s INT NOT NULL DEFAULT 86400,
    allowlist TEXT[] NOT NULL DEFAULT '{}',
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO ipban_settings (id) VALUES (1);

CREATE TABLE ipban_hosts (
    host_id BIGINT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
    detect BOOLEAN,
    enforce BOOLEAN,
    contribute BOOLEAN,
    apply_fleet BOOLEAN,
    supported BOOLEAN NOT NULL DEFAULT FALSE,
    detect_state TEXT NOT NULL DEFAULT '',
    detect_message TEXT NOT NULL DEFAULT '',
    enforce_state TEXT NOT NULL DEFAULT '',
    enforce_message TEXT NOT NULL DEFAULT '',
    sources JSONB NOT NULL DEFAULT '[]'::jsonb,
    active_local INT NOT NULL DEFAULT 0,
    fleet_applied INT NOT NULL DEFAULT 0,
    applied_version BIGINT NOT NULL DEFAULT 0,
    reported_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ipban_active (
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    ip INET NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    failures INT NOT NULL DEFAULT 0,
    user_sample TEXT NOT NULL DEFAULT '',
    banned_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    enforced BOOLEAN NOT NULL DEFAULT FALSE,
    repeat_count INT NOT NULL DEFAULT 1,
    PRIMARY KEY (host_id, ip)
);
CREATE INDEX ipban_active_expires_idx ON ipban_active (expires_at);
CREATE INDEX ipban_active_ip_idx ON ipban_active (ip);

CREATE TABLE ipban_fleet (
    ip INET PRIMARY KEY,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    host_count INT NOT NULL DEFAULT 0,
    ban_count INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    source TEXT NOT NULL DEFAULT 'auto',
    note TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ipban_fleet_expires_idx ON ipban_fleet (expires_at);

CREATE TABLE ipban_suppress (
    ip INET PRIMARY KEY,
    until TIMESTAMPTZ NOT NULL
);

CREATE TABLE ipban_host_cmds (
    id BIGSERIAL PRIMARY KEY,
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    ip INET NOT NULL,
    action TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ipban_host_cmds_host_idx ON ipban_host_cmds (host_id, created_at);

CREATE TABLE ipban_events (
    id BIGSERIAL PRIMARY KEY,
    time TIMESTAMPTZ NOT NULL,
    host_id BIGINT,
    ip INET NOT NULL,
    action TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    failures INT NOT NULL DEFAULT 0,
    user_sample TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    enforced BOOLEAN NOT NULL DEFAULT FALSE,
    repeat_count INT NOT NULL DEFAULT 0,
    actor TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ipban_events_time_idx ON ipban_events (time DESC);
CREATE INDEX ipban_events_ip_time_idx ON ipban_events (ip, time DESC);
CREATE INDEX ipban_events_host_time_idx ON ipban_events (host_id, time DESC);
