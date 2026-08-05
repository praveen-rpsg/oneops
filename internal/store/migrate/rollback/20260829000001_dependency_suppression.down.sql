-- Reverses 20260829000001_dependency_suppression.

DROP POLICY IF EXISTS tenant_isolation ON dependency_suppression;
ALTER TABLE IF EXISTS dependency_suppression DISABLE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS ix_dependency_suppression_tenant;

DROP TABLE IF EXISTS dependency_suppression;
