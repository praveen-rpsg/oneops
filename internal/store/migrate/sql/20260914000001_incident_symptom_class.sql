-- OPS-S-E4.3 (E4.3) incident.symptom_class — threads E3.4's alert-rule
-- taxonomy primitive (20260831000001, ADR-ALERTING-005) onto the incident an
-- alert firing correlates into, so E4.2's topology-aware grouping reconciler
-- (internal/grouping, ADR-ALERTING-004) can gate on it (ADR-ALERTING-007),
-- the exact grouping-side counterpart of E3.5's dependency-suppression gate
-- (ADR-ALERTING-006).
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (20260826000002's rule): the
-- derived-schema extractor (internal/kg/extract/schema) captures only the
-- first ADD of a multi-clause ALTER.
--
-- symptom_class classifies WHAT the incident is about, mirroring
-- alert_rule.symptom_class's own three-value enum exactly (availability |
-- resource | unspecified). It is additive and non-breaking: DEFAULT
-- 'unspecified' backfills every pre-existing row (manual/security/vuln
-- incidents, and every alert incident correlated before this column
-- existed) to the same class an un-classified alert_rule already gets, so no
-- existing row's grouping outcome changes because of this migration alone.
--
-- Only internal/alerting's evaluator (E4.1's correlation path,
-- FindOrCreateOpenAlertIncident) ever writes a non-default value here, at
-- CREATE time, copying the firing rule's own symptom_class — mirrored from
-- rule to incident once, never re-synced on a later rule PATCH (the
-- incident is a historical record of the condition that opened it, the same
-- "derived write-time fact" treatment domain.Incident's other correlation
-- fields already get). There is no HTTP write path for it at all — the same
-- evaluator/reconciler-only write discipline root_incident_id already has.
ALTER TABLE incident ADD COLUMN IF NOT EXISTS symptom_class text NOT NULL DEFAULT 'unspecified';
ALTER TABLE incident ADD CONSTRAINT ck_incident_symptom_class
    CHECK (symptom_class IN ('availability', 'resource', 'unspecified'));

COMMENT ON COLUMN incident.symptom_class IS
    'WHAT this incident is about (E4.3): availability | resource | unspecified, mirroring alert_rule.symptom_class. Set once at create by the alert-correlation pipeline from the firing rule''s own class; unspecified (the default) for manual/security/vuln incidents and every pre-E4.3 row. Consumed by internal/grouping''s reconciler (ADR-ALERTING-007): a resource-class incident is never rooted under a down dependency.';
