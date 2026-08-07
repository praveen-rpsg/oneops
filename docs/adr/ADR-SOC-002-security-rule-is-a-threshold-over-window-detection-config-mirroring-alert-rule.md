# ADR-SOC-002 — `security_rule` is a threshold-over-window detection config, mirroring `alert_rule`

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-001 (security_observation facts this evaluates), ADR-ALERTING-001..005 (the alert_rule config + evaluator this mirrors), ADR-HARD-003 (DELETE-no-row_version asymmetry), `internal/domain/security_rule.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.1b-1. **Consumed by E8.1b-2** (the detector worker — separate). |

## Context

SOC detection over `security_observation` facts (ADR-SOC-001) needs a config
primitive. Rather than invent one, mirror the proven `alert_rule` shape: the
alert side already solved config + evaluator-owned firing state + incident
correlation + tenancy, and SOC's first detection is the direct analog —
"count matching facts in a window, fire past a threshold."

## Decision

`security_rule` is a tenant-owned config row (NOT a hypertable, NOT a reified
detection/alert noun — a config, exactly like `alert_rule`). It expresses a
**threshold-over-window SIEM detection**: for its `asset_id`, count
`security_observation` rows of `observation_type` at/above `min_severity` in a
trailing `window_seconds`; when the count reaches `threshold_count`, fire and
raise an incident of `incident_severity`. Fields mirror `alert_rule`, including
the **evaluator-owned** `last_state` (`ok|firing`) and `current_incident_id`
(nullable link) — present but written by nothing until E8.1b-2's detector, the
same staging `alert_rule` used before its evaluator existed.

Reused disciplines (identical to alert_rule): server-minted `rule_id`;
tenant-scoped `appPool` CRUD with `asset_id` re-verified against the caller's
tenant; FORCE RLS `tenant_isolation`; PATCH requires `row_version` (409 on
stale); DELETE carries no `row_version` (ADR-HARD-003 asymmetry, documented).
CRUD at `POST/GET/PATCH/DELETE /v1/admin/security-rules` (PermAdmin).

### Two distinct severity vocabularies, kept distinct

`min_severity` uses the observation severity (`info|low|medium|high|critical`,
ADR-SOC-001) — it filters which facts count. `incident_severity` uses the
INCIDENT severity (`critical|high|medium|low`, `domain.IncidentSeverity`) — it
sets the raised incident's severity. They are not collapsed into one type; a
round-trip test proves they never conflate.

## Consequences

**Guaranteed.** Operators can define/list/edit/delete SIEM detection rules,
tenant-isolated (proven by a two-tenant test; swept into the RLS/uniqueness/
tenant-column arch guards). Nothing evaluates them yet — `last_state`/
`current_incident_id` stay at their defaults until E8.1b-2.

**Not built here / deferred.** No detector, no reading of observations, no
incident creation (E8.1b-2). `last_transition_at` and any flap/dwell
bookkeeping are deliberately NOT on the table yet — E8.1b-2 adds them if its
transition discipline needs them (additive), rather than speculatively now.
Rule scope is a single `asset_id` (cross-asset/correlation detection is a later
refinement, consistent with alert_rule's own asset-scoped start).

## Enforcement

- `TestSecurityRuleIsolation_RLSByTenant` + `..._CreateRejectsCrossTenantOrNonexistentAsset`
  + `..._MinSeverityAndIncidentSeverityRoundTrip`
  (`internal/store/postgres/security_rule_store_integration_test.go`) — the
  isolation + severity-distinctness gates.
- The RLS/uniqueness/tenant-column arch guards sweep `security_rule` in
  automatically; contract bijection keeps the routes and schema in lockstep.
- If E8.1b-2 gives `last_state`/`current_incident_id` a writer, it must keep
  them evaluator-owned (never create/patch-settable), as `alert_rule` does.
