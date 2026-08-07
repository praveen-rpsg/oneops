# ADR-SOC-003 — A leader-gated SecurityDetector raises security-sourced incidents, mirroring the alerting evaluator with its own no-duplicate constraint

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-001 (observations), ADR-SOC-002 (security_rule config), ADR-ALERTING-001/004 (the evaluator + E4.1 correlation this mirrors), ADR-CONCURRENCY-002 (incident timeline), ADR-TENANCY-012 (correlation cross-tenant defense), `internal/security/detector.go`, `internal/store/postgres/incident_store.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.1b-2. **Completes E8.1 (SOC ingestion → detection → incident).** |

## Context

E8.1a gave SOC append-only observation facts; E8.1b-1 gave `security_rule`
detection configs with staged (unwritten) firing state. E8.1b-2 is the worker
that closes the loop: evaluate rules over observations and raise incidents, so
security detections flow into the SAME incident/grouping/paging/escalation loop
the ops side already has. The alerting evaluator + E4.1 correlation already
solved this exact shape; SOC mirrors it rather than inventing a parallel stack.

## Decision

A leader-gated `security.Detector` (`internal/security/detector.go`), built in
the `alerting.Evaluator`'s exact shape (Config/Store/Run/RunOnce, keyset
paging, bounded concurrency, transition-only). Each pass, per enabled
`security_rule`: `CountForTenant(tenant, asset, observation_type, ≥min_severity,
now-window, now)` (a SQL `COUNT(*)`, never pulling rows into Go); next state =
`firing` if count ≥ `threshold_count` else `ok`; on a transition, correlate and
`RecordTransition` (writes `last_state`, the new `last_transition_at`,
`current_incident_id`). Wired into `workers` under `RunAsLeader`, exactly like
the evaluator and grouping reconciler.

### Security-sourced incidents, coexisting with alert incidents

`IncidentSource` gains `security` (a widening of the `manual|alert` vocabulary).
`IncidentStore.FindOrCreateOpenSecurityIncident`/`AppendSecurityNote` mirror the
alert methods byte-for-byte in shape. A security incident and an alert incident
on the SAME asset are two SEPARATE open incidents (source-distinguished), each
with its own no-duplicate guarantee.

### No-duplicate is a DB constraint, mirrored not shared

A new partial unique index `ux_incident_open_security_per_asset`
(`(tenant_id, asset_id) WHERE source='security' AND status NOT IN
('resolved','closed')`) mirrors `ux_incident_open_alert_per_asset`'s predicate
exactly but is entirely separate. Two concurrent security firings on one
(tenant, asset) can never both insert (proven by a real concurrency test); the
alert index is untouched (its own concurrency test still passes unchanged).
Only the shared `ck_incident_source` / `ck_incident_event_kind` CHECKs were
*widened* (new allowed values) — a change that cannot affect existing
alert/manual rows or any predicate over them.

### Recovery matches the evaluator exactly

On `firing→ok`, the detector appends a `security_note` to the linked incident
and clears `current_incident_id` — it NEVER auto-resolves the incident (the
`IncidentCorrelator` interface has no status-changing method). Byte-identical
scope discipline to `alerting.Evaluator`'s recovery half.

### Guards respected, not weakened

Mirroring `AlertRuleStore`'s single dual-role struct would have pushed the
type-granular `TestPrivilegedReads_AreScopedToATenant` exemption list past its
cap. Instead the privileged detector surface is split into minimal per-role
types (`SecurityRuleDetectorStore`, `SecurityObservationCounterStore`) exposing
only what the detector needs — **0 new privileged-read exemptions**; the arch
guards pass unmodified.

## Consequences

**Guaranteed.** SOC is operational end-to-end: ingest observations → threshold/
window detection → a security-sourced incident (no duplicates, tenant-isolated)
→ the existing group/page/escalate loop. The alert path is behaviorally
unchanged (its code untouched; only shared CHECKs widened; its tests green).

**Not built / deferred.** IOC/behavioral detection (E8.2) — this is
threshold/window only. `security_rule` is single-asset-scoped (cross-asset
correlation later). The E9.1 incident-trends dashboard counts security
incidents in `opened_total` but has no dedicated `security` bucket yet (a NOC
dashboards follow-up, flagged, not in this story). Retention/legal-hold/chain-
of-custody (E8 edge cases) still inherit telemetry's posture.

## Enforcement

- `TestIncidentStore_FindOrCreateOpenSecurityIncident_ConcurrentFiringsNoDuplicate`
  (real concurrency), `..._AlertAndSecurityIncidentsCoexistOnSameAsset`,
  `..._AppendSecurityNote_TenantIsolation`, the detector's transition unit
  tests, and the recovery test — the correctness gate. The alert-path
  concurrency/correlation tests must stay green unmodified (regression proof).
- The two partial unique indexes must remain separate; widening either source
  CHECK to *narrow* it, or merging the indexes, contradicts this ADR.
- The detector's tenancy posture (leader-gated, per-tenant, explicit tenant_id)
  must match the evaluator's; the split privileged store types must not grow
  into general-purpose privileged readers (that would need a guard exemption
  and a superseding decision).
