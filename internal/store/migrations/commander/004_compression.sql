-- TimescaleDB 2.18+ (columnar compression / "columnstore") is incompatible with
-- Row Level Security on the same hypertable. Since RLS is a hard security requirement
-- for multi-tenant commander isolation, we use a data-retention policy instead of
-- columnar compression for this hypertable.
--
-- In practice: old chunks (older than 90 days) are dropped automatically.
-- A separate cold-storage / export pipeline (future story) will handle archiving
-- data outside the TimescaleDB hypertable if long-term compressed storage is needed.
SELECT add_retention_policy('commander.journal_events', INTERVAL '90 days', if_not_exists => TRUE);
