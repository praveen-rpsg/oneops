-- Reverses 20260820000001_asset_health.
--
-- Dropping the column drops any dependent index too; the indexes are dropped
-- explicitly for clarity, mirroring 20260819000001's rollback style.

DROP INDEX IF EXISTS ix_asset_tenant_incomplete;
DROP INDEX IF EXISTS ix_asset_tenant_status_last_seen;

ALTER TABLE asset DROP COLUMN IF EXISTS last_seen;
