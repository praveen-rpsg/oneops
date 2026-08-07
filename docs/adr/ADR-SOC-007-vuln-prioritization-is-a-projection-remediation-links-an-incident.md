# ADR-SOC-007 — Vulnerability prioritization is a computed projection; remediation links a `vuln`-sourced incident idempotently

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-006 (the vuln_finding entity), ADR-ASSET-001 (AssetCriticality), ADR-NOC-001 (read-projection pattern), ADR-SOC-003 (the source-widening + incident-correlation precedent), `internal/store/postgres/vuln_finding_store.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.3b. **Completes E8.3.** |

## Context

E8.3a gave stateful vuln findings. E8.3b makes them actionable: rank by business
risk, and open tracked remediation work from a finding.

## Decision

### Prioritization is a computed read projection (no stored priority)

`GET /v1/admin/vuln-findings/prioritized` (PermAdmin) joins open `vuln_finding`
to its `asset`'s `criticality` and ranks at query time:
`score = severityRank × criticalityRank`, both scales **1-based**
(severity none=1..critical=5; criticality unknown=1, low=2..critical=5). 1-based
deliberately: a 0-based `unknown`/`none` would zero the product and sink a
high-severity finding on an unclassified asset; 1-based keeps every combination
strictly positive while still ranking `unknown` (1) strictly below `low` (2).
Ties break `last_seen DESC`, then `finding_id`. Bounded (default 50/max 500),
RLS-`appPool` isolation only (ADR-NOC-001 projection pattern), a transient DTO
carrying the finding + asset criticality + a computed priority band. **No stored
priority column** — reduced-concept.

### Remediation links a `vuln`-sourced incident, idempotently

Additive `vuln_finding.remediation_incident_id text NULL REFERENCES incident`
(a nullable link, like `alert_rule.current_incident_id`; a partial unique index
where non-null). `POST /v1/admin/vuln-findings/{id}/remediate` (PermAdmin,
row_version body) opens or returns the finding's remediation incident.

- **`IncidentSourceVuln`** added (widening the `source` CHECK `manual|alert|
  security|vuln`, mirroring the E8.1b-2 `security` widening + asset-required
  check) so remediation incidents are filterable by their own provenance. The
  alert/security partial no-dup indexes are UNTOUCHED.
- **No new partial no-dup index** — unlike alert/security correlation (natural
  key `(tenant, asset)`), the idempotency key here is the finding's OWN row.
  `Remediate` runs in one transaction holding `SELECT … FOR UPDATE` on the
  finding for its full read→decide→write span, so concurrent calls serialize
  and the loser's `row_version` check fails before it can create a second
  incident (proven: 10 goroutines → exactly one incident). A second call while
  the linked incident is open returns it; after it closes, a new one opens.
- Reduced-concept: the link is a column, the work-item is the existing
  `incident` (no reified Remediation noun). The remediation incident append
  path reuses `IncidentStore.recordEvent` (the arch audit-append-serialisation
  guard caught and rejected an initial inlined insert — one append path only).

## Consequences

**Guaranteed.** Operators see open findings ranked by severity×CI-criticality
(tenant-isolated, bounded, computed) and can open exactly one tracked
remediation incident per finding, idempotently and concurrency-safe, sourced as
`vuln`. E8.3a ingestion/lifecycle and the alert/security paths are unchanged.

**Deferred.** Auto-close-on-absence (E8.3a's deferral stands); ownership-scoped
remediation authorization; a distinct triage-note kind. Behavioral/anomaly
detection remains E13/AI.

## Enforcement

- `TestVulnFindingStore_Remediate_ConcurrentCallsCreateOnlyOneIncident` (10
  goroutines), `..._IsIdempotentOnOpenLink`, `..._OpensNewIncidentAfterPriorOneClosed`,
  `..._Prioritized_UnknownCriticalityNeverOutranksKnownLow`,
  `..._Prioritized_TieBreaksByLastSeenThenFindingID`, `..._Prioritized_TenantIsolation`
  (`internal/store/postgres/vuln_finding_remediation_integration_test.go`).
- `TestEveryAuditAppendPath_SerialisesOnItsChainHead` must stay green (one
  append path). Prioritization must never gain a stored priority column; the
  remediate FOR-UPDATE idempotency guard must not be weakened.
