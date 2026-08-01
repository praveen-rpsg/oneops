-- Reverses 20260817000001_asset_service_mapping.
--
-- Dropping a column drops the CHECK/FOREIGN KEY constraint and index that
-- depend on it alone; the composite (tenant_id, environment|criticality)
-- indexes are dropped explicitly for clarity even though the column drop
-- would take them with it (mirrors 20260815000001's rollback style).

DROP INDEX IF EXISTS ix_asset_owner_user;
DROP INDEX IF EXISTS ix_asset_owner_team;
DROP INDEX IF EXISTS ix_asset_tenant_criticality;
DROP INDEX IF EXISTS ix_asset_tenant_environment;

ALTER TABLE asset DROP COLUMN IF EXISTS owner_user_id;
ALTER TABLE asset DROP COLUMN IF EXISTS owner_team_id;
ALTER TABLE asset DROP COLUMN IF EXISTS criticality;
ALTER TABLE asset DROP COLUMN IF EXISTS environment;
