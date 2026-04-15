ALTER TABLE commander.journal_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE commander.journal_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS commander_isolation ON commander.journal_events;

-- Create the RLS policy. When the application roles exist (production / Ansible-provisioned
-- environments) we scope it to those roles only. In bare test environments (no roles yet)
-- we fall back to PUBLIC so the policy still exercises the USING clause.
DO $$
DECLARE
    has_writer BOOLEAN := EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer');
    has_reader BOOLEAN := EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_reader');
BEGIN
    IF has_writer AND has_reader THEN
        EXECUTE $policy$
            CREATE POLICY commander_isolation ON commander.journal_events
                AS PERMISSIVE FOR ALL
                TO edin_cmd_reader, edin_cmd_writer
                USING (fid = current_setting('app.current_fid', true))
        $policy$;
    ELSIF has_writer THEN
        EXECUTE $policy$
            CREATE POLICY commander_isolation ON commander.journal_events
                AS PERMISSIVE FOR ALL
                TO edin_cmd_writer
                USING (fid = current_setting('app.current_fid', true))
        $policy$;
    ELSIF has_reader THEN
        EXECUTE $policy$
            CREATE POLICY commander_isolation ON commander.journal_events
                AS PERMISSIVE FOR ALL
                TO edin_cmd_reader
                USING (fid = current_setting('app.current_fid', true))
        $policy$;
    ELSE
        -- No application roles present (bare test environment); apply policy to PUBLIC.
        EXECUTE $policy$
            CREATE POLICY commander_isolation ON commander.journal_events
                AS PERMISSIVE FOR ALL
                USING (fid = current_setting('app.current_fid', true))
        $policy$;
    END IF;
END $$;
