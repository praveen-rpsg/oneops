# ADR-ALERTING-007 — Root-cause grouping is symptom-class-aware: a resource incident is never collateral

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-08 |
| **Decider** | Acting CTO |
| **Related** | ADR-ALERTING-004 (E4.2 topology grouping this refines), ADR-ALERTING-005 (E3.4 `symptom_class` primitive), ADR-ALERTING-006 (E3.5 — the suppression-side gate this mirrors), `internal/grouping/reconciler.go`, `internal/domain/incident.go`, `internal/alerting/evaluator.go`, migration `20260914000001_incident_symptom_class.sql`, `docs/PLATFORM-BUILD-PLAN.md` E4.3. Completes the symptom-class consumer arc (E3.5 suppression + E4.3 grouping). |

## Context

E4.2's reconciler roots each open alert incident under the deepest down
dependency (`root_incident_id`), organizing an outage's collateral under its
cause. But it did this for every incident regardless of what the incident was
about — its Honest Bound (like E3.3b's) was that it could not tell a genuinely
independent failure from true collateral. E3.4 added `alert_rule.symptom_class`;
E3.5 consumed it on the suppression side. E4.3 is the grouping-side mirror.

## Decision

**Thread the firing rule's `symptom_class` onto the incident it correlates
into, and make the reconciler gate on it — a `resource` incident is never
rooted as collateral.** Symmetric with E3.5 (gate on the affected node's OWN
class).

- **Migration (additive):** `incident.symptom_class text NOT NULL DEFAULT
  'unspecified'` + a CHECK to the same three values as `alert_rule`. Backfills
  every pre-existing row (manual/security/vuln + pre-E4.3 alert incidents) to
  `unspecified` — no existing grouping outcome changes from the migration alone.
- **Set once at create:** the evaluator's alert-correlation path
  (`FindOrCreateOpenAlertIncident`) copies the firing rule's `symptom_class`
  onto the incident at CREATE. It is a historical write-time fact — never
  re-synced on a later rule PATCH, and never touched when linking to an already-
  open incident. No HTTP write path (same discipline as `root_incident_id`).
- **Reconciler gate:** an incident whose `symptom_class == resource` is never
  assigned a root (`candidate = nil`). **The `down` set is unchanged** — a
  resource incident on Y still marks Y down, so a cascading child can still be
  rooted under Y's incident; only whether the resource incident *itself* is
  treated as someone's collateral is gated. `availability`/`unspecified` group
  exactly as E4.2.

## Consequences

**Guaranteed.** A `resource`-class incident (a CPU/mem/disk problem) stays a
standalone root even when an upstream dependency is also down — it is not
mislabeled as collateral of an outage it is independent of. Cascading
(`availability`) and default (`unspecified`) incidents group as before. The E4.2
mechanics (deepest-root pick, cycle-breaking, self-healing, tenant isolation)
and the alert no-duplicate correlation are unchanged.

**Bound (narrowed).** As with E3.5, an `availability`/`unspecified` incident
coincidentally-independent of a real outage can still be grouped under it;
operators refine by classifying such rules `resource`. Security/vuln incidents
are `unspecified` (they carry no alert-rule class) and are not alert-grouped
anyway (the reconciler is alert-source-scoped).

## Enforcement

- `internal/grouping/reconciler_test.go`:
  `TestReconciler_ResourceSymptomIncidentIsNeverRooted`,
  `..._ResourceSymptomIncidentStillMarksItsAssetDown`,
  `..._AvailabilitySymptomIncidentStillRootsUnderDownDependency`,
  `..._UnspecifiedSymptomIncidentStillRootsUnderDownDependency` — plus every
  existing E4.2 test unchanged (regression). Integration (real PG): correlation
  persists the class; the reconciler gates on it.
- The gate must use `domain.AlertSymptomClassResource`; the `down` candidate set
  must stay class-blind; the column stays create-time-only (no PATCH path).
