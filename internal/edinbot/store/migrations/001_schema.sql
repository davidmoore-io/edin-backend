-- Bot-owned schema. The edin_bot_writer role is granted USAGE+CREATE here
-- via the timescaledb ansible role (Phase 1.1). This migration is idempotent
-- and re-grants on every run as a safety net.
--
-- The grants below are guarded by a role-exists check so this migration runs
-- cleanly against a fresh test database (where the connected user owns the
-- schema directly and edin_bot_writer does not exist).
CREATE SCHEMA IF NOT EXISTS discord;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'edin_bot_writer') THEN
        EXECUTE 'GRANT USAGE, CREATE ON SCHEMA discord TO edin_bot_writer';
        EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA discord GRANT ALL ON TABLES TO edin_bot_writer';
        EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA discord GRANT ALL ON SEQUENCES TO edin_bot_writer';
    END IF;
END
$$;
