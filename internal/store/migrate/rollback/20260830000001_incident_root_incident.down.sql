-- Reverses 20260830000001_incident_root_incident.

DROP INDEX IF EXISTS ix_incident_root_incident;
ALTER TABLE incident DROP CONSTRAINT IF EXISTS ck_incident_root_not_self;
ALTER TABLE incident DROP CONSTRAINT IF EXISTS fk_incident_root_incident;
ALTER TABLE incident DROP COLUMN IF EXISTS root_incident_id;
