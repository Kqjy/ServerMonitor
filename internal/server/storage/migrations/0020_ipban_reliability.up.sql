ALTER TABLE ipban_events
    ADD COLUMN agent_event_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN contributed BOOLEAN NOT NULL DEFAULT FALSE;

CREATE UNIQUE INDEX ipban_events_agent_event_idx
    ON ipban_events (host_id, agent_event_id)
    WHERE host_id IS NOT NULL AND agent_event_id <> '';

CREATE INDEX ipban_events_user_sample_idx
    ON ipban_events (ip, time DESC)
    WHERE action = 'ban' AND user_sample <> '';

ALTER TABLE ipban_hosts
    ADD COLUMN dropped_events INT NOT NULL DEFAULT 0,
    ADD COLUMN dropped_failures INT NOT NULL DEFAULT 0;
