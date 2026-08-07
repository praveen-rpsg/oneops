-- OPS-S<E8.1b-2> The leader-gated SecurityDetector (E8.1b-2): security_rule
-- gains the evaluator-owned last_transition_at column
-- 20260824000001_alert_rule already gives alert_rule, and incident gains the
-- SECURITY analog of E4.1's alert-correlation wiring
-- (20260826000001_incident_source) — a new source value, its own
-- asset-required check, its own no-duplicate-open-incident partial unique
-- index, and a new timeline event kind. The alert path's own column,
-- constraint and index are UNCHANGED by this migration; every statement here
-- either adds something new or WIDENS a vocabulary shared by every source
-- (source/kind), never narrows or touches the alert-scoped predicate.
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (see 20260819000001's rule):
-- the derived-schema extractor captures only the first ADD of a multi-clause
-- ALTER.

-- security_rule.last_transition_at: evaluator-owned, mirrors
-- alert_rule.last_transition_at exactly (nullable, no CHECK, written only by
-- security.Store.RecordTransition in the SAME statement as last_state).
ALTER TABLE security_rule ADD COLUMN IF NOT EXISTS last_transition_at timestamptz NULL;

COMMENT ON COLUMN security_rule.last_transition_at IS
    'When last_state last actually changed. Set only by the detector''s RecordTransition (E8.1b-2), in the same fenced UPDATE as last_state. NULL means never evaluated to a transition.';

-- incident.source widens from ('manual', 'alert') to also accept 'security'
-- (the detector's own correlation source, E8.1b-2). This is a WIDENING of a
-- vocabulary shared by every incident, not the alert path's own constraint:
-- every existing 'manual'/'alert' row, and every existing query/constraint
-- naming those two values, is unaffected. CHECK constraints cannot be
-- altered in place; drop and recreate widens the same constraint rather than
-- adding a second one — the identical technique 20260826000001 already used
-- to widen incident_event.kind.
ALTER TABLE incident DROP CONSTRAINT ck_incident_source;
ALTER TABLE incident ADD CONSTRAINT ck_incident_source CHECK (source IN ('manual', 'alert', 'security'));

COMMENT ON COLUMN incident.source IS
    'manual (operator-filed), alert (E4.1 correlation-created/linked) or security (E8.1b-2 detector-created/linked). Closed vocabulary. Written once at Create, never changed.';

-- A security-sourced incident always names the CI the firing rule watches
-- (domain.NewSecurityIncident requires assetID) — a SEPARATE constraint from
-- ck_incident_alert_source_has_asset (20260826000001), which is left
-- completely untouched: the alert path's own constraint is unchanged by this
-- migration, byte for byte.
ALTER TABLE incident ADD CONSTRAINT ck_incident_security_source_has_asset
    CHECK (source <> 'security' OR asset_id IS NOT NULL);

-- The SECURITY analog of E4.1's no-duplicate-incident requirement
-- (ux_incident_open_alert_per_asset, 20260826000001): at most one OPEN
-- (not resolved/closed) security-sourced incident per (tenant, asset),
-- enforced at the database. postgres.IncidentStore.
-- FindOrCreateOpenSecurityIncident races an INSERT ... ON CONFLICT
-- (tenant_id, asset_id) WHERE ... DO NOTHING against exactly this index, the
-- identical atomicity shape the alert path's own index gives
-- FindOrCreateOpenAlertIncident. This is a SEPARATE, source-scoped index: it
-- can never conflict with, or be satisfied by, an alert-sourced row, so an
-- alert incident and a security incident on the SAME asset coexist as two
-- distinct open rows.
CREATE UNIQUE INDEX IF NOT EXISTS ux_incident_open_security_per_asset
    ON incident (tenant_id, asset_id)
    WHERE source = 'security' AND status NOT IN ('resolved', 'closed');

-- incident_event.kind gains security_note (E8.1b-2): a free-text correlation
-- note ("security rule <observation_type> fired"/"... recovered"), written
-- only by the detector, never by an HTTP handler — the exact analog of
-- alert_note (20260826000001). Widens the same shared CHECK constraint,
-- mirroring that migration's own technique.
ALTER TABLE incident_event DROP CONSTRAINT ck_incident_event_kind;
ALTER TABLE incident_event ADD CONSTRAINT ck_incident_event_kind
    CHECK (kind IN ('created', 'status_transitioned', 'assigned', 'alert_note', 'security_note'));
