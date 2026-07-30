-- Operational rollback for 20260807000001_admin_audit_query.
-- Kept out of the Atlas migration directory (which holds forward files only).
--
-- Safe and unguarded: this migration creates no table, grants no privilege and
-- destroys no history. Dropping the indexes leaves every row intact; the query
-- API would degrade to sequential scans, not to incorrect answers.
DROP INDEX IF EXISTS ix_admin_audit_page;
DROP INDEX IF EXISTS ix_admin_audit_subject_user;
DROP INDEX IF EXISTS ix_admin_audit_subject_tenant;
