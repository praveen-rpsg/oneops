-- OPS-S<E8.1b-1> Security rule detection config (E8.1b-1) — a
-- threshold-over-window SIEM detection rule over security_observation
-- (E8.1a): it fires when the count of one asset's ObservationType
-- observations, at or above MinSeverity, within a trailing WindowSeconds,
-- meets ThresholdCount. A firing is DERIVED by a future, separate detector
-- (E8.1b-2) evaluating this config against security_observation; it is
-- never stored as a reified Alert/Detection/Correlation row
-- (docs/PLATFORM-BUILD-PLAN.md §4, ADR-SOC-001, ADR-SOC-002). This migration
-- is CONFIG ONLY: no detector, worker, or correlation exists yet. last_state/
-- current_incident_id are the only state this table carries beyond config,
-- and — exactly as alert_rule's did before its evaluator (20260824000001) —
-- nothing writes them in this increment; they exist so E8.1b-2 has a column
-- to write into without a second migration.
--
-- security_rule is TENANT-SCOPED, exactly as alert_rule is: tenant_id is
-- denormalised onto the row and is the RLS key.
--
-- asset_id REFERENCES asset (asset_id) ON DELETE CASCADE, exactly as
-- alert_rule's does: a rule naming a hard-deleted CI has nothing left to
-- watch. As ADR-ASSET-001 §6 records for alert_rule/telemetry_sample/
-- collector_check/security_observation, PostgreSQL's foreign-key trigger
-- runs with the constraint's own privileges and BYPASSES row-level security
-- on asset — this reference alone cannot stop a caller naming another
-- tenant's asset_id. SecurityRuleStore.Create re-verifies asset_id against
-- its own tenant-scoped connection before any row naming it is written (the
-- same defense AlertRuleStore.Create already applies).
--
-- last_state/current_incident_id are written ONLY by the future detector's
-- background counterpart (E8.1b-2, mirroring AlertRuleStore's dual-role
-- split), never through the row_version-guarded Update path: the same
-- reason alert_rule.last_state/current_incident_id are excluded from
-- AlertRulePatch.
CREATE TABLE IF NOT EXISTS security_rule (
    rule_id             text        PRIMARY KEY,
    tenant_id           text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id            text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    observation_type    text        NOT NULL,
    min_severity        text        NOT NULL DEFAULT 'info',
    window_seconds      integer     NOT NULL,
    threshold_count     integer     NOT NULL,
    incident_severity   text        NOT NULL,
    enabled             boolean     NOT NULL DEFAULT true,
    last_state          text        NOT NULL DEFAULT 'ok',
    current_incident_id text,
    row_version         bigint      NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_security_rule_observation_type_length CHECK (char_length(observation_type) <= 100),
    CONSTRAINT ck_security_rule_min_severity            CHECK (min_severity IN ('info', 'low', 'medium', 'high', 'critical')),
    CONSTRAINT ck_security_rule_window_seconds          CHECK (window_seconds BETWEEN 1 AND 86400),
    CONSTRAINT ck_security_rule_threshold_count         CHECK (threshold_count BETWEEN 1 AND 1000000),
    CONSTRAINT ck_security_rule_incident_severity       CHECK (incident_severity IN ('critical', 'high', 'medium', 'low')),
    CONSTRAINT ck_security_rule_last_state              CHECK (last_state IN ('ok', 'firing'))
);

CREATE INDEX IF NOT EXISTS ix_security_rule_tenant ON security_rule (tenant_id);
CREATE INDEX IF NOT EXISTS ix_security_rule_asset  ON security_rule (asset_id);

COMMENT ON TABLE security_rule IS
    'A threshold-over-window SIEM detection config over security_observation (E8.1b-1), tenant-scoped. A firing is derived by a future detector (E8.1b-2) evaluating it against security_observation, never a reified Alert/Detection/Correlation row. CONFIG ONLY in this migration: no detector exists yet.';
COMMENT ON COLUMN security_rule.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id is (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN security_rule.asset_id IS
    'Re-verified against the writer''s own tenant-scoped connection before insert — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6).';
COMMENT ON COLUMN security_rule.incident_severity IS
    'The severity of the Incident a future detector (E8.1b-2) will raise on a firing — the INCIDENT severity vocabulary (critical/high/medium/low), NOT the observation severity scale (info/low/medium/high/critical) min_severity uses.';
COMMENT ON COLUMN security_rule.last_state IS
    'Set only by the future detector''s background write path (E8.1b-2), never by the CRUD Update path — see this table''s header comment. Nothing writes it in this migration''s increment.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring alert_rule's policy
-- verbatim).
ALTER TABLE security_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE security_rule FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON security_rule
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
