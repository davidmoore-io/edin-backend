CREATE TABLE IF NOT EXISTS commander.journal_events (
    id             BIGSERIAL,
    commander_id   UUID        NOT NULL REFERENCES commander.commanders(id),
    fid            TEXT        NOT NULL,
    timestamp      TIMESTAMPTZ NOT NULL,
    event_type     TEXT        NOT NULL,
    event_data     JSONB       NOT NULL,
    client_version TEXT,
    ingested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY    (fid, timestamp, id)
);

SELECT create_hypertable('commander.journal_events', by_range('timestamp', INTERVAL '7 days'), if_not_exists => TRUE);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer') THEN
        GRANT SELECT, INSERT ON commander.journal_events TO edin_cmd_writer;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_reader') THEN
        GRANT SELECT ON commander.journal_events TO edin_cmd_reader;
    END IF;
END $$;
