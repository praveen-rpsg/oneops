-- Operational rollback for 20260805000001_admin_audit.
-- Kept out of the Atlas migration directory (which holds forward files only).
--
-- Guarded, as 20260723000001_audit.down.sql is: refuses to drop a non-empty,
-- immutable administrative audit history. ADR-AUDIT-007 §6.6 makes this store
-- never-pruned; a rollback that silently erased administrative accountability
-- would be a deletion path in all but name, and §6.5/§6.6 admit none.
--
-- Ordering: admin_audit_event first, then its chain head, then the trigger
-- function -- the function cannot be dropped while a trigger still references
-- it, and dropping the table removes its triggers.
DO $$
BEGIN
    IF to_regclass(current_schema() || '.admin_audit_event') IS NOT NULL
       AND EXISTS (SELECT 1 FROM admin_audit_event LIMIT 1) THEN
        RAISE EXCEPTION
            'refusing to drop non-empty admin_audit_event (immutable administrative audit history)';
    END IF;
END;
$$;

DROP TABLE IF EXISTS admin_audit_event;
DROP TABLE IF EXISTS admin_audit_chain_head;
DROP FUNCTION IF EXISTS admin_audit_event_immutable();
