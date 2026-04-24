ALTER TABLE commander.commanders
    ADD COLUMN IF NOT EXISTS authentik_user_id UUID NULL,
    ADD COLUMN IF NOT EXISTS approved          BOOLEAN NOT NULL DEFAULT false;

-- Prevent two commanders pointing at the same Authentik user — a
-- one-to-one link is the only shape the admin UI supports.
CREATE UNIQUE INDEX IF NOT EXISTS idx_commanders_authentik_user_id
    ON commander.commanders(authentik_user_id)
    WHERE authentik_user_id IS NOT NULL;

-- Row-level security on commander.commanders. Today only journal_events
-- has RLS; extending it to commanders means a bug in the backend that
-- mixes up FIDs cannot leak one commander's link or approval state into
-- another commander's response. RLS kicks in on any SELECT/UPDATE
-- performed without the admin bypass role.
ALTER TABLE commander.commanders ENABLE ROW LEVEL SECURITY;
ALTER TABLE commander.commanders FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS commanders_self_rw ON commander.commanders;

-- Dedicated admin role that the Kaine admin endpoints use. BYPASSRLS is
-- scoped to the commander schema via the GRANT below — the role holds
-- no rights outside schema `commander`.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_admin') THEN
        CREATE ROLE edin_cmd_admin NOLOGIN BYPASSRLS;
    END IF;
END $$;

-- The RLS policy shape mirrors 006_rls_policies.sql — adapt to which
-- application roles are present so the policy works in both
-- production (Ansible-provisioned roles) and bare test environments.
DO $$
DECLARE
    has_writer BOOLEAN := EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer');
    has_reader BOOLEAN := EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_reader');
BEGIN
    IF has_writer AND has_reader THEN
        EXECUTE $policy$
            CREATE POLICY commanders_self_rw ON commander.commanders
                AS PERMISSIVE FOR ALL
                TO edin_cmd_reader, edin_cmd_writer
                USING      (fid = current_setting('app.current_fid', true))
                WITH CHECK (fid = current_setting('app.current_fid', true))
        $policy$;
    ELSIF has_writer THEN
        EXECUTE $policy$
            CREATE POLICY commanders_self_rw ON commander.commanders
                AS PERMISSIVE FOR ALL
                TO edin_cmd_writer
                USING      (fid = current_setting('app.current_fid', true))
                WITH CHECK (fid = current_setting('app.current_fid', true))
        $policy$;
    ELSIF has_reader THEN
        EXECUTE $policy$
            CREATE POLICY commanders_self_rw ON commander.commanders
                AS PERMISSIVE FOR ALL
                TO edin_cmd_reader
                USING      (fid = current_setting('app.current_fid', true))
                WITH CHECK (fid = current_setting('app.current_fid', true))
        $policy$;
    ELSE
        -- Bare test environment (no application roles present): PUBLIC policy.
        EXECUTE $policy$
            CREATE POLICY commanders_self_rw ON commander.commanders
                AS PERMISSIVE FOR ALL
                USING      (fid = current_setting('app.current_fid', true))
                WITH CHECK (fid = current_setting('app.current_fid', true))
        $policy$;
    END IF;
END $$;

-- Grant the admin role full DML on commander schema so the admin
-- endpoints (Task 8) can operate cross-FID. The BYPASSRLS attribute on
-- the role means RLS is skipped when the writer SET LOCAL ROLE's to
-- admin inside an admin-scoped transaction.
GRANT USAGE ON SCHEMA commander TO edin_cmd_admin;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES    IN SCHEMA commander TO edin_cmd_admin;
GRANT USAGE, SELECT                  ON ALL SEQUENCES IN SCHEMA commander TO edin_cmd_admin;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer') THEN
        -- Application connects as edin_cmd_writer; admin endpoints SET LOCAL
        -- ROLE edin_cmd_admin inside a transaction when they need cross-FID
        -- reads/writes. cmd_writer itself remains RLS-scoped.
        GRANT edin_cmd_admin TO edin_cmd_writer;
        -- Column-scoped UPDATE on commander.commanders — cmd_writer can set
        -- the link/approval but never rewrite identity fields (fid / cmdr_name).
        GRANT SELECT,
              UPDATE (authentik_user_id, approved, last_seen_at, cmdr_name)
           ON commander.commanders TO edin_cmd_writer;
    END IF;
END $$;
