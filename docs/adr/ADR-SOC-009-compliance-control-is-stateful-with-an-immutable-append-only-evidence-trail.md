# ADR-SOC-009 — A compliance control is a stateful entity with an immutable, append-only evidence trail

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-008 (risk — the entity+lifecycle mirror), ADR-CONCURRENCY-002 / the incident_event + asset_change_history append-only hardened pattern, ADR-ASSET-001 §6 (FK-alone-is-not-isolation), `internal/domain/compliance_control.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.4b. **Completes E8.4.** |

## Context

E8.4b tracks the controls an org maintains against a framework, and the evidence
filed for each. A control has an implementation lifecycle (stateful entity);
evidence is proof that must not be silently altered after the fact (an audit
trail). Continuous-audit automation (mapping controls to live system checks) is
speculative on a customer-less product and stays deferred.

## Decision

### `compliance_control` — a stateful entity (mirrors `risk`)

Tenant-owned: `framework` (free-ish validated, case-PRESERVING label — an
external standard's own canonical name like `SOC2`/`ISO27001`/`PCI-DSS`, not an
org taxonomy word), `control_ref` (framework-relative id, dots allowed for
`CC6.1`/`A.9.2.3`), `title`/`description`, `status`. `framework`+`control_ref`
are the natural key (`UNIQUE (tenant_id, framework, control_ref)`, tenant-scoped)
and IMMUTABLE after create (provenance, not an edit — patch carries only
title/description). Lifecycle: `not_implemented ↔ in_progress ↔ implemented`,
`… → not_applicable → not_implemented`; row_version-guarded; no Delete
(transition to `not_applicable`). Owner fields deferred (same justification as
ADR-SOC-008).

### `control_evidence` — an immutable, append-only child (mirrors `incident_event`)

A plain append-only table (NOT the audit hash-chain): `kind`
(`url|note|attestation`), `value`, `recorded_by` (the authenticated subject),
`recorded_at`. Hardened exactly like `incident_event`/`asset_change_history`:
`BEFORE UPDATE OR DELETE` and `TRUNCATE` triggers set `ENABLE ALWAYS` (fire even
under `session_replication_role='replica'`), `UPDATE/DELETE/TRUNCATE` REVOKEd,
and the table registered in `SchemaValidator.immutableAuditTables` — so the
platform's audit-immutability + `TestEveryAuditAppendPath_SerialisesOnItsChainHead`
guards sweep it in automatically (now 5 append paths). No update/delete path
exists in the store or API.

### One append path, tenant-safe by the parent's FOR UPDATE

`AddEvidence` runs in one transaction that reads the parent `compliance_control`
under `SELECT … FOR UPDATE` (that read IS the tenant re-verification — RLS makes
a cross-tenant `control_id` invisible → `ErrNotFound` before any evidence is
written; the FK alone would bypass RLS, ADR-ASSET-001 §6), then calls a
`recordEvidence(tx, …)` helper — structured like `IncidentStore.recordEvent` so
no second audit-append path is introduced.

## Consequences

**Guaranteed.** Operators maintain a tenant-isolated control register with a
governed lifecycle and a tamper-evident evidence trail; evidence cannot be
altered or deleted (proven by the immutability guards + REVOKE); cross-tenant
read/write/evidence-append is impossible (proven); the same framework+control_ref
is independent across tenants.

**Deferred / caveat.** Continuous-audit automation (speculative). The standing
customer-validation caveat from ADR-SOC-008 applies to all of E8.4: built to
completion per an explicit directive; value unproven until a customer with a
real compliance obligation exercises it.

## Enforcement

- `TestComplianceControlIsolation_RLSByTenant` (cross-tenant read/patch/status/
  evidence-append all refused; evidence never leaks; tenant-scoped uniqueness).
- `TestEveryAuditAppendPath_SerialisesOnItsChainHead` +
  `TestRuntimeInvariant_AuditImmutabilityDropIsDetectableButUnwatched` +
  RLS/uniqueness/tenant-column guards — all cover the two new tables and must
  stay green.
- `control_evidence` must stay append-only (no update/delete path, triggers
  `ENABLE ALWAYS`, writes REVOKEd); `framework`/`control_ref` must stay
  immutable.
