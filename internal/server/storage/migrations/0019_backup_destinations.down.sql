ALTER TABLE backup_targets
    DROP CONSTRAINT IF EXISTS backup_targets_one_destination_chk,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS direct_credentials_ciphertext,
    DROP COLUMN IF EXISTS namespace_prefix,
    DROP COLUMN IF EXISTS destination_id;

DROP TABLE IF EXISTS backup_destinations;
