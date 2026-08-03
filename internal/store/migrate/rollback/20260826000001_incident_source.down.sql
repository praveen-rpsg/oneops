-- Reverses 20260826000001_incident_source.
--
-- Dropping a column drops the CHECK constraints that depend on it alone; the
-- unique index is dropped explicitly for clarity even though the column drop
-- would take it with it too (mirrors 20260819000001's rollback style).
--
-- incident_event.ck_incident_event_kind is restored to its pre-E4.1 set —
-- this will refuse the rollback if any alert_note row still exists, the same
-- protective failure a CHECK constraint addition always risks; the forward
-- migration's caller is expected to have rolled back or migrated any such
-- rows first, exactly as any other narrowing rollback would require.

DROP INDEX IF EXISTS ux_incident_open_alert_per_asset;

ALTER TABLE incident DROP CONSTRAINT IF EXISTS ck_incident_alert_source_has_asset;
ALTER TABLE incident DROP CONSTRAINT IF EXISTS ck_incident_source;
ALTER TABLE incident DROP COLUMN IF EXISTS source;

ALTER TABLE incident_event DROP CONSTRAINT IF EXISTS ck_incident_event_kind;
ALTER TABLE incident_event ADD CONSTRAINT ck_incident_event_kind
    CHECK (kind IN ('created', 'status_transitioned', 'assigned'));
