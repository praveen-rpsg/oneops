-- OPS-S<E8.1a> Security observation ingestion (E8.1a) — the SOC epic's (E8)
-- foundation: an append-only fact table for security-relevant observations
-- tied to Configuration Items.
--
-- STORAGE DECISION: a SIBLING TimescaleDB hypertable, not a reuse of
-- telemetry_sample (20260821000001), behind a pluggable ingestion interface
-- (domain.SecurityObservationRepository) — the identical D1 shape
-- ADR-TELEMETRY-001 chose for telemetry, applied to a table of its own.
-- telemetry_sample's value column is a numeric measurement (`value double
-- precision`); a security observation is a categorical/attributed fact
-- (type, source, severity, plus open-ended attributes) that does not fit
-- that numeric shape, so it gets its own table rather than a NULL-able value
-- column bolted onto telemetry_sample. It is the same conceptual category as
-- telemetry_sample — a FACT, append-only — and deliberately NOT a reified
-- SecurityEvent/Alert/Finding entity: no status, lifecycle, or management API
-- exists here or is added by this migration. Detection/correlation over
-- these facts is a later, separate story (E8.1b) and touches no schema here.
--
-- security_observation is TENANT-SCOPED, exactly as telemetry_sample is:
-- tenant_id is denormalised onto the row and is the RLS key.
--
-- No surrogate id: the natural key (tenant_id, asset_id, observation_type,
-- source, observed_at) is this row's identity — the same choice
-- telemetry_sample makes (20260821000001) and for the same reason:
-- TimescaleDB requires the partitioning column (observed_at) to be part of
-- any unique constraint on a hypertable, and the UNIQUE constraint below both
-- is that key AND is the covering index range queries need, so no second
-- index is declared for it. As with telemetry_sample, this assumes at most
-- one observation of a given type from a given source on a given asset at a
-- given instant; a second write naming the same key is idempotent (ON
-- CONFLICT DO NOTHING in SecurityObservationStore.WriteObservations), not
-- merged — the same documented limitation
-- domain.TelemetryRepository.WriteSamples already carries.
--
-- asset_id REFERENCES asset (asset_id) ON DELETE CASCADE, exactly as
-- telemetry_sample's does: an observation about a hard-deleted CI has
-- nothing left to describe, so it goes with it. As ADR-ASSET-001 §6 and
-- ADR-TELEMETRY-001 record, PostgreSQL's foreign-key trigger runs with the
-- constraint's own privileges and BYPASSES row-level security on asset — so
-- this reference alone cannot stop a caller naming another tenant's
-- asset_id. SecurityObservationStore.WriteObservations re-verifies asset_id
-- against its own tenant-scoped connection before any row naming it is
-- written (the same defense TelemetryStore.WriteSamples already applies);
-- this FK is referential integrity, not the isolation control.
--
-- The timescaledb extension and its create_hypertable schema-qualification
-- were already established by 20260821000001; this migration only calls it,
-- schema-qualified for the identical reason (concurrent per-schema
-- integration test runs — see that migration's own comment).
CREATE TABLE IF NOT EXISTS security_observation (
    tenant_id        text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id         text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    observation_type text        NOT NULL,
    source           text        NOT NULL,
    severity         text        NOT NULL,
    observed_at      timestamptz NOT NULL,
    attributes       jsonb       NOT NULL DEFAULT '{}',
    CONSTRAINT ck_security_observation_type_length   CHECK (char_length(observation_type) <= 100),
    CONSTRAINT ck_security_observation_source_length CHECK (char_length(source) <= 200),
    CONSTRAINT ck_security_observation_severity      CHECK (severity IN ('info', 'low', 'medium', 'high', 'critical')),
    CONSTRAINT uq_security_observation UNIQUE (tenant_id, asset_id, observation_type, source, observed_at)
);

SELECT public.create_hypertable('security_observation'::regclass, 'observed_at'::name, if_not_exists => TRUE, migrate_data => TRUE);

COMMENT ON TABLE security_observation IS
    'A security-relevant fact tied to a Configuration Item (E8.1a) — a TimescaleDB hypertable behind domain.SecurityObservationRepository, a sibling of telemetry_sample rather than a reuse of it (ADR-TELEMETRY-001). Tenant-scoped; not a reified SecurityEvent/Alert/Finding entity.';
COMMENT ON COLUMN security_observation.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id is (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN security_observation.asset_id IS
    'Re-verified against the writer''s own tenant-scoped connection before insert — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6, ADR-TELEMETRY-001).';
COMMENT ON COLUMN security_observation.observation_type IS
    'Lower-case snake_case, e.g. port_scan, malware_detected, failed_login — the same discipline telemetry_sample.metric applies.';
COMMENT ON COLUMN security_observation.attributes IS
    'Open-ended, bounded key/value context (e.g. actor, source_ip) — the same role telemetry_sample.labels plays. This is where an "actor" or similar detail belongs; there is no dedicated column for it, to keep the fact shape minimal.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring 20260821000001's telemetry
-- policy verbatim). A hypertable is still a plain table to PostgreSQL's RLS
-- machinery — ENABLE/FORCE and the policy below apply to it unchanged.
ALTER TABLE security_observation ENABLE ROW LEVEL SECURITY;
ALTER TABLE security_observation FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON security_observation
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
