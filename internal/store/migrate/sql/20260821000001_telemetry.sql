-- OPS-S<telemetry> Telemetry ingestion (E2.1) — the first monitoring signal,
-- and the spine E3 alerting will derive from: metric time-series tied to
-- Configuration Items.
--
-- STORAGE DECISION (ADR-TELEMETRY-001, D1): Postgres + TimescaleDB
-- hypertables behind a pluggable ingestion interface (domain.
-- TelemetryRepository), not a dedicated TSDB and not plain partitioning. A
-- hypertable is a regular Postgres table under the hood — row-level security
-- applies to it exactly as it does to any other table in this schema, so
-- isolation stays the same fail-closed model every tenant-owned table
-- already uses, with one datastore/DR story, and the interface leaves room
-- to swap the backing store later without moving the contract.
--
-- CREATE EXTENSION requires a role that can install extensions (superuser, or
-- CREATE privilege on the database if the extension control file marks it
-- trusted — timescaledb does not). The docker-compose/local role (oneops,
-- the image's bootstrap superuser) and this migration's role in CI both have
-- it; a production deployment must grant it once, out of band, before this
-- migration runs for the first time (see the ADR's Enforcement section for
-- the exact requirement).
--
-- SCHEMA public is explicit, not incidental. timescaledb is a per-DATABASE
-- singleton (one pg_extension row, however many schemas run this migration),
-- so whichever caller installs it first fixes where its functions physically
-- live; every OTHER schema's unqualified `create_hypertable(...)` would then
-- fail to resolve unless it happens to have that same schema on its
-- search_path. Proven live: this repo's integration suite runs several
-- packages' migrations concurrently, each in its own private schema
-- (pgstore_itest, httpapi_itest, ...) with search_path narrowed to just that
-- schema — the second and later packages to install the extension got
-- "function create_hypertable(...) does not exist" even though the
-- extension itself was already present, because it had landed in the FIRST
-- package's schema. Pinning the schema, and schema-qualifying every call
-- below, makes the outcome independent of which caller runs first.
CREATE EXTENSION IF NOT EXISTS timescaledb SCHEMA public;

-- telemetry_sample is TENANT-SCOPED, exactly as asset is (20260816000001):
-- tenant_id is denormalised onto the row and is the RLS key.
--
-- No surrogate id: the natural key (tenant_id, asset_id, metric, ts) is the
-- primary key candidate a time-series row has, and TimescaleDB requires the
-- partitioning column (ts) to be part of any unique constraint on a
-- hypertable — a separately-minted id would need ts added to it anyway. The
-- UNIQUE constraint below both is that key AND is the covering index range
-- queries need (tenant_id, asset_id, metric, ts), so no second index is
-- declared for it.
--
-- asset_id REFERENCES asset (asset_id) ON DELETE CASCADE, exactly as
-- asset_relationship's endpoints do: telemetry for a hard-deleted CI has
-- nothing left to describe, so it goes with it. As ADR-ASSET-001 §6 records
-- for asset_relationship, PostgreSQL's foreign-key trigger runs with the
-- constraint's own privileges and BYPASSES row-level security on asset — so
-- this reference alone cannot stop a caller naming another tenant's
-- asset_id. TelemetryStore.WriteSamples re-verifies asset_id against its own
-- tenant-scoped connection before any row naming it is written (the same
-- defense CreateRelationship already applies); this FK is referential
-- integrity, not the isolation control.
CREATE TABLE IF NOT EXISTS telemetry_sample (
    tenant_id  text             NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id   text             NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    metric     text             NOT NULL,
    ts         timestamptz      NOT NULL,
    value      double precision NOT NULL,
    labels     jsonb            NOT NULL DEFAULT '{}',
    CONSTRAINT ck_telemetry_sample_metric_length CHECK (char_length(metric) <= 100),
    CONSTRAINT uq_telemetry_sample UNIQUE (tenant_id, asset_id, metric, ts)
);

-- create_hypertable partitions telemetry_sample by ts into per-interval
-- chunks. if_not_exists guards re-application the same way every CREATE
-- TABLE/INDEX statement in this migration set already does; migrate_data
-- covers the (never-expected-in-practice) case of re-running this against a
-- table that already holds rows. The explicit ::regclass/::name casts are
-- required: create_hypertable is overloaded (a `dimension_info`-based form
-- alongside this simple time-column form), and two untyped string literals
-- are otherwise ambiguous between the two — PostgreSQL reports
-- "function create_hypertable(unknown, unknown, ...) does not exist" without
-- them, on this exact call shape. Schema-qualified as public.create_hypertable
-- for the same reason the extension itself is pinned to SCHEMA public above.
SELECT public.create_hypertable('telemetry_sample'::regclass, 'ts'::name, if_not_exists => TRUE, migrate_data => TRUE);

COMMENT ON TABLE telemetry_sample IS
    'A metric observation tied to a Configuration Item (E2.1) — a TimescaleDB hypertable behind domain.TelemetryRepository (ADR-TELEMETRY-001). Tenant-scoped; not a reified Metric/Alert entity.';
COMMENT ON COLUMN telemetry_sample.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id is (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN telemetry_sample.asset_id IS
    'Re-verified against the writer''s own tenant-scoped connection before insert — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6, ADR-TELEMETRY-001).';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring 20260816000001's asset
-- policy). A hypertable is still a plain table to PostgreSQL's RLS machinery
-- — ENABLE/FORCE and the policy below apply to it unchanged.
ALTER TABLE telemetry_sample ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry_sample FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON telemetry_sample
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
