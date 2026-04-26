-- Bot-owned schema. The edin_bot_writer role is granted USAGE+CREATE here
-- via the timescaledb ansible role (Phase 1.1). This migration is idempotent
-- and re-grants on every run as a safety net.
CREATE SCHEMA IF NOT EXISTS discord;
GRANT USAGE, CREATE ON SCHEMA discord TO edin_bot_writer;
ALTER DEFAULT PRIVILEGES IN SCHEMA discord GRANT ALL ON TABLES TO edin_bot_writer;
ALTER DEFAULT PRIVILEGES IN SCHEMA discord GRANT ALL ON SEQUENCES TO edin_bot_writer;
