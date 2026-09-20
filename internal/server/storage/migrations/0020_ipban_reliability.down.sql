ALTER TABLE ipban_hosts
    DROP COLUMN dropped_failures,
    DROP COLUMN dropped_events;

DROP INDEX ipban_events_user_sample_idx;
DROP INDEX ipban_events_agent_event_idx;

ALTER TABLE ipban_events
    DROP COLUMN contributed,
    DROP COLUMN agent_event_id;
