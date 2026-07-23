-- Operational rollback for 20260723000001_audit.
-- Kept out of the Atlas migration directory (which holds forward files only).
-- Guarded (ECR-08): refuses to drop a non-empty, immutable audit history.
DO $$
BEGIN
    IF to_regclass('public.audit_event') IS NOT NULL
       AND EXISTS (SELECT 1 FROM audit_event LIMIT 1) THEN
        RAISE EXCEPTION 'refusing to drop non-empty audit_event (immutable audit history)';
    END IF;
END;
$$;

-- Safe to drop only when empty. Dropping the partitioned table removes its
-- partitions (incl. audit_event_default) and its triggers.
DROP TABLE IF EXISTS audit_event;
DROP TABLE IF EXISTS audit_chain_head;
DROP FUNCTION IF EXISTS audit_event_immutable();
