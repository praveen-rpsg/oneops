-- OPS-S<E8.3b> vuln_finding gains remediation_incident_id (E8.3b,
-- ADR-SOC-007): the LINK between a vulnerability finding and the tracked
-- remediation Incident an operator has opened for it — a column, not a
-- reified "Remediation" noun, mirroring alert_rule.current_incident_id's
-- exact discipline (20260826000002). This migration carries no
-- "priority"/"score" column at all: prioritization (E8.3b's other half) is a
-- computed-at-request-time projection (VulnFindingStore.Prioritized), never
-- a stored field.
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (see 20260819000001's rule):
-- the derived-schema extractor (internal/kg/extract/schema) captures only
-- the first ADD of a multi-clause ALTER.
--
-- NULL means "no remediation Incident currently linked" — the same meaning
-- alert_rule.current_incident_id's NULL carries. Set ONLY by
-- VulnFindingStore.Remediate, atomically with the incident row it names, in
-- the SAME transaction — never through the row_version-guarded PATCH path
-- (domain.PatchVulnFindingRequest carries no such field).
--
-- REFERENCES incident (incident_id) with no ON DELETE clause: Incident has
-- NO Delete method at all (E5.1 decision), so the referenced row is never
-- gone — the same reasoning current_incident_id's own FK already gives.
ALTER TABLE vuln_finding ADD COLUMN IF NOT EXISTS remediation_incident_id text NULL;
ALTER TABLE vuln_finding ADD CONSTRAINT fk_vuln_finding_remediation_incident
    FOREIGN KEY (remediation_incident_id) REFERENCES incident (incident_id);

CREATE INDEX IF NOT EXISTS ix_vuln_finding_remediation_incident
    ON vuln_finding (remediation_incident_id) WHERE remediation_incident_id IS NOT NULL;

COMMENT ON COLUMN vuln_finding.remediation_incident_id IS
    'The open, vuln-sourced Incident this finding''s remediation is currently tracked by, or NULL when none is linked (E8.3b). Set/cleared only by VulnFindingStore.Remediate, atomically with the incident row it names.';

-- incident.source widens from ('manual', 'alert', 'security') to also accept
-- 'vuln' (VulnFindingStore.Remediate's own provenance, E8.3b). This is a
-- WIDENING of a vocabulary shared by every incident, not any existing
-- source's own constraint: every existing 'manual'/'alert'/'security' row,
-- and every existing query/constraint naming those three values, is
-- unaffected. CHECK constraints cannot be altered in place; drop and
-- recreate widens the same constraint rather than adding a second one —
-- the identical technique 20260826000001/20260907000001 already used twice.
ALTER TABLE incident DROP CONSTRAINT ck_incident_source;
ALTER TABLE incident ADD CONSTRAINT ck_incident_source CHECK (source IN ('manual', 'alert', 'security', 'vuln'));

COMMENT ON COLUMN incident.source IS
    'manual (operator-filed), alert (E4.1 correlation-created/linked), security (E8.1b-2 detector-created/linked) or vuln (E8.3b VulnFindingStore.Remediate-opened). Closed vocabulary. Written once at Create, never changed.';

-- A vuln-sourced incident always names the CI the finding it remediates
-- watches (domain.NewVulnRemediationIncident requires assetID) — a SEPARATE
-- constraint from ck_incident_alert_source_has_asset/
-- ck_incident_security_source_has_asset, both left completely untouched.
ALTER TABLE incident ADD CONSTRAINT ck_incident_vuln_source_has_asset
    CHECK (source <> 'vuln' OR asset_id IS NOT NULL);

-- Unlike the alert/security paths (20260826000001/20260907000001), the vuln
-- path gets NO partial unique index analogous to
-- ux_incident_open_alert_per_asset/ux_incident_open_security_per_asset. Those
-- exist because their natural dedup key is (tenant_id, asset_id, source): any
-- rule watching the same CI must land on the same open incident.
-- VulnFindingRepository.Remediate's dedup key is different — one finding's
-- OWN remediation_incident_id link — so two DIFFERENT findings on the SAME
-- asset are expected to open two SEPARATE vuln-sourced incidents, which a
-- (tenant_id, asset_id) unique index would wrongly forbid. Remediate's
-- no-duplicate-per-FINDING guarantee is instead enforced by holding a
-- `SELECT ... FOR UPDATE` lock on the vuln_finding row for the whole decision
-- (read -> reuse-or-create -> write), which serializes concurrent callers on
-- the SAME finding without needing a second index (see
-- VulnFindingRepository.Remediate's own doc comment, ADR-SOC-007).
