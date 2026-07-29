-- Reverses 20260804000002_organization_backfill.
--
-- Removes only the rows the backfill created, identified by the org_ + tenant_id
-- key it generates, and only where the organisation still matches its tenant
-- exactly as inserted. An organisation that has since been renamed, re-slugged
-- or suspended is no longer the row this migration wrote, and deleting it would
-- destroy an operator's change rather than reverse a migration.

DELETE FROM organization o
USING tenant t
WHERE o.tenant_id = t.tenant_id
  AND o.org_id    = 'org_' || t.tenant_id
  AND o.slug      = t.slug
  AND o.name      = t.name
  AND o.status    = 'active';
