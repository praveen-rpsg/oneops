-- Reverses 20260825000001_incident.
--
-- incident_event carries a foreign key to incident (by design — see the
-- forward migration's comment on why this table differs from
-- asset_change_history), so it must be dropped before incident itself.
DROP TABLE IF EXISTS incident_event;
DROP TABLE IF EXISTS incident;
