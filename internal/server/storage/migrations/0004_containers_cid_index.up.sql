CREATE INDEX IF NOT EXISTS containers_host_cid_time_idx ON containers (host_id, cid, time DESC);
