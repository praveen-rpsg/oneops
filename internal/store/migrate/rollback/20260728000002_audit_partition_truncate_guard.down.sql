-- Reverses 20260728000002_audit_partition_truncate_guard.sql.
--
-- Rolling this back reopens the partition-level TRUNCATE bypass and should only
-- be done to unblock a migration failure, never as routine operation.

DO $$
DECLARE
    part regclass;
BEGIN
    FOR part IN
        SELECT inhrelid::regclass
          FROM pg_inherits
         WHERE inhparent = 'audit_event'::regclass
    LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS trg_audit_event_no_truncate ON %s', part);
    END LOOP;
END;
$$;
