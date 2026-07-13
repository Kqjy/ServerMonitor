CREATE TABLE backup_tunnel (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    private_key BYTEA NOT NULL,
    subnet TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE backup_tunnel_peers (
    host_id BIGINT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
    public_key BYTEA NOT NULL UNIQUE,
    tunnel_ip INET NOT NULL UNIQUE,
    enrolled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
