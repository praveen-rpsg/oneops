-- OPS-CMDB-005 (E1.5) CMDB health: a freshness field plus the two access
-- paths its read-only health report needs. Extends ADR-ASSET-001; asset
-- remains operational data, not a governance artifact.
--
-- One ADD COLUMN per statement (see 20260817000001's identical rule): the
-- derived-schema extractor (internal/kg/extract/schema) captures only the
-- first ADD of a multi-clause ALTER.

-- last_seen tracks when this CI was last CONFIRMED BY A SOURCE — set by
-- AssetStore.Create, AssetStore.Update (a manual write is itself a
-- confirmation) and AssetStore.Upsert (E1.4's import path, including a
-- no-op re-import: see AssetRepository.Upsert's doc comment). No DEFAULT: a
-- default of now() would make every asset that predates this migration look
-- freshly confirmed the moment this migration ran, which is false — it has
-- not been confirmed by anything, ever. NULL is therefore the honest value
-- for a pre-existing row until the next Create/Update/Upsert touches it, and
-- the health report treats NULL identically to "confirmed a very long time
-- ago" (GET /admin/assets/health, AssetStore.staleAssets).
ALTER TABLE asset ADD COLUMN IF NOT EXISTS last_seen timestamptz NULL;

-- Access path: the Stale health category — "active/maintenance CIs whose
-- last_seen is older than stale_after, or NULL" — filters on (status,
-- last_seen) within one tenant. tenant_id leads for the same reason every
-- other asset index does; status narrows to two of four values before
-- last_seen's range/NULL check runs.
CREATE INDEX IF NOT EXISTS ix_asset_tenant_status_last_seen
    ON asset (tenant_id, status, last_seen);

-- Access path: the Incomplete health category — "unowned, or criticality/
-- environment left at unknown, excluding retired" — is a fixed predicate
-- (unlike Stale's caller-chosen threshold), so a PARTIAL index matching it
-- exactly serves both the COUNT and the bounded sample directly, the same
-- technique ix_asset_tenant_status_not_retired (20260818000001) already
-- uses for its own fixed predicate.
CREATE INDEX IF NOT EXISTS ix_asset_tenant_incomplete
    ON asset (tenant_id, asset_id)
    WHERE status <> 'retired'
      AND ((owner_team_id IS NULL AND owner_user_id IS NULL)
           OR criticality = 'unknown'
           OR environment = 'unknown');

-- The Orphaned/OrphanedBusinessServices categories need no new index: both
-- are a NOT EXISTS against asset_relationship keyed on from_asset_id/
-- to_asset_id, which ix_asset_relationship_from/ix_asset_relationship_to
-- (20260816000001) already cover.

COMMENT ON COLUMN asset.last_seen IS
    'When this CI was last confirmed by a source: set by Create, Update and Upsert (including a no-op re-import). NULL means never confirmed since this column existed (E1.5) — treated as stale by GET /admin/assets/health, the same as a very old timestamp.';
