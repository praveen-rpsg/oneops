-- OPS-S<telemetry-b> Telemetry retention + downsampling (E2.1b) — bounding
-- telemetry_sample's growth and enabling efficient wide-range queries.
--
-- STORAGE DECISION (ADR-TELEMETRY-002, D2/D3/D4): this migration does NOT
-- call TimescaleDB's add_retention_policy or create a continuous aggregate
-- (`CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)`). Both are
-- Timescale-LICENSED (community) features; this platform's pinned image
-- (timescale/timescaledb:2.19.3-pg16-oss, ADR-TELEMETRY-001) carries only the
-- Apache license — verified live: both calls fail with "functionality not
-- supported under the current 'apache' license". Retention is instead
-- performed by an application worker calling the Apache-licensed
-- `drop_chunks` directly (internal/store/postgres/telemetry_retention_worker.go);
-- downsampling is this migration's own hypertable
-- (telemetry_rollup_5m), populated incrementally by an application worker
-- (internal/store/postgres/telemetry_rollup_worker.go) rather than
-- TimescaleDB's scheduler. See ADR-TELEMETRY-002 for the full reasoning,
-- including why this is a STRONGER tenant-isolation position than a native
-- continuous aggregate would have given: telemetry_rollup_5m is a table this
-- platform owns outright, so it carries the identical
-- ENABLE/FORCE ROW LEVEL SECURITY + tenant_isolation policy every other
-- tenant-owned table does, rather than depending on RLS reaching a
-- Timescale-managed internal materialization object it was never proven to
-- reach.
--
-- telemetry_rollup_5m mirrors telemetry_sample's shape (20260821000001):
-- tenant-scoped, no surrogate id (the natural key IS the primary key
-- candidate), asset_id references asset the same way and for the same
-- re-verification reason (the FK bypasses RLS on asset; the write path
-- re-checks it — see TelemetryRollupWorker). `bucket` takes ts's role as the
-- hypertable's partitioning column: TimescaleDB requires it in the unique
-- constraint, and it is the covering index range queries need
-- (tenant_id, asset_id, metric, bucket), so no second index is declared.
CREATE TABLE IF NOT EXISTS telemetry_rollup_5m (
    tenant_id    text             NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id     text             NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    metric       text             NOT NULL,
    bucket       timestamptz      NOT NULL,
    avg_value    double precision NOT NULL,
    min_value    double precision NOT NULL,
    max_value    double precision NOT NULL,
    sample_count bigint           NOT NULL,
    CONSTRAINT ck_telemetry_rollup_5m_metric_length CHECK (char_length(metric) <= 100),
    CONSTRAINT uq_telemetry_rollup_5m UNIQUE (tenant_id, asset_id, metric, bucket)
);

-- Schema-qualified for the same reason 20260821000001 qualifies
-- create_hypertable: this repository's integration suite runs several
-- packages' migrations concurrently, each against its own private schema
-- with search_path narrowed to just that schema, and timescaledb is a
-- per-DATABASE singleton extension whose functions physically live wherever
-- CREATE EXTENSION ... SCHEMA public first put them.
SELECT public.create_hypertable('telemetry_rollup_5m'::regclass, 'bucket'::name, if_not_exists => TRUE, migrate_data => TRUE);

COMMENT ON TABLE telemetry_rollup_5m IS
    'A 5-minute avg/min/max/count rollup of telemetry_sample (E2.1b) — an application-populated TimescaleDB hypertable, not a Timescale continuous aggregate (ADR-TELEMETRY-002: that feature requires a license this platform''s image does not carry). Tenant-scoped, RLS-isolated identically to telemetry_sample.';
COMMENT ON COLUMN telemetry_rollup_5m.bucket IS
    'The bucket''s start (public.time_bucket(''5 minutes'', ts)), populated by TelemetryRollupWorker, not by a database trigger or Timescale job.';
COMMENT ON COLUMN telemetry_rollup_5m.asset_id IS
    'Re-verified against the writer''s own tenant before insert would apply to a request-path write; TelemetryRollupWorker instead derives tenant_id/asset_id FROM already-tenant-scoped telemetry_sample rows, so no additional check is needed here — the source rows were already validated on ingest (ADR-TELEMETRY-001, ADR-TELEMETRY-002).';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring telemetry_sample's policy
-- verbatim). This is the load-bearing tenant-isolation control for the
-- rollup: because it is a real, RLS-enabled table this platform owns, tenant
-- isolation on the rollup is IDENTICAL in mechanism to every other
-- tenant-owned table, not a special case reasoned about separately.
ALTER TABLE telemetry_rollup_5m ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry_rollup_5m FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON telemetry_rollup_5m
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
