-- OPS-S3.4 (E3.4) alert_rule.symptom_class — the taxonomy PRIMITIVE only.
-- Extends the existing config+derived-state row (20260824000001,
-- 20260827000001, 20260830000001); no new table, no reified taxonomy
-- entity/noun (docs/PLATFORM-BUILD-PLAN.md §4, ADR-ALERTING-005).
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (20260819000001's rule): the
-- derived-schema extractor (internal/kg/extract/schema) captures only the
-- first ADD of a multi-clause ALTER.
--
-- symptom_class classifies WHAT a rule detects (availability = reachability/
-- up-ness, which cascades through dependencies; resource = a
-- resource/utilization metric such as cpu/disk/mem/latency, which usually
-- does not) — see domain.AlertSymptomClass's doc comment. It is explicit and
-- operator-set, NEVER inferred from metric (too fragile, would silently
-- mis-classify), defaulting to 'unspecified' both for new rows and, via this
-- DEFAULT, for every row that predates this migration: every existing rule
-- behaves EXACTLY as it did before this column existed. Patchable through
-- the row_version-guarded admin Update path, the same config-field treatment
-- flap_dwell_seconds gets (unlike last_state/pending_state, which only the
-- evaluator sets).
--
-- E3.4 IS THE PRIMITIVE ONLY: nothing reads this column to make a decision
-- yet. Class-scoped refinements of E3.3b dependency suppression and E4.2
-- root-cause ranking are separate, later stories that consume it.
ALTER TABLE alert_rule ADD COLUMN IF NOT EXISTS symptom_class text NOT NULL DEFAULT 'unspecified';
ALTER TABLE alert_rule ADD CONSTRAINT ck_alert_rule_symptom_class
    CHECK (symptom_class IN ('availability', 'resource', 'unspecified'));

COMMENT ON COLUMN alert_rule.symptom_class IS
    'WHAT this rule detects (E3.4): availability | resource | unspecified. Explicit, operator-set, never inferred from metric; defaults to unspecified for every pre-existing and new rule. Primitive only — see ADR-ALERTING-005; not consumed by any decision path yet.';
