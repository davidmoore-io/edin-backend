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

-- Table-level grants plus the implicit BIGSERIAL sequence (journal_events_id_seq):
-- inserting a row calls nextval() on the sequence, which requires USAGE. Without
-- it, inserts fail with "permission denied for sequence journal_events_id_seq".
-- We also set default privileges so any future sequence added to this schema
-- inherits the same grants automatically.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer') THEN
        GRANT SELECT, INSERT ON commander.journal_events TO edin_cmd_writer;
        GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA commander TO edin_cmd_writer;
        ALTER DEFAULT PRIVILEGES IN SCHEMA commander GRANT USAGE, SELECT ON SEQUENCES TO edin_cmd_writer;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_reader') THEN
        GRANT SELECT ON commander.journal_events TO edin_cmd_reader;
        GRANT SELECT ON ALL SEQUENCES IN SCHEMA commander TO edin_cmd_reader;
        ALTER DEFAULT PRIVILEGES IN SCHEMA commander GRANT SELECT ON SEQUENCES TO edin_cmd_reader;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_migrator') THEN
        GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA commander TO edin_migrator;
    END IF;
END $$;
