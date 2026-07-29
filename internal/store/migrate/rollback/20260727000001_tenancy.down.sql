-- Reverses 20260727000001_tenancy.sql.
--
-- Safe to run only while no table references tenant. The migration that adds
-- tenant_id columns must be rolled back first, or the DROP fails on the
-- dependent foreign keys — which is the intended protection.

DROP INDEX IF EXISTS uq_tenant_external;
DROP INDEX IF EXISTS uq_tenant_slug;
DROP TABLE IF EXISTS tenant;
