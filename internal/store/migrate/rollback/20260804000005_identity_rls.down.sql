-- Reverses 20260804000005_identity_rls.
--
-- Removes the policies and the forced-RLS flags while leaving the tables. This
-- is the narrow reversal, for rolling back the policy change alone without
-- destroying membership data.
--
-- It leaves the tables unprotected, so it is only safe as a step immediately
-- before 20260804000004's rollback, or while the application is not serving.
-- The schema validator refuses startup on a listed table without RLS, which is
-- what stops this state reaching production unnoticed.

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['membership', 'invitation']
    LOOP
        IF to_regclass(t) IS NOT NULL THEN
            EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
            EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
            EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
        END IF;
    END LOOP;
END;
$$;
