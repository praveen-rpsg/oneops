-- OPS-S<E8.4a> Risk register: a tenant-owned, operator-authored stateful
-- entity (E8.4a) — the same category as incident/vuln_finding/asset, NOT a
-- false noun — plus the classic likelihood x impact risk matrix. Compliance
-- controls/evidence (E8.4b) and continuous-audit automation are OUT of
-- scope for this migration; both are separate, later stories (ADR-SOC-008).
--
-- risk is TENANT-SCOPED, exactly as incident/vuln_finding are: tenant_id is
-- denormalised onto the row and is the RLS key.
--
-- Unlike vuln_finding, a risk carries NO natural dedup key: it is
-- operator-authored, not scan-deduped, so there is no UNIQUE(tenant_id, ...)
-- beyond the surrogate primary key — the risk_pkey (risk_id) is the only
-- constraint that needs a serverGeneratedIdentifiers/keyScopeJustifications
-- entry, the same as vuln_finding_pkey/incident_pkey.
--
-- asset_id REFERENCES asset (asset_id) ON DELETE SET NULL, mirroring
-- incident.asset_id exactly (20260825000001): a risk is itself an
-- operational record with standalone value, so a hard-deleted CI clears the
-- link, it does not delete the risk. As ADR-ASSET-001 §6 records for
-- incident/security_rule/vuln_finding, PostgreSQL's foreign-key trigger runs
-- with the constraint's own privileges and BYPASSES row-level security on
-- asset — this reference alone cannot stop a caller naming another tenant's
-- asset_id. RiskStore.Create/Update re-verifies asset_id against its own
-- tenant-scoped connection before any row naming it is written.
--
-- likelihood/impact each use their OWN five-value closed vocabularies
-- (domain.RiskLikelihood / domain.RiskImpact) — independent of every other
-- severity-shaped scale in this schema (VulnFindingSeverity, AssetCriticality,
-- IncidentSeverity), for the reason domain.RiskLikelihood's doc comment
-- states. treatment is OPTIONAL (nullable) — a freshly registered risk may
-- carry no disposition decision yet.
--
-- category is deliberately NOT a closed enum — an organisation's own risk
-- taxonomy is not this layer's decision to make, mirroring asset.type/
-- asset.source's identical "open but bounded" treatment; the database
-- enforces only length, the lower-snake-case charset rule
-- (domain.riskCategoryPattern) is a domain-layer concern, the same split
-- vuln_finding.vuln_id draws between the DB length check and the Go-side
-- pattern.
CREATE TABLE IF NOT EXISTS risk (
    risk_id     text        PRIMARY KEY,
    tenant_id   text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    title       text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    category    text        NOT NULL DEFAULT '',
    likelihood  text        NOT NULL,
    impact      text        NOT NULL,
    status      text        NOT NULL DEFAULT 'open',
    treatment   text        NULL,
    asset_id    text        NULL REFERENCES asset (asset_id) ON DELETE SET NULL,
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_risk_title_length       CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT ck_risk_description_length CHECK (char_length(description) <= 10000),
    CONSTRAINT ck_risk_category_length    CHECK (char_length(category) <= 100),
    CONSTRAINT ck_risk_likelihood         CHECK (likelihood IN ('rare', 'unlikely', 'possible', 'likely', 'almost_certain')),
    CONSTRAINT ck_risk_impact             CHECK (impact IN ('negligible', 'minor', 'moderate', 'major', 'severe')),
    CONSTRAINT ck_risk_status             CHECK (status IN ('open', 'mitigating', 'accepted', 'closed')),
    CONSTRAINT ck_risk_treatment          CHECK (treatment IS NULL OR treatment IN ('mitigate', 'accept', 'transfer', 'avoid')),
    CONSTRAINT ck_risk_row_version        CHECK (row_version >= 1)
);

-- Index every access path (docs/PLATFORM-BUILD-PLAN.md §3). tenant_id leads
-- the keyset-pagination index because it is the RLS-filtered column every
-- List query already carries — mirrors ix_incident_tenant_incident's
-- reasoning. ix_risk_tenant_status also serves GET .../register's own
-- `status <> 'closed'` filter, the same partial-filter-over-a-plain-index
-- shape ix_vuln_finding_tenant_status serves for its own Prioritized query.
CREATE INDEX IF NOT EXISTS ix_risk_tenant_risk   ON risk (tenant_id, risk_id);
CREATE INDEX IF NOT EXISTS ix_risk_tenant_status ON risk (tenant_id, status);
CREATE INDEX IF NOT EXISTS ix_risk_asset         ON risk (asset_id) WHERE asset_id IS NOT NULL;

COMMENT ON TABLE risk IS
    'A tenant-owned, operator-authored risk-register entry (E8.4a): a stateful reified entity, like incident/vuln_finding, with a governed lifecycle and a computed (never stored) likelihood x impact score. Compliance controls (E8.4b) are OUT of scope for this table.';
COMMENT ON COLUMN risk.category IS
    'Open-but-bounded operator label (e.g. operational, compliance, security) — NOT a closed enum. Blank means uncategorised. Charset rule (domain.riskCategoryPattern) is a domain-layer concern.';
COMMENT ON COLUMN risk.likelihood IS
    'The RiskLikelihood scale (rare..almost_certain) — its own vocabulary, independent of severity/criticality scales elsewhere in this schema (domain.RiskLikelihood).';
COMMENT ON COLUMN risk.impact IS
    'The RiskImpact scale (negligible..severe) — its own vocabulary (domain.RiskImpact).';
COMMENT ON COLUMN risk.status IS
    'The lifecycle: open -> {mitigating, accepted, closed}; mitigating -> {accepted, closed, open}; accepted -> {open, closed}; closed -> open (reopened) (domain.RiskStatus).';
COMMENT ON COLUMN risk.treatment IS
    'The operator''s OPTIONAL disposition decision (mitigate/accept/transfer/avoid), or NULL when not yet decided (domain.RiskTreatment).';
COMMENT ON COLUMN risk.asset_id IS
    'The Configuration Item this risk concerns, or NULL when not tied to one. Re-verified against the writer''s own tenant-scoped connection before write — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6). ON DELETE SET NULL: a hard-deleted CI clears the link, it does not delete the risk.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring incident's/vuln_finding's
-- policy verbatim).
ALTER TABLE risk ENABLE ROW LEVEL SECURITY;
ALTER TABLE risk FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON risk
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
