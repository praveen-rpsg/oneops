-- OPS-S<e4.1> (E4.1) alert_rule.current_incident_id: the correlation LINK
-- between a rule's firing and the Incident it is currently associated with —
-- a column, not a reified Alert/Event/Signal row (docs/PLATFORM-BUILD-PLAN.md
-- §4, Vol II §5.3, mirrors alert_rule's own header comment on last_state).
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (see 20260819000001's rule):
-- the derived-schema extractor captures only the first ADD of a multi-clause
-- ALTER.
--
-- NULL means "ok, nothing currently linked" — the same meaning
-- last_transition_at's NULL default carries ("never evaluated"/"not
-- currently firing"), set/cleared ONLY by the evaluator's background write
-- path (alerting.Store.RecordTransition), atomically with last_state in the
-- SAME fenced UPDATE — never through the row_version-guarded admin Update
-- path (domain.AlertRulePatch's doc comment).
--
-- REFERENCES incident (incident_id) with no ON DELETE clause: Incident has
-- NO Delete method at all (E5.1 decision, incident.go's IncidentRepository
-- doc comment), so the referenced row is never gone — the same reasoning
-- that lets incident_event's own FK to incident omit ON DELETE. Unlike
-- alert_rule.asset_id (caller-supplied, re-verified before write,
-- ADR-ASSET-001 §6), this value is minted or discovered by
-- IncidentStore.FindOrCreateOpenAlertIncident itself under the SAME
-- tenant-scoped transaction as the incident row it names, never accepted
-- from a caller — there is nothing here for a caller to spoof.
ALTER TABLE alert_rule ADD COLUMN IF NOT EXISTS current_incident_id text NULL;
ALTER TABLE alert_rule ADD CONSTRAINT fk_alert_rule_current_incident
    FOREIGN KEY (current_incident_id) REFERENCES incident (incident_id);

CREATE INDEX IF NOT EXISTS ix_alert_rule_current_incident
    ON alert_rule (current_incident_id) WHERE current_incident_id IS NOT NULL;

COMMENT ON COLUMN alert_rule.current_incident_id IS
    'The open, alert-sourced Incident this rule''s firing is currently linked to, or NULL when ok (E4.1). Set/cleared only by the evaluator''s RecordTransition, in the same fenced UPDATE as last_state.';
