-- OPS-S<collector> Collector check-target configuration (E2.2a) — the
-- agentless collector framework's first, simplest check type: a leader-gated
-- scheduler polls each enabled row here on its interval_seconds and writes
-- the result as telemetry samples tied to asset_id (domain.Sample, E2.1).
--
-- collector_check is TENANT-SCOPED, exactly as asset is (20260816000001):
-- tenant_id is denormalised onto the row and is the RLS key.
--
-- asset_id REFERENCES asset (asset_id) ON DELETE CASCADE, exactly as
-- telemetry_sample's does (20260821000001): a check naming a hard-deleted CI
-- has nothing left to check. As ADR-ASSET-001 §6 records for
-- asset_relationship and telemetry_sample, PostgreSQL's foreign-key trigger
-- runs with the constraint's own privileges and BYPASSES row-level security
-- on asset — this reference alone cannot stop a caller naming another
-- tenant's asset_id. CollectorCheckStore.Create re-verifies asset_id against
-- its own tenant-scoped connection before any row naming it is written (the
-- same defense CreateRelationship and WriteSamples already apply); this FK
-- is referential integrity, not the isolation control.
--
-- last_run_at/last_status are written ONLY by the scheduler (collector.
-- Scheduler via CollectorCheckStore's background counterpart, built over the
-- PRIVILEGED pool — mirrors NotificationStore/TelemetryRollupWorker), never
-- through the row_version-guarded Update path: the same reason asset.
-- last_seen (20260820000001) is excluded from AssetPatch — a background
-- confirmation is not a field an operator is patching, and bumping
-- row_version on every scheduler tick would make an operator's own PATCH,
-- built from a row_version it read moments ago, spuriously conflict against
-- a version the scheduler had already advanced.
CREATE TABLE IF NOT EXISTS collector_check (
    check_id         text        PRIMARY KEY,
    tenant_id        text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id         text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    type             text        NOT NULL,
    target           text        NOT NULL,
    interval_seconds integer     NOT NULL,
    enabled          boolean     NOT NULL DEFAULT true,
    metric_prefix    text        NOT NULL,
    last_run_at      timestamptz,
    last_status      text,
    row_version      bigint      NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_collector_check_type_length          CHECK (char_length(type) <= 100),
    CONSTRAINT ck_collector_check_target_length        CHECK (char_length(target) <= 500),
    CONSTRAINT ck_collector_check_metric_prefix_length CHECK (char_length(metric_prefix) <= 80),
    CONSTRAINT ck_collector_check_interval             CHECK (interval_seconds BETWEEN 30 AND 2592000),
    CONSTRAINT ck_collector_check_last_status          CHECK (last_status IS NULL OR last_status IN ('ok', 'down', 'unsupported'))
);

CREATE INDEX IF NOT EXISTS ix_collector_check_tenant ON collector_check (tenant_id);
CREATE INDEX IF NOT EXISTS ix_collector_check_asset  ON collector_check (asset_id);
-- The scheduler's due-scan (collector.Scheduler, over the PRIVILEGED pool)
-- filters WHERE enabled = true first, then compares last_run_at against
-- interval_seconds; this partial index confines the scan to enabled rows —
-- the same reason a disabled check should never cost the due-scan anything —
-- without indexing an interval_seconds-derived expression that would need
-- reconstructing here.
CREATE INDEX IF NOT EXISTS ix_collector_check_due ON collector_check (last_run_at) WHERE enabled = true;

COMMENT ON TABLE collector_check IS
    'A polled check target (E2.2a): the agentless collector framework''s config, tenant-scoped. Its result is written as telemetry_sample rows, never a reified Alert/Metric.';
COMMENT ON COLUMN collector_check.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id is (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN collector_check.asset_id IS
    'Re-verified against the writer''s own tenant-scoped connection before insert — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6, ADR-TELEMETRY-001).';
COMMENT ON COLUMN collector_check.last_run_at IS
    'Set only by the scheduler''s background write path, never by the CRUD Update path — see this table''s header comment.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring asset's policy verbatim).
ALTER TABLE collector_check ENABLE ROW LEVEL SECURITY;
ALTER TABLE collector_check FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON collector_check
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
