-- OPS-S<e9.1> (E9.1) incident-trends projection — the ordering/range key
-- GET /admin/dashboards/incident-trends' opened series bucket query needs.
--
-- Additive. No completed migration is altered, and no grant is issued:
-- incident is tenant-owned and already carries row-level security, so the
-- request-path role holds exactly what it needs and the policy confines it
-- (mirrors 20260808000001_membership_page's identical reasoning).
--
-- ix_incident_tenant_status (tenant_id, status) and ix_incident_tenant_incident
-- (tenant_id, incident_id) both exist (20260825000001) but neither leads with
-- created_at, so a `WHERE created_at >= $ AND created_at < $` range scan has
-- nothing to walk but tenant_id — this index gives it a real range predicate.
-- DESC matches the CTO-locked column order (tenant_id, created_at DESC): a
-- dashboard's own most common query is its most recent window, and a B-tree
-- scanned in either direction over the same index costs the same, so DESC
-- costs nothing while matching that access pattern's natural read order.
CREATE INDEX IF NOT EXISTS ix_incident_tenant_created_at
    ON incident (tenant_id, created_at DESC);

COMMENT ON INDEX ix_incident_tenant_created_at IS
    'Range-scan key for E9.1''s incident-trends opened series (created_at bucketing). RLS supplies tenant_id; created_at DESC serves the common recent-window query.';
