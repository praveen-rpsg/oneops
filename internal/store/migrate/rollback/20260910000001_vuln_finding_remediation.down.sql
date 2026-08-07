-- Reverses 20260910000001_vuln_finding_remediation.
--
-- Must run BEFORE 20260825000001_incident's own rollback (the same ordering
-- protection 20260826000002_alert_rule_current_incident.down.sql documents
-- for its own FK into incident): vuln_finding.remediation_incident_id
-- references incident, so PostgreSQL refuses to drop incident while this FK
-- still exists.
--
-- incident.ck_incident_source is restored to its pre-E8.3b set — this
-- refuses the rollback if any vuln-sourced incident row still exists, the
-- same protective failure a CHECK constraint narrowing always risks
-- (mirrors 20260907000001_security_rule_detector.down.sql's identical
-- caveat). The forward migration's caller is expected to have rolled back
-- or migrated any such rows first.

ALTER TABLE incident DROP CONSTRAINT IF EXISTS ck_incident_vuln_source_has_asset;

ALTER TABLE incident DROP CONSTRAINT IF EXISTS ck_incident_source;
ALTER TABLE incident ADD CONSTRAINT ck_incident_source CHECK (source IN ('manual', 'alert', 'security'));

DROP INDEX IF EXISTS ix_vuln_finding_remediation_incident;
ALTER TABLE vuln_finding DROP CONSTRAINT IF EXISTS fk_vuln_finding_remediation_incident;
ALTER TABLE vuln_finding DROP COLUMN IF EXISTS remediation_incident_id;
