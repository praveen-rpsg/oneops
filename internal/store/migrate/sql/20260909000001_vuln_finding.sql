-- OPS-S<E8.3a> Vulnerability-finding stateful entity + scan ingestion
-- (E8.3a): the foundation of vulnerability management (E8.3). Unlike
-- security_observation/ioc, a VulnFinding is a legitimate STATEFUL reified
-- entity — the same category as incident/asset, NOT a false noun — because a
-- vulnerability PERSISTS on an asset until remediated, is deduped, and
-- carries a governed lifecycle (ADR-SOC-006). Prioritization by CI
-- criticality and remediation -> Incident linkage are E8.3b, a LATER,
-- SEPARATE story: this migration carries no ranking/priority column and no
-- FK to incident. Auto-close-on-absence (an open finding not named in a new
-- full-scope scan) is DEFERRED — it needs full-scan-scope semantics this
-- story does not have.
--
-- vuln_finding is TENANT-SCOPED, exactly as security_rule/ioc are: tenant_id
-- is denormalised onto the row and is the RLS key.
--
-- asset_id REFERENCES asset (asset_id) ON DELETE CASCADE, exactly as
-- security_rule's does: a finding naming a hard-deleted CI has nothing left
-- to track. As ADR-ASSET-001 §6 records for alert_rule/security_rule/ioc,
-- PostgreSQL's foreign-key trigger runs with the constraint's own privileges
-- and BYPASSES row-level security on asset — this reference alone cannot
-- stop a caller naming another tenant's asset_id. VulnFindingStore.Upsert
-- re-verifies asset_id against its own tenant-scoped connection before any
-- row naming it is written (the same defense SecurityObservationStore.
-- WriteObservations already applies).
--
-- UNIQUE (tenant_id, asset_id, vuln_id): one finding per asset per
-- vulnerability, the batch-ingest UPSERT's dedup key. Tenant-scoped by
-- construction (tenant_id is the leading column), so it needs no
-- serverGeneratedIdentifiers entry (it is route 1: it already contains
-- tenant_id) — only the surrogate vuln_finding_pkey (finding_id) needs that
-- justification, the same as security_rule.rule_id/ioc.ioc_id.
--
-- vuln_id is deliberately NOT constrained to look like a CVE — scanners
-- report GHSA/RHSA/vendor advisory codes and internal plugin ids too — the
-- database enforces only non-blankness and length; the charset rule
-- (domain.vulnFindingVulnIDPattern) is a domain-layer concern, the same split
-- security_rule.observation_type draws between the DB length check and the
-- Go-side snake_case pattern.
--
-- severity uses its OWN five-value CVSS-derived vocabulary
-- (none/low/medium/high/critical) — distinct from BOTH incident_severity's
-- (critical/high/medium/low, no "none") and security_observation's
-- (info/low/medium/high/critical, no "none") — see domain.VulnFindingSeverity's
-- doc comment for why conflating any of the three would be wrong.
CREATE TABLE IF NOT EXISTS vuln_finding (
    finding_id  text        PRIMARY KEY,
    tenant_id   text        NOT NULL DEFAULT 'system' REFERENCES tenant (tenant_id),
    asset_id    text        NOT NULL REFERENCES asset (asset_id) ON DELETE CASCADE,
    vuln_id     text        NOT NULL,
    title       text        NOT NULL,
    severity    text        NOT NULL,
    status      text        NOT NULL DEFAULT 'open',
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    scanner     text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_vuln_finding_vuln_id_blank    CHECK (btrim(vuln_id) <> ''),
    CONSTRAINT ck_vuln_finding_vuln_id_length   CHECK (char_length(vuln_id) <= 100),
    CONSTRAINT ck_vuln_finding_title_blank      CHECK (btrim(title) <> ''),
    CONSTRAINT ck_vuln_finding_title_length     CHECK (char_length(title) <= 300),
    CONSTRAINT ck_vuln_finding_severity         CHECK (severity IN ('none', 'low', 'medium', 'high', 'critical')),
    CONSTRAINT ck_vuln_finding_status           CHECK (status IN ('open', 'remediated', 'accepted_risk', 'false_positive')),
    CONSTRAINT ck_vuln_finding_scanner_blank    CHECK (btrim(scanner) <> ''),
    CONSTRAINT ck_vuln_finding_scanner_length   CHECK (char_length(scanner) <= 200),
    CONSTRAINT ck_vuln_finding_description_length CHECK (char_length(description) <= 2000),
    CONSTRAINT uq_vuln_finding_tenant_asset_vuln UNIQUE (tenant_id, asset_id, vuln_id)
);

CREATE INDEX IF NOT EXISTS ix_vuln_finding_asset         ON vuln_finding (asset_id);
CREATE INDEX IF NOT EXISTS ix_vuln_finding_tenant_status ON vuln_finding (tenant_id, status);

COMMENT ON TABLE vuln_finding IS
    'A deduplicated vulnerability-on-asset finding (E8.3a): a stateful reified entity, like incident/asset, tenant-scoped. Batch scan-ingest UPSERTs by (tenant_id, asset_id, vuln_id). Prioritization/remediation-to-incident (E8.3b) and auto-close-on-absence are OUT of scope for this migration.';
COMMENT ON COLUMN vuln_finding.tenant_id IS
    'The RLS key. NOT NULL and denormalised, the same reason asset.tenant_id/security_rule.tenant_id are (ADR-IDENTITY-002 §6).';
COMMENT ON COLUMN vuln_finding.asset_id IS
    'Re-verified against the writer''s own tenant-scoped connection before insert — the foreign key alone bypasses row-level security on asset (ADR-ASSET-001 §6).';
COMMENT ON COLUMN vuln_finding.vuln_id IS
    'The vulnerability identifier (often a CVE, but deliberately not CVE-format-only — scanners emit GHSA/RHSA/vendor codes too). Part of the tenant-scoped dedup key with asset_id.';
COMMENT ON COLUMN vuln_finding.severity IS
    'The CVSS-derived rating (NVD: none/low/medium/high/critical) — its own vocabulary, distinct from incident_severity and security_observation.severity (domain.VulnFindingSeverity).';
COMMENT ON COLUMN vuln_finding.status IS
    'The lifecycle: open -> {remediated, accepted_risk, false_positive}; remediated/accepted_risk -> open (automatically, on scan reappearance, via Upsert); false_positive -> open ONLY via a manual SetStatus re-triage, never via Upsert (domain.VulnFindingStatus).';
COMMENT ON COLUMN vuln_finding.first_seen IS
    'Set once, at INSERT. Store-managed — never touched by an UPDATE branch of the ingestion UPSERT.';
COMMENT ON COLUMN vuln_finding.last_seen IS
    'Refreshed to now() by every ingestion UPSERT that names this row, including the false_positive branch (which touches nothing else) — see VulnFindingRepository.Upsert''s doc comment.';
COMMENT ON CONSTRAINT uq_vuln_finding_tenant_asset_vuln ON vuln_finding IS
    'The batch-ingest UPSERT dedup key. Tenant-scoped by construction (tenant_id is the leading column): two tenants may independently hold a finding for the identical (asset_id, vuln_id) pair without a real collision, because asset_id itself is never shared across tenants in practice, but the constraint does not rely on that — it is scoped explicitly.';

-- ---------------------------------------------------------------------------
-- Row-level security (ADR-TENANCY-001, mirroring security_rule's/ioc's policy
-- verbatim).
ALTER TABLE vuln_finding ENABLE ROW LEVEL SECURITY;
ALTER TABLE vuln_finding FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON vuln_finding
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
