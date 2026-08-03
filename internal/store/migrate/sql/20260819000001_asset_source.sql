-- OPS-CMDB-004 (E1.4) Source identity on Asset: bulk import/export and
-- reconciliation from multiple CMDB sources (manual entry, discovery
-- scanners, cloud pollers, SNMP...). Extends ADR-ASSET-001; Asset remains
-- operational data, not a governance artifact.
--
-- One ADD COLUMN per statement (see 20260817000001's identical rule): the
-- derived-schema extractor (internal/kg/extract/schema) captures only the
-- first ADD of a multi-clause ALTER.

ALTER TABLE asset ADD COLUMN IF NOT EXISTS source        text NOT NULL DEFAULT 'manual';
ALTER TABLE asset ADD COLUMN IF NOT EXISTS external_ref  text NULL;

-- source is OPEN, like asset.type (not a closed enum): manual/discovery/aws/
-- snmp/... are the seeded examples, not the full set, so a new integration
-- source (E2's agentless collectors, a future connector) needs no migration
-- to be accepted. Only the shape is fixed here, mirroring ck_asset_type's
-- discipline (domain.assetTypePattern enforces the identical pattern
-- application-side, reused rather than duplicated for asset.source).
--
-- external_ref carries no format constraint: it is opaque, foreign data (a
-- cloud ARN, an SNMP OID instance, a discovery scanner's own id), not an
-- identifier this schema mints or filters on — only a length bound.
ALTER TABLE asset ADD CONSTRAINT ck_asset_source_length CHECK (char_length(source) <= 100);
ALTER TABLE asset ADD CONSTRAINT ck_asset_external_ref_length CHECK (external_ref IS NULL OR char_length(external_ref) <= 500);

-- A source system's own identifier for a CI is unique WITHIN that source and
-- WITHIN a tenant — "aws" and "snmp" may legitimately both use "i-0123" for
-- unrelated things, and two tenants' external inventories are unrelated by
-- definition — so tenant_id LEADS the key (docs/PLATFORM-BUILD-PLAN.md §3's
-- tenant-key-scope rule; also satisfies postgres.TestEveryTenantScopedUniqueKey_IsTenantScoped
-- automatically, since the index already contains tenant_id) and the
-- constraint reaches no wider than one tenant.
--
-- PARTIAL on external_ref IS NOT NULL, not merely relying on PostgreSQL's
-- own "NULLs are distinct" behaviour: a manually-entered CI (the default
-- source) usually has no natural key at all, and this predicate makes "no
-- external_ref, therefore always create" an explicit schema decision here
-- rather than an incidental consequence of how NULL happens to compare.
-- This IS the constraint that makes AssetStore.Upsert idempotent: a second
-- INSERT attempt for the same (tenant, source, external_ref) triple is
-- refused by the database, not merely by application logic that happened to
-- check first.
CREATE UNIQUE INDEX IF NOT EXISTS ux_asset_tenant_source_external_ref
    ON asset (tenant_id, source, external_ref) WHERE external_ref IS NOT NULL;

-- Access path: "find every CI sharing an external_ref, regardless of which
-- source reported it" — the cross-source half of the duplicate-detection
-- projection (GET /admin/assets/duplicates). The unique index above cannot
-- serve this query efficiently: its leading columns are (tenant_id, source),
-- so it cannot group across sources without a full scan.
CREATE INDEX IF NOT EXISTS ix_asset_tenant_external_ref
    ON asset (tenant_id, external_ref) WHERE external_ref IS NOT NULL;

-- Access path: the type/name half of duplicate detection groups on
-- normalized (type, name) — this functional index matches that predicate
-- directly rather than forcing a sequential scan with a computed lower/trim
-- on every row.
CREATE INDEX IF NOT EXISTS ix_asset_tenant_type_name_normalized
    ON asset (tenant_id, type, lower(trim(name)));

COMMENT ON COLUMN asset.source IS
    'The system of record that produced this CI: manual, discovery, aws, snmp, ... Open but validated (E1.4), mirrors asset.type.';
COMMENT ON COLUMN asset.external_ref IS
    'The natural key the source system uses for this CI, or NULL for a manually-entered asset with no external identity. Unique per (tenant_id, source) when present (E1.4) — the key AssetStore.Upsert imports against.';
