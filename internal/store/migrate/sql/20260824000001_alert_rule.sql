-- OPS-S<alertrule> Alert rule config + firing-state (E3.1) — a threshold on
-- one asset's one metric. A firing is DERIVED by the leader-gated evaluator
-- (internal/alerting) evaluating this config against telemetry_sample; it is
-- never stored as a reified Alert/Event/Signal row
-- (docs/PLATFORM-BUILD-PLAN.md §4, Vol II §5.3). last_state/last_transition_at
-- are the only state this table carries beyond config, and they record the
-- evaluator's last VERDICT, not a history of firings.
--
-- alert_rule is TENANT-SCOPED, exactly as collector_check is (20260823000001):
-- tenant_id is denormalised onto the row and is the RLS key.
--
-- asset_id REFERENCES asset (asset_id) ON DELETE CASCADE, exactly as
-- collector_check's does: a rule naming a hard-deleted CI has nothing left to
-- watch. As ADR-ASSET-001 §6 records for asset_relationship, telemetry_sample
-- and collector_check, PostgreSQL's foreign-key trigger runs with the
-- constraint's own privileges and BYPASSES row-level security on asset — this
-- reference alone cannot stop a caller naming another tenant's asset_id.
-- AlertRuleStore.Create re-verifies asset_id against its own tenant-scoped
-- connection before any row naming it is written (the same defense
-- CollectorCheckStore.Create and TelemetryStore.WriteSamples already apply).
--
-- last_state/last_transition_at are written ONLY by the evaluator
-- (internal/alerting.Evaluator via AlertRuleStore's background counterpart,
-- built over the PRIVILEGED pool — mirrors CollectorCheckStore/
-- TelemetryRollupWorker), never through the row_version-guarded Update path:
-- the same reason collector_check.last_run_at is excluded from
-- CollectorCheckPatch — a background verdict is not a field an operator is
-- patching, and bumping row_version on every evaluation tick would make an
-- operator's own PATCH, built from a row_version it read moments ago,
-- spuriously conflict against a version the evaluator had already advanced.
CREATE TABLE IF NOT EXISTS alert_rule (
    rule_id               text        PRIMARY KEY,
    tenant_id             text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id              text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    metric                text        NOT NULL,
    comparator            text        NOT NULL,
    threshold             double precision NOT NULL,
    for_duration_seconds  integer     NOT NULL,
    severity              text        NOT NULL,
    enabled               boolean     NOT NULL DEFAULT true,
    last_state            text        NOT NULL DEFAULT 'ok',
    last_transition_at    timestamptz,
    row_version           bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_alert_rule_metric_length   CHECK (char_length(metric) <= 100),
    CONSTRAINT ck_alert_rule_comparator      CHECK (comparator IN ('gt', 'lt', 'gte', 'lte')),
    -- double precision has no isfinite() in Postgres (unlike date/timestamp/
    -- numeric); NaN also compares NaN = NaN true here (Postgres's own total
    -- order for floats, not IEEE754), so equality cannot detect it either.
    -- Postgres instead sorts NaN as greater than every other float8 value
    -- including Infinity, so a strict double-bound is the one comparison
    -- that excludes NaN and both infinities at once (verified live).
    CONSTRAINT ck_alert_rule_threshold_finite CHECK (threshold > '-Infinity'::double precision AND threshold < 'Infinity'::double precision),
    CONSTRAINT ck_alert_rule_for_duration    CHECK (for_duration_seconds BETWEEN 1 AND 86400),
    CONSTRAINT ck_alert_rule_severity        CHECK (severity IN ('critical', 'warning', 'info')),
    CONSTRAINT ck_alert_rule_last_state      CHECK (last_state IN ('ok', 'firing'))
);

CREATE INDEX IF NOT EXISTS ix_alert_rule_tenant ON alert_rule (tenant_id);
CREATE INDEX IF NOT EXISTS ix_alert_rule_asset  ON alert_rule (asset_id);
-- The evaluator's per-tick scan (internal/alerting.Evaluator, over the
-- PRIVILEGED pool) filters WHERE enabled = true first, then keyset-paginates
-- by rule_id; this partial index confines the scan to enabled rows, the same
-- reason ix_collector_check_due is partial.
CREATE INDEX IF NOT EXISTS ix_alert_rule_enabled ON alert_rule (rule_id) WHERE enabled = true;

COMMENT ON TABLE alert_rule IS
    'A threshold config over one asset''s one metric (E3.1), tenant-scoped. A firing is derived by evaluating it against telemetry_sample, never a reified Alert/Event/Signal row.';
COMMENT ON COLUMN alert_rule.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id is (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN alert_rule.asset_id IS
    'Re-verified against the writer''s own tenant-scoped connection before insert — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6, ADR-TELEMETRY-001).';
COMMENT ON COLUMN alert_rule.last_state IS
    'Set only by the evaluator''s background write path, never by the CRUD Update path — see this table''s header comment.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring collector_check's policy
-- verbatim).
ALTER TABLE alert_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_rule FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON alert_rule
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
