-- Reverses 20260907000001_security_rule_detector.
--
-- incident_event.ck_incident_event_kind and incident.ck_incident_source are
-- restored to their pre-E8.1b-2 sets — this refuses the rollback if any
-- security_note event row or security-sourced incident row still exists, the
-- same protective failure a CHECK constraint narrowing always risks
-- (mirrors 20260826000001_incident_source.down.sql's identical caveat). The
-- forward migration's caller is expected to have rolled back or migrated any
-- such rows first.

DROP INDEX IF EXISTS ux_incident_open_security_per_asset;

ALTER TABLE incident DROP CONSTRAINT IF EXISTS ck_incident_security_source_has_asset;

ALTER TABLE incident DROP CONSTRAINT IF EXISTS ck_incident_source;
ALTER TABLE incident ADD CONSTRAINT ck_incident_source CHECK (source IN ('manual', 'alert'));

ALTER TABLE incident_event DROP CONSTRAINT IF EXISTS ck_incident_event_kind;
ALTER TABLE incident_event ADD CONSTRAINT ck_incident_event_kind
    CHECK (kind IN ('created', 'status_transitioned', 'assigned', 'alert_note'));

ALTER TABLE security_rule DROP COLUMN IF EXISTS last_transition_at;
