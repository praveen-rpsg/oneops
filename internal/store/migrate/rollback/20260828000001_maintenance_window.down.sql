-- Reverses 20260828000001_maintenance_window.

DROP POLICY IF EXISTS tenant_isolation ON maintenance_window;
ALTER TABLE IF EXISTS maintenance_window DISABLE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS ix_maintenance_window_active_lookup;
DROP INDEX IF EXISTS ix_maintenance_window_tenant_window;

DROP TABLE IF EXISTS maintenance_window;
