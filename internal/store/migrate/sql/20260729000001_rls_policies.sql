-- Row-level tenant isolation, enforced by the database (ADR-TENANCY-001 §2-3).
--
-- Migration 20260728000001 gave every governed table an owner. This makes the
-- database refuse to return another tenant's rows, so isolation no longer
-- depends on every query remembering a predicate.
--
-- Two properties of this deployment shape the design:
--
-- 1. The application connects as the role that owns the tables. PostgreSQL
--    exempts a table's owner from its policies unless the table is declared
--    FORCE ROW LEVEL SECURITY, so policies without FORCE would never be
--    evaluated — isolation that reads as present in the schema and is absent at
--    runtime. FORCE is therefore set on every table.
--
-- 2. Background workers (dispatcher, relay, retention, policy consumer and
--    executor, integrity sweeper) legitimately process every tenant from one
--    process. They keep the owning role, whose BYPASSRLS attribute overrides
--    FORCE. Request-scoped connections instead assume oneops_app, which owns
--    nothing and holds no BYPASSRLS.
--
-- The policy is deliberately fail-closed. current_setting('app.tenant_id', true)
-- yields NULL when unset, and `tenant_id = NULL` is NULL, which is not true —
-- so a connection that has not declared a tenant sees no rows at all. The
-- obvious alternative, `OR current_setting(...) IS NULL`, would make a
-- forgotten assignment expose every tenant to every caller: the failure mode of
-- an omission would be total cross-tenant disclosure, which is the exact class
-- of bug this migration exists to prevent.

-- The application role -------------------------------------------------------
--
-- NOLOGIN by construction: it is a privilege set to be assumed with SET ROLE,
-- never a credential. There is no password to provision, rotate or leak, and it
-- cannot be used to open a connection even if its name is known.
--
-- Roles are cluster-wide while this migration is schema-scoped, so creation is
-- guarded and the grants below are re-applied per schema.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        CREATE ROLE oneops_app NOLOGIN;
    END IF;
END;
$$;

DO $$
DECLARE
    sch text := current_schema();
BEGIN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO oneops_app', sch);
    EXECUTE format(
        'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO oneops_app', sch);
    EXECUTE format('GRANT USAGE ON ALL SEQUENCES IN SCHEMA %I TO oneops_app', sch);
    -- Tables added by later migrations must be reachable without revisiting
    -- this one.
    EXECUTE format(
        'ALTER DEFAULT PRIVILEGES IN SCHEMA %I '
        'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO oneops_app', sch);
END;
$$;

-- Policies -------------------------------------------------------------------
--
-- Applied by loop rather than by hand so the set cannot drift from
-- tenantOwnedTables in the integration suite, which asserts the same list.
--
-- webhook_cursor and policy_cursor are excluded per ADR-TENANCY-001 §4: they
-- hold worker progress, not customer data, and are only ever read by the
-- BYPASSRLS role. `tenant` is excluded because resolving a bearer token to a
-- tenant necessarily happens before any tenant is known.
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'configuration_object',
        'configuration_metadata',
        'artifact_version',
        'dependency_edge',
        'idempotency_key',
        'audit_event',
        'audit_chain_head',
        'webhook',
        'webhook_delivery',
        'webhook_replay_job',
        'policy',
        'policy_execution'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);

        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        -- USING governs which rows are visible to SELECT/UPDATE/DELETE.
        -- WITH CHECK governs which rows may be written, so a tenant cannot
        -- insert a row labelled with another tenant's id.
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I '
            'USING (tenant_id = current_setting(''app.tenant_id'', true)) '
            'WITH CHECK (tenant_id = current_setting(''app.tenant_id'', true))', t);
    END LOOP;
END;
$$;
