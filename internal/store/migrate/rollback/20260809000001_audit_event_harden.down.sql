-- Reverses 20260809000001_audit_event_harden.sql.
-- Kept out of the Atlas migration directory (which holds forward files only).
--
-- Rolling this back REOPENS the session_replication_role bypass of the
-- constitutional audit chain (ADR-AUDIT-008) and re-grants write privilege on an
-- append-only table to the request-path role. It should only be run to unblock a
-- migration failure, never as routine operation. The boot validator will report
-- audit_event as DOWNGRADED once alwaysRequired is true in schema_validation.go,
-- so a binary built after this story will refuse to serve on a rolled-back
-- schema -- which is the intended safety behaviour, not a bug in this rollback.
--
-- Returns the guards to origin firing mode and restores the broad grant, on the
-- parent and every partition, mirroring the forward migration's loops.
ALTER TABLE audit_event ENABLE TRIGGER trg_audit_event_no_row_mutate;
ALTER TABLE audit_event ENABLE TRIGGER trg_audit_event_no_truncate;

DO $$
DECLARE
    part regclass;
BEGIN
    FOR part IN
        SELECT inhrelid::regclass
          FROM pg_inherits
         WHERE inhparent = 'audit_event'::regclass
    LOOP
        EXECUTE format('ALTER TABLE %s ENABLE TRIGGER trg_audit_event_no_row_mutate', part);
        EXECUTE format('ALTER TABLE %s ENABLE TRIGGER trg_audit_event_no_truncate', part);
    END LOOP;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'GRANT UPDATE, DELETE ON audit_event TO oneops_app';
        FOR part IN
            SELECT inhrelid::regclass
              FROM pg_inherits
             WHERE inhparent = 'audit_event'::regclass
        LOOP
            EXECUTE format('GRANT UPDATE, DELETE ON %s TO oneops_app', part);
        END LOOP;
    END IF;
END;
$$;
