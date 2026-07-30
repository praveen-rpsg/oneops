-- Operational rollback for 20260806000001_admin_audit_writer.
-- Kept out of the Atlas migration directory (which holds forward files only).
--
-- Safe and unguarded, unlike the store's own rollback: this migration creates
-- no table and destroys no history. Reversing it returns the administrative
-- audit store to the state OPS-S034 left it in -- present, immutable and
-- unwritable -- which means any binary still running the OPS-S035 chokepoint
-- will fail every administrative mutation with "permission denied".
--
-- That is the intended behaviour, not a defect: ADR-AUDIT-007 SS6.9 makes an
-- unauditable administrative act fail. Roll the binary back with it.
ALTER TABLE admin_audit_event DROP CONSTRAINT IF EXISTS ck_admin_audit_subject_bound;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'oneops_app') THEN
        EXECUTE 'REVOKE INSERT ON admin_audit_event FROM oneops_app';
        EXECUTE 'REVOKE SELECT, INSERT, UPDATE ON admin_audit_chain_head FROM oneops_app';
    END IF;
END;
$$;
