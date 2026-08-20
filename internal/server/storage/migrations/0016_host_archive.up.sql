ALTER TABLE hosts ADD COLUMN archived_at TIMESTAMPTZ;
CREATE INDEX hosts_archived_at_idx ON hosts (archived_at) WHERE archived_at IS NOT NULL;
DROP INDEX hosts_hostname_alive_idx;
CREATE UNIQUE INDEX hosts_hostname_alive_idx ON hosts (hostname) WHERE deleted_at IS NULL AND archived_at IS NULL;
