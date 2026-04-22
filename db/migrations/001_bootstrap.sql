CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE IF NOT EXISTS commanders (
    fid         TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS journal_events (
    time            TIMESTAMPTZ     NOT NULL,
    fid             TEXT            NOT NULL REFERENCES commanders(fid),
    event_type      TEXT            NOT NULL,
    event_data      JSONB           NOT NULL,
    session_id      UUID,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

SELECT create_hypertable('journal_events', by_range('time'));

CREATE INDEX IF NOT EXISTS idx_journal_events_fid ON journal_events(fid, time DESC);
