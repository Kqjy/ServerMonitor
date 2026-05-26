CREATE TABLE ports (
    time TIMESTAMPTZ NOT NULL,
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    proto TEXT NOT NULL,
    addr TEXT NOT NULL,
    port INT NOT NULL,
    pid INT,
    process TEXT
);
SELECT create_hypertable('ports', 'time', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX ports_host_time_idx ON ports (host_id, time DESC);
CREATE INDEX ports_host_port_time_idx ON ports (host_id, port, time DESC);
