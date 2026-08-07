# ADR-SOC-006 — A vulnerability finding is a stateful entity; scan ingestion is a false-positive-protected UPSERT

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-001..005 (SOC), ADR-ASSET-001 (the CI a finding names), ADR-HARD-003 (delete-vs-transition), `internal/domain/vuln_finding.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.3a. **Consumed by E8.3b** (prioritization + remediation-to-incident — separate). |

## Context

E8.3 is vulnerability management. Unlike a `security_observation` (an
append-only fact), a vulnerability persists on an asset until remediated, must
dedup across scans, and carries operator judgment (accepted-risk, false
positive). So a `vuln_finding` is a legitimate STATEFUL reified entity — the
same category as `incident`/`asset`, not a false-noun.

## Decision

`vuln_finding` is a tenant-owned stateful entity, deduped by
`UNIQUE (tenant_id, asset_id, vuln_id)`, with a lifecycle state machine:
`open → {remediated, accepted_risk, false_positive}`;
`remediated/accepted_risk → open` (reappearance); operator transitions via
`PATCH` are `row_version`-guarded (409 on stale / illegal transition). No
`Delete` (transition, not delete — mirrors `incident`). Severity is a new
closed `VulnFindingSeverity` (`none|low|medium|high|critical`, the CVSS scale) —
distinct from incident-severity (operational impact) and observation-severity
(SIEM notability).

### Scan ingestion is an idempotent, false-positive-protected UPSERT

`POST /v1/admin/vuln-findings` batch-UPSERTs by `(tenant, asset, vuln_id)`:
- absent → INSERT `open`, `first_seen=last_seen=now`.
- `open`/`remediated`/`accepted_risk` → refresh `last_seen`/title/severity/
  scanner/description; force back to `open` if it had been remediated/accepted
  (the fix/acceptance didn't hold — it's in the scan again).
- **`false_positive` → only `last_seen` advances**; status and all scan fields
  are left as the operator judged them. Enforced ATOMICALLY inside the single
  `INSERT ... ON CONFLICT DO UPDATE` (a `CASE` reading the row's own pre-update
  status), so a scan can never overturn an operator's false-positive judgment.
  `false_positive → open` is reachable ONLY via manual `PATCH`.

asset_id is re-verified against the caller's tenant (a finding for an unknown/
cross-tenant asset is skipped with a reason, like observation ingestion); the
UPSERT is idempotent (re-ingesting a scan never duplicates). Tenant-scoped
`appPool`, FORCE RLS.

## Consequences

**Guaranteed.** Scans keep findings current without duplicates or losing
operator triage; the same (asset, vuln) is independent across tenants; a
false-positive judgment survives re-scans (adversarially tested). The RLS/
uniqueness/tenant-column guards cover the table.

**Deferred (E8.3b / later).** Prioritization ranking (severity × CI
criticality — a projection, not a stored field), remediation→incident linkage,
and auto-close-on-absence (needs full-scan-scope semantics). Operator triage
notes distinct from the scan-owned `description` are a possible follow-up.

## Enforcement

- `TestVulnFindingStore_FalsePositiveIsNotReopenedByIngestion` (adversarial
  payload), `..._IdempotentReingestDoesNotDuplicate`,
  `..._RemediatedReappearsAsOpen`, `TestVulnFindingIsolation_RLSByTenant`
  (`internal/store/postgres/vuln_finding_store_integration_test.go`) — the
  correctness + isolation gates; plus the domain transition-map unit tests.
- The platform RLS/uniqueness/tenant-column guards cover `vuln_finding`;
  contract bijection keeps routes/schema in lockstep.
- The false-positive protection must stay inside the UPSERT statement (atomic);
  scan ingestion must never set status via any path but the documented rule.
