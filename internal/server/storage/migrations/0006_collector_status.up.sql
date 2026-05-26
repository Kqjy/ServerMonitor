ALTER TABLE hosts
    ADD COLUMN collector_status JSONB NOT NULL DEFAULT '{}'::jsonb;
