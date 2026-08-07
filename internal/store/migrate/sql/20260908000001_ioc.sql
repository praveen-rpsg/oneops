-- OPS-S<E8.2a> Indicator-of-compromise watchlist (E8.2a): a tenant-owned
-- REFERENCE DATA table an operator curates — the same legitimate-stored-data
-- category as alert_rule/security_rule/maintenance_window — never a reified
-- Threat/Event row. A future, separate detector (E8.2b) will match
-- security_observation facts against this watchlist and raise an Incident on
-- a hit; this migration is CONFIG ONLY: no detector, worker or correlation
-- exists yet, and this table carries no match state of its own (no
-- last_matched_at, no current_incident_id — unlike security_rule/alert_rule,
-- an IOC has no evaluator-owned firing state to stage a column for).
--
-- ioc is TENANT-SCOPED, exactly as security_rule/alert_rule are: tenant_id
-- is denormalised onto the row and is the RLS key.
--
-- UNIQUE (tenant_id, indicator_type, indicator_value): a tenant cannot hold
-- the same indicator twice, but two different tenants may each independently
-- hold the identical indicator value — tenant_id is IN the key, so this
-- constraint needs no serverGeneratedIdentifiers/keyScopeJustifications
-- entry (it is route 1: it already contains tenant_id). Only the surrogate
-- ioc_pkey (ioc_id) needs that justification, the same as security_rule's
-- rule_id.
--
-- indicator_value is normalized by the application before insert
-- (domain.NormalizeIOCIndicatorValue) — trimmed always, additionally
-- lower-cased for domain/url/email — so the uniqueness constraint compares
-- like with like; the database enforces length and non-blankness only, not
-- the per-type case rule, which is a domain-layer concern (see
-- domain.IOC.Validate's doc comment).
CREATE TABLE IF NOT EXISTS ioc (
    ioc_id          text        PRIMARY KEY,
    tenant_id       text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    indicator_type  text        NOT NULL,
    indicator_value text        NOT NULL,
    severity        text        NOT NULL,
    enabled         boolean     NOT NULL DEFAULT true,
    description     text        NOT NULL DEFAULT '',
    source          text        NOT NULL DEFAULT '',
    row_version     bigint      NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_ioc_indicator_type          CHECK (indicator_type IN ('ip', 'domain', 'url', 'file_hash', 'email')),
    CONSTRAINT ck_ioc_indicator_value_blank   CHECK (btrim(indicator_value) <> ''),
    CONSTRAINT ck_ioc_indicator_value_length  CHECK (char_length(indicator_value) <= 2048),
    CONSTRAINT ck_ioc_severity                CHECK (severity IN ('critical', 'high', 'medium', 'low')),
    CONSTRAINT ck_ioc_description_length      CHECK (char_length(description) <= 2000),
    CONSTRAINT ck_ioc_source_length           CHECK (char_length(source) <= 200),
    CONSTRAINT uq_ioc_tenant_indicator        UNIQUE (tenant_id, indicator_type, indicator_value)
);

COMMENT ON TABLE ioc IS
    'A tenant-curated indicator-of-compromise watchlist (E8.2a), tenant-scoped. Reference data an operator maintains, never a reified Threat/Event row. A future detector (E8.2b) will match security_observation against it; CONFIG ONLY in this migration.';
COMMENT ON COLUMN ioc.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id/security_rule.tenant_id are (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN ioc.indicator_value IS
    'Normalized by the application before insert (domain.NormalizeIOCIndicatorValue): always trimmed, additionally lower-cased for domain/url/email so the uniqueness constraint compares like with like.';
COMMENT ON COLUMN ioc.severity IS
    'The severity of the Incident a future detector (E8.2b) will raise on a match — the INCIDENT severity vocabulary (critical/high/medium/low), NOT security_observation''s five-level observation-severity scale.';
COMMENT ON CONSTRAINT uq_ioc_tenant_indicator ON ioc IS
    'Tenant-scoped by construction (tenant_id is the leading column): a tenant cannot hold the same indicator twice, but two different tenants may each independently hold the identical indicator value.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring security_rule's/alert_rule's
-- policy verbatim).
ALTER TABLE ioc ENABLE ROW LEVEL SECURITY;
ALTER TABLE ioc FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON ioc
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
