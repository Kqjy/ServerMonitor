DROP INDEX backup_targets_node_host_id_idx;
ALTER TABLE backup_targets DROP COLUMN node_host_id;
DROP TABLE backup_nodes;
