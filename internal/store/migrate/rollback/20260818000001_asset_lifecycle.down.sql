-- Reverses 20260818000001_asset_lifecycle.
--
-- asset_change_history carries no foreign key to asset (by design — see the
-- forward migration), so it has no ordering dependency on asset's own
-- rollback and may be dropped independently. Dropping the table drops its
-- row-level-security policy and both triggers with it.
DROP TABLE IF EXISTS asset_change_history;

DROP INDEX IF EXISTS ix_asset_tenant_status_not_retired;

-- Restore the two-value lifecycle ck_asset_status carried before this
-- migration. A database holding a 'planned' or 'maintenance' row at
-- rollback time would violate this constraint on the restored definition —
-- the same caveat every status-widening rollback in this tree carries, and
-- is why rollback is exercised against a freshly-seeded database, never a
-- live one with rows in the new states.
ALTER TABLE asset DROP CONSTRAINT ck_asset_status;
ALTER TABLE asset ADD CONSTRAINT ck_asset_status
    CHECK (status IN ('active', 'retired'));
