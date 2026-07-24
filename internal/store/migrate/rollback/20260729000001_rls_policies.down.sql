-- Reverses 20260729000001_rls_policies.sql.
--
-- Rolling this back removes database-enforced tenant isolation; the platform
-- then relies entirely on application predicates. The oneops_app role is left
-- in place because it is cluster-wide and may be in use by another schema.

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'configuration_object', 'configuration_metadata', 'artifact_version',
        'dependency_edge', 'idempotency_key', 'audit_event', 'audit_chain_head',
        'webhook', 'webhook_delivery', 'webhook_replay_job',
        'policy', 'policy_execution'
    ]
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
END;
$$;
