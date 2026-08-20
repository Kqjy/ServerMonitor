DROP INDEX hosts_hostname_alive_idx;
DROP INDEX hosts_archived_at_idx;
ALTER TABLE hosts DROP COLUMN archived_at;
CREATE UNIQUE INDEX hosts_hostname_alive_idx ON hosts (hostname) WHERE deleted_at IS NULL;
