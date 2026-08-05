-- OPS-S4.2 (E4.2) incident.root_incident_id: the topology-aware grouping LINK
-- — a column, not a reified Group/Correlation entity (docs/PLATFORM-BUILD-PLAN.md
-- §4, mirrors alert_rule.current_incident_id's own header comment,
-- 20260826000002). NULL means "this incident is a root or standalone"; a
-- non-NULL value names the OPEN, alert-sourced incident this one is grouped
-- under, because its own asset transitively depends (depends_on/runs_on
-- only, cycle-safe recursive-CTE traversal — the same edge-type filter
-- ADR-ALERTING-003 §2 uses) on the root's asset, which is itself "down" (has
-- its own open, alert-sourced incident). The leader-gated reconciliation
-- pass in internal/grouping computes and maintains this column
-- (ADR-ALERTING-004); there is no operator-facing write route for it at all.
--
-- One ADD COLUMN/ADD CONSTRAINT per statement (20260826000002's own rule): the
-- derived-schema extractor (internal/kg/extract/schema) captures only the
-- first ADD of a multi-clause ALTER.
--
-- REFERENCES incident (incident_id) ON DELETE SET NULL — unlike
-- alert_rule.current_incident_id's no-ON-DELETE-clause FK (safe there only
-- because Incident has no Delete method at all, incident.go's own doc
-- comment), this reference is given one anyway for defense in depth against
-- a future schema change: "the linked root disappeared" and "nothing was
-- ever linked" should read identically through this column regardless.
--
-- ck_incident_root_not_self is a DB-level belt for the single-row case (an
-- incident can never name itself as its own root); it is NOT the platform's
-- only defense against a pointer CYCLE across multiple rows — a check
-- constraint cannot see other rows. Cycle-freedom across rows is a property
-- of the reconciliation algorithm itself (internal/grouping's
-- resolveFinalRoots, ADR-ALERTING-004 §4), verified by a dedicated test, not
-- by this schema.
ALTER TABLE incident ADD COLUMN IF NOT EXISTS root_incident_id text NULL;
ALTER TABLE incident ADD CONSTRAINT fk_incident_root_incident
    FOREIGN KEY (root_incident_id) REFERENCES incident (incident_id) ON DELETE SET NULL;
ALTER TABLE incident ADD CONSTRAINT ck_incident_root_not_self
    CHECK (root_incident_id IS NULL OR root_incident_id <> incident_id);

CREATE INDEX IF NOT EXISTS ix_incident_root_incident ON incident (root_incident_id) WHERE root_incident_id IS NOT NULL;

COMMENT ON COLUMN incident.root_incident_id IS
    'The OPEN, alert-sourced incident this one is grouped under (E4.2), or NULL when this incident is itself a root/standalone. Set/cleared ONLY by internal/grouping''s leader-gated reconciliation pass — never through the row_version-guarded admin Update path. ck_incident_root_not_self prevents a self-pointer; a multi-row pointer cycle is prevented by the reconciliation algorithm, not by this schema (ADR-ALERTING-004).';
