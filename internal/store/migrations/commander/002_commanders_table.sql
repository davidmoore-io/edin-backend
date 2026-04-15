CREATE TABLE IF NOT EXISTS commander.commanders (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    fid           TEXT        UNIQUE NOT NULL,
    cmdr_name     TEXT        NOT NULL,
    capi_pending  BOOLEAN     NOT NULL DEFAULT FALSE,
    platform      TEXT        NOT NULL DEFAULT 'frontier',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Roles are created by Ansible (Story 9.2); GRANT here assumes they exist.
-- In test environments, the test harness creates these roles.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer') THEN
        GRANT SELECT, INSERT, UPDATE ON commander.commanders TO edin_cmd_writer;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_reader') THEN
        GRANT SELECT ON commander.commanders TO edin_cmd_reader;
    END IF;
END $$;
