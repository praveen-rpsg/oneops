-- OPS-S<e3.2> (E3.2) alert_rule flap suppression — de-flap/hysteresis dwell.
-- Extends the existing config+derived-state row (20260824000001,
-- 20260826000002); no new table, no reified Alert/Event/Signal row
-- (docs/PLATFORM-BUILD-PLAN.md §4, Vol II §5.3, ADR-ALERTING-001).
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (see 20260819000001's rule):
-- the derived-schema extractor captures only the first ADD of a multi-clause
-- ALTER.
--
-- flap_dwell_seconds is a rule-level, operator-tunable setting (a config
-- field, like for_duration_seconds — patchable through the row_version-
-- guarded Update path, unlike the columns below). Defaulted to 60s for every
-- existing row so E3.1-era rules gain real, sane suppression on this
-- migration rather than silently reverting to "no dwell" — 0 (E3.1's original
-- immediate-transition behaviour) remains available but must be chosen
-- explicitly per rule (ADR-ALERTING-001).
--
-- pending_state/pending_since are the dwell's own bookkeeping: the
-- not-yet-committed candidate the evaluator is currently timing, set/cleared
-- ONLY by the evaluator's background write path (alerting.Store.
-- RecordPending), atomically cleared by RecordTransition when a candidate is
-- finally committed, and atomically cleared by the admin Update path on any
-- edit — never through Update as a value a caller supplies (AlertRulePatch's
-- doc comment). Both NULL means nothing is currently pending, the common case
-- for a rule that is not flapping.
ALTER TABLE alert_rule ADD COLUMN IF NOT EXISTS flap_dwell_seconds integer NOT NULL DEFAULT 60;
ALTER TABLE alert_rule ADD CONSTRAINT ck_alert_rule_flap_dwell_seconds
    CHECK (flap_dwell_seconds BETWEEN 0 AND 86400);

ALTER TABLE alert_rule ADD COLUMN IF NOT EXISTS pending_state text NULL;
ALTER TABLE alert_rule ADD CONSTRAINT ck_alert_rule_pending_state
    CHECK (pending_state IS NULL OR pending_state IN ('ok', 'firing'));

ALTER TABLE alert_rule ADD COLUMN IF NOT EXISTS pending_since timestamptz NULL;
-- Both columns travel together (RecordPending's own doc comment): either
-- nothing is pending (both NULL) or a candidate and its start time both are.
ALTER TABLE alert_rule ADD CONSTRAINT ck_alert_rule_pending_state_since_paired
    CHECK ((pending_state IS NULL) = (pending_since IS NULL));

COMMENT ON COLUMN alert_rule.flap_dwell_seconds IS
    'How long a candidate transition must be observed continuously before the evaluator commits it (E3.2 de-flap dwell). 0 disables the dwell for this rule. Operator-patchable, unlike pending_state/pending_since.';
COMMENT ON COLUMN alert_rule.pending_state IS
    'The not-yet-committed candidate state the evaluator is currently timing, or NULL. Set only by the evaluator''s background write path, never through the row_version-guarded Update path — see this table''s header comment.';
COMMENT ON COLUMN alert_rule.pending_since IS
    'When pending_state was first observed continuously. NULL exactly when pending_state is NULL.';
