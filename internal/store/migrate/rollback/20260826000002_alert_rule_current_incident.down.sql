-- Reverses 20260826000002_alert_rule_current_incident.
--
-- Must run BEFORE 20260826000001_incident_source's rollback (and before
-- 20260825000001_incident's own): alert_rule.current_incident_id references
-- incident, so PostgreSQL refuses to drop incident while this FK still
-- exists — the same ordering protection every other referencing table's
-- rollback in this tree documents (e.g. 20260825000001_incident.down.sql
-- must itself run before app_user's, per that table's own header comment).

DROP INDEX IF EXISTS ix_alert_rule_current_incident;
ALTER TABLE alert_rule DROP CONSTRAINT IF EXISTS fk_alert_rule_current_incident;
ALTER TABLE alert_rule DROP COLUMN IF EXISTS current_incident_id;
