-- Bot-owned schema and grants are managed by the database ansible role
-- (edin-data/ansible/roles/databases/templates/edin-init.sql.j2). This file
-- is intentionally a no-op asserting the schema is reachable — running
-- 'CREATE SCHEMA IF NOT EXISTS' here would require CREATE-on-database, which
-- edin_bot_writer does not (and should not) have.
--
-- For a fresh test database where the bot connects as the testuser (which
-- DOES have CREATE-on-database), the schema is also created via the same
-- DO-block guard.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'discord') THEN
        -- Only attempt creation if we have privilege (test contexts do; bot in prod doesn't).
        EXECUTE 'CREATE SCHEMA discord';
    END IF;
END
$$;
