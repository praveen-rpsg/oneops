-- Operational rollback for 20260904000001_incident_created_at_index.
-- Kept out of the Atlas migration directory (which holds forward files only).
--
-- Safe and unguarded: an index carries no data. Dropping it degrades the
-- incident-trends opened-series query to a tenant-scoped scan without a
-- created_at range predicate, not to incorrect answers.
DROP INDEX IF EXISTS ix_incident_tenant_created_at;
