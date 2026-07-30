-- Row-level security for the tenant-scoped identity tables
-- (ADR-IDENTITY-002 §4).
--
-- This is a NEW migration rather than an edit of 20260729000001. That file
-- iterates a literal array inside a DO block, and Atlas migrations are
-- checksummed: editing it breaks atlas.sum and fails the `migrations` CI job.
-- The loop below is the same loop over the new tables.
--
-- It must not run before 20260804000004 creates the tables, and 20260728000001
-- records why the ordering matters in the other direction too: enabling forced
-- RLS before the application sets app.tenant_id takes the platform down. Here
-- the pool split already exists, so the policies are safe on arrival.
--
-- The policy is identical to the existing one — same name, same predicate, same
-- fail-closed behaviour. current_setting('app.tenant_id', true) returns NULL
-- when unset and NULL matches no row; the obvious `OR current_setting(...) IS
-- NULL` relaxation is refused here for the same reason it was refused there,
-- because it would make an unset session variable mean "see everything".
--
-- USING governs which rows SELECT/UPDATE/DELETE can see. WITH CHECK governs
-- which rows may be written, so a tenant cannot insert a membership labelled
-- with another tenant's id and thereby grant itself access to that boundary.

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'membership',
        'invitation'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);

        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I '
            'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
            'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))', t);
    END LOOP;
END;
$$;
