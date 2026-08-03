-- Reverses 20260819000001_asset_source.
--
-- Dropping a column drops the CHECK constraint that depends on it alone; the
-- indexes are dropped explicitly for clarity even though the column drop
-- would take them with it too (mirrors 20260817000001's rollback style).

DROP INDEX IF EXISTS ix_asset_tenant_type_name_normalized;
DROP INDEX IF EXISTS ix_asset_tenant_external_ref;
DROP INDEX IF EXISTS ux_asset_tenant_source_external_ref;

ALTER TABLE asset DROP COLUMN IF EXISTS external_ref;
ALTER TABLE asset DROP COLUMN IF EXISTS source;
