-- Unique index for InsertEvents deduplication.
-- ON CONFLICT (fid, timestamp, event_type) allows "insert-or-skip" semantics
-- when the client re-submits journal events that were already stored.
-- NOTE: TimescaleDB requires unique indexes on hypertables to include the
-- partitioning column (timestamp), which this index satisfies.
CREATE UNIQUE INDEX IF NOT EXISTS idx_journal_events_dedup
    ON commander.journal_events (fid, timestamp, event_type);
