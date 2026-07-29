-- Close a bypass in the append-only audit guarantee.
--
-- Migration 20260723000001 installs two triggers on audit_event: a row-level
-- guard against UPDATE and DELETE, and a statement-level guard against
-- TRUNCATE. PostgreSQL propagates row-level triggers from a partitioned parent
-- to its partitions, but statement-level TRUNCATE triggers are NOT inherited.
--
-- audit_event is LIST-partitioned, so the guarantee had a hole:
--
--     TRUNCATE audit_event            -> refused by trg_audit_event_no_truncate
--     TRUNCATE audit_event_default    -> SUCCEEDED, erasing audit history
--
-- Verified directly against PostgreSQL 16 before this migration was written.
-- State Model §8 forbids deletion of audit history, and the row-level triggers
-- correctly refuse DELETE, so the only route to erasure was truncating a
-- partition by name. That is now closed.
--
-- The guard is attached by catalogue lookup rather than by partition name, so
-- this stays correct if the scheme grows beyond the current DEFAULT partition.
--
-- A DDL event trigger was considered for automatically guarding partitions
-- created later, and rejected. Event triggers are database-wide while the
-- function they call is schema-scoped, so a database hosting more than one
-- schema — which this one does, since the integration suites isolate
-- themselves in their own — would break in two ways: the trigger fires for
-- CREATE TABLE in schemas that have no audit_event, and dropping the schema
-- holding the function leaves a database-wide trigger pointing at nothing,
-- failing every subsequent DDL statement. Both were observed. Partitions are
-- added deliberately and rarely (ECR-09), so the guard belongs in the
-- migration that adds them; docs/runbooks/audit-integrity.md carries the
-- requirement, and TestAuditPartitionsCannotBeTruncatedDirectly fails the
-- build if any partition is left unguarded.

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
            'CREATE OR REPLACE TRIGGER trg_audit_event_no_truncate '
            'BEFORE TRUNCATE ON %s '
            'FOR EACH STATEMENT EXECUTE FUNCTION audit_event_immutable()',
            part);
    END LOOP;
END;
$$;
