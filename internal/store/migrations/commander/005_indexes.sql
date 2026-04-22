CREATE INDEX IF NOT EXISTS idx_journal_events_fid_time
    ON commander.journal_events (fid, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_journal_events_fid_type_time
    ON commander.journal_events (fid, event_type, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_journal_events_event_data
    ON commander.journal_events USING GIN (event_data jsonb_path_ops);
