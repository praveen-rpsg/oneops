-- OPS-CMDB-002 (E1.2) Business-service mapping: classification, environment
-- and ownership on the CMDB Asset — the context downstream NOC health
-- rollups, SOC criticality-based vulnerability prioritization, and ITSM
-- routing all key off (docs/PLATFORM-BUILD-PLAN.md E1.2). Extends
-- ADR-ASSET-001's model; Asset remains operational data, not a governance
-- artifact.
--
-- One ADD COLUMN per statement: the derived-schema extractor
-- (internal/kg/extract/schema) captures only the first ADD of a
-- multi-clause ALTER, which would leave the later columns invisible in the
-- knowledge graph. Every migration in this tree follows this rule.

ALTER TABLE asset ADD COLUMN IF NOT EXISTS environment   text NOT NULL DEFAULT 'unknown';
ALTER TABLE asset ADD COLUMN IF NOT EXISTS criticality   text NOT NULL DEFAULT 'unknown';
ALTER TABLE asset ADD COLUMN IF NOT EXISTS owner_team_id text NULL;
ALTER TABLE asset ADD COLUMN IF NOT EXISTS owner_user_id text NULL;

-- environment/criticality are CLOSED enums (unlike asset.type): they are the
-- vocabulary NOC health rollups and SOC vulnerability prioritization reason
-- over directly, so a downstream increment that needs a new tier is a
-- deliberate schema change here, not a value a caller happens to send
-- (mirrors asset_relationship.type's closedness, ADR-ASSET-001 §4).
ALTER TABLE asset ADD CONSTRAINT ck_asset_environment
    CHECK (environment IN ('production', 'staging', 'development', 'unknown'));
ALTER TABLE asset ADD CONSTRAINT ck_asset_criticality
    CHECK (criticality IN ('critical', 'high', 'medium', 'low', 'unknown'));

-- owner_team_id/owner_user_id name the team/user accountable for this asset.
-- Both REFERENCE a real table, but NEITHER foreign key is trusted alone for
-- tenant isolation — see AssetStore.Create/Update: PostgreSQL's FK-check
-- trigger runs with the constraint's own privileges and bypasses row-level
-- security on `team`, the exact documented behaviour ADR-ASSET-001 §6
-- already records for asset_relationship's endpoints, so the store
-- re-verifies owner_team_id on its own RLS-enforced connection before every
-- write. owner_user_id references app_user, which carries no tenant_id at
-- all (ADR-IDENTITY-002 §3.1); the store re-verifies it via an active
-- `membership` row instead, because app_user's own table cannot answer "is
-- this tenant's" at all. team is never hard-deleted (archived only) and
-- app_user is never hard-deleted (deactivated only), so neither FK needs an
-- ON DELETE clause: the referenced row always survives.
ALTER TABLE asset ADD CONSTRAINT fk_asset_owner_team
    FOREIGN KEY (owner_team_id) REFERENCES team (team_id);
ALTER TABLE asset ADD CONSTRAINT fk_asset_owner_user
    FOREIGN KEY (owner_user_id) REFERENCES app_user (user_id);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3): tenant_id leads
-- both composite indexes because it is the RLS-filtered column every query
-- already carries, and the owner indexes are partial because most assets
-- start unowned.
CREATE INDEX IF NOT EXISTS ix_asset_tenant_environment ON asset (tenant_id, environment);
CREATE INDEX IF NOT EXISTS ix_asset_tenant_criticality ON asset (tenant_id, criticality);
CREATE INDEX IF NOT EXISTS ix_asset_owner_team ON asset (owner_team_id) WHERE owner_team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_asset_owner_user ON asset (owner_user_id) WHERE owner_user_id IS NOT NULL;

COMMENT ON COLUMN asset.environment IS
    'Deployment environment: production/staging/development/unknown. Feeds NOC health rollups (E1.2).';
COMMENT ON COLUMN asset.criticality IS
    'Business-impact tier: critical/high/medium/low/unknown. Feeds SOC vulnerability prioritization and ITSM routing (E1.2).';
COMMENT ON COLUMN asset.owner_team_id IS
    'The team accountable for this asset. Re-verified tenant-side on write, not trusted to the foreign key alone (ADR-ASSET-001 §6).';
COMMENT ON COLUMN asset.owner_user_id IS
    'The individual accountable for this asset. Re-verified against an active membership on write, not trusted to the foreign key alone: app_user itself carries no tenant_id (ADR-ASSET-001 §6, ADR-IDENTITY-002 §3.1).';
