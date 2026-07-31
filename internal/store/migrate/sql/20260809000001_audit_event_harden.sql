-- OPS-S039 Close the live-proven bypass of the constitutional audit chain.
-- Realises ADR-AUDIT-008. Additive: no completed migration is altered.
--
-- WHAT WAS OPEN. audit_event's two append-only guards
-- (trg_audit_event_no_row_mutate, trg_audit_event_no_truncate) were created in
-- 20260723000001 in the default "origin" firing mode (pg_trigger.tgenabled =
-- 'O'). A session that sets session_replication_role = 'replica' suppresses an
-- origin-mode trigger entirely. Proven live against this dev database before
-- this migration was written:
--
--     SET session_replication_role = 'replica';
--     UPDATE audit_event SET actor = '...';   -- UPDATE 1, history rewritten
--     TRUNCATE audit_event_default;           -- partition erased, 0 rows left
--
-- Neither statement raised an error. The product promise is a tamper-evident
-- audit chain; in origin mode it was not one.
--
-- Separately, 20260729000001_rls_policies.sql grants SELECT/INSERT/UPDATE/DELETE
-- on every table to oneops_app (both directly, ON ALL TABLES, and going forward,
-- ALTER DEFAULT PRIVILEGES), so the request-path role arrived holding UPDATE and
-- DELETE on an append-only table. Origin-mode triggers still fired for it during
-- ordinary traffic, so this was not itself a reachable rewrite -- but privilege
-- is the second layer 20260723000001's header promised ("defense-in-depth
-- alongside privilege revocation in the bootstrap") and never delivered, because
-- no such bootstrap exists in infra/, deploy/ or scripts/. It is delivered here.
--
-- WHO THIS DEFENDS AGAINST, STATED HONESTLY. session_replication_role is a
-- superuser-context parameter. The REQUEST-PATH role cannot set it -- measured:
-- oneops_app gets "permission denied to set parameter". So the replica-role
-- bypass was reachable by the PRIVILEGED pool's role (the table owner, and in
-- the dev stack the cluster superuser), by operators, and by DBA/restore
-- sessions -- NOT by a tenant request. This hardening is defence-in-depth
-- against the owning pool and insiders. It must NOT be described as closing a
-- tenant-reachable hole; the tenant path was already refused by the origin-mode
-- trigger and by FORCE row-level security.
--
-- WHY BOTH LAYERS. ADR-AUDIT-008 §5. A trigger is droppable/disable-able by one
-- operator ALTER; a privilege can be re-granted. Neither alone is sufficient,
-- and each is independently checked: the ENABLE ALWAYS arming is verified at
-- boot and every sentinel interval by SchemaValidator.validateAuditImmutability
-- (audit_event's alwaysRequired flag flips to true with this migration).

-- ---------------------------------------------------------------------------
-- Layer 1: arm the guards regardless of replication role (ENABLE ALWAYS).
--
-- audit_event is LIST-partitioned. The two trigger kinds propagate differently
-- and this was verified against PostgreSQL 16 before writing:
--
--   * trg_audit_event_no_row_mutate is FOR EACH ROW. It is cloned onto every
--     partition, and ALTER TABLE <parent> ENABLE ALWAYS TRIGGER recurses to the
--     clones -- parent and partition both become 'A'.
--
--   * trg_audit_event_no_truncate is FOR EACH STATEMENT. PostgreSQL does not
--     propagate a statement-level TRUNCATE trigger from a partitioned parent
--     (this is exactly why 20260728000002 had to attach it partition by
--     partition). ALTER TABLE <parent> ENABLE ALWAYS leaves the partition's
--     separate truncate trigger at 'O' -- measured: parent 'A', partition 'O'.
--     So each partition MUST be armed individually.
--
-- The parent is armed first, then every partition is armed for BOTH triggers by
-- catalogue lookup. Arming a row-mutate clone that the parent recursion already
-- set to 'A' is a harmless no-op; doing it unconditionally means this stays
-- correct if the recursion behaviour ever changes, and covers the truncate
-- trigger which does not recurse at all.
ALTER TABLE audit_event ENABLE ALWAYS TRIGGER trg_audit_event_no_row_mutate;
ALTER TABLE audit_event ENABLE ALWAYS TRIGGER trg_audit_event_no_truncate;

DO $$
DECLARE
    part regclass;
BEGIN
    FOR part IN
        SELECT inhrelid::regclass
          FROM pg_inherits
         WHERE inhparent = 'audit_event'::regclass
    LOOP
        EXECUTE format(
            'ALTER TABLE %s ENABLE ALWAYS TRIGGER trg_audit_event_no_row_mutate', part);
        EXECUTE format(
            'ALTER TABLE %s ENABLE ALWAYS TRIGGER trg_audit_event_no_truncate', part);
    END LOOP;
END;
$$;

-- ---------------------------------------------------------------------------
-- Layer 2: an append-only table must hold no write privilege beyond INSERT.
--
-- SELECT and INSERT are RETAINED for oneops_app: the request path appends
-- constitutional events on the tenant-scoped pool (audit_store.AppendEvent), and
-- the verifier and ownership resolver read the chain. Neither the appender nor
-- any other code path issues UPDATE, DELETE or TRUNCATE on audit_event -- swept
-- before writing this: the chain-head lock (ADR-AUDIT-006) is taken on
-- audit_chain_head, a different table this migration does not touch. So removing
-- UPDATE/DELETE/TRUNCATE removes privilege the append flow never uses.
--
-- Revoked from PUBLIC and from oneops_app explicitly, on the parent AND every
-- partition: GRANT ... ON ALL TABLES in 20260729000001 reached audit_event_default
-- (it already existed), so a direct UPDATE/DELETE against the partition by name
-- would otherwise still hold privilege. TRUNCATE was never granted to oneops_app
-- (measured: has_table_privilege = false); revoking it is belt-and-suspenders and
-- keeps this migration's intent explicit and self-documenting.
--
-- A future partition added operationally (ECR-09) inherits oneops_app's default
-- privileges from 20260729000001's ALTER DEFAULT PRIVILEGES; the migration that
-- adds it MUST re-run these two revokes for the new partition and ENABLE ALWAYS
-- both guards on it, exactly as 20260728000002 documents for the truncate guard.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_event FROM PUBLIC;
DO $$
DECLARE
    part regclass;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON audit_event FROM oneops_app';
    END IF;
    FOR part IN
        SELECT inhrelid::regclass
          FROM pg_inherits
         WHERE inhparent = 'audit_event'::regclass
    LOOP
        EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %s FROM PUBLIC', part);
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
            EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %s FROM oneops_app', part);
        END IF;
    END LOOP;
END;
$$;
