# ADR-SOC-008 — The risk register is a stateful entity with a governed lifecycle and a computed (never stored) likelihood×impact score

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-006 (vuln_finding — the entity+lifecycle pattern mirrored), ADR-SOC-007 (the computed-projection pattern mirrored), ADR-NOC-001 (projection), `internal/domain/risk.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.4a. **First half of E8.4; compliance controls are E8.4b.** |

## Context

E8.4 is compliance + risk. The concrete, reduced-concept-clean, non-speculative
core is a risk register: operator-authored risks with a lifecycle and a risk-
matrix score. (Continuous-audit automation — mapping controls to live checks —
is speculative on a customer-less product and is deferred; see the standing
charter note.)

## Decision

`risk` is a tenant-owned stateful reified entity (like `incident`/`vuln_finding`
— legitimate, not a false-noun): `title`, `description`, `category` (free-ish
validated label), `likelihood` (closed `rare..almost_certain`, rank 1..5),
`impact` (closed `negligible..severe`, rank 1..5), `status`, `treatment`
(optional `mitigate|accept|transfer|avoid`), nullable `asset_id`
(`ON DELETE SET NULL` — a risk is standalone, unlike a vuln finding), server-
minted `risk_id` (no natural dedup key — operator-authored, not scan-deduped).

**Lifecycle** (row_version-guarded, illegal → 409): `open →
{mitigating,accepted,closed}`; `mitigating → {accepted,closed,open}`; `accepted
→ {open,closed}`; `closed → open`. No Delete (transition to `closed`).

**Score is a computed projection, never stored.** `GET /v1/admin/risks/register`
ranks non-closed risks by `score = likelihoodRank × impactRank` (both 1-based,
so an untiered dimension can't zero the product — same rationale as ADR-SOC-007),
with a severity band, tie-broken `score DESC, updated_at DESC, risk_id`. RLS-
`appPool`, bounded, transient DTO. Full CRUD at `/v1/admin/risks` (+ the register
projection).

**Deferred (justified in code):** owner_team_id/owner_user_id (an ownership
workflow this story's value — register + scoring — doesn't need; two extra
cross-tenant checks per write for nothing yet); `source_finding_id` (a
vuln-finding→risk link, non-trivial cross-lifecycle behavior — a risk can name
the same `asset_id` today).

## Consequences

**Guaranteed.** Operators maintain a tenant-isolated risk register with a
governed lifecycle and a live likelihood×impact ranking; no stored score
(reduced-concept); mirrors vuln_finding's proven shape.

**Deferred / caveat.** Compliance controls + evidence (E8.4b). Continuous-audit
automation is out of scope (speculative). **Standing CTO caveat:** compliance/
risk is the least customer-validated SOC surface; it is built to completion per
an explicit "continue until fully" directive, but its value is unproven until a
customer with a real compliance obligation exercises it.

## Enforcement

- `TestRiskIsolation_RLSByTenant`, `TestRiskStore_Register`,
  `TestRiskStore_SetStatusTransitions`, `TestRiskStore_UpdateRevalidatesTheWholeEntity`
  (`internal/store/postgres/risk_store_integration_test.go`) + the domain
  transition/rank unit tests. RLS/uniqueness/tenant-column guards cover `risk`.
- The score must stay computed (never a stored column); transitions must stay
  row_version-guarded; no Delete.

## Follow-up noted (not this story)

`internal/kg/extract/schema.TestExtractionIsWithinBudget` (a 200ms wall-clock
budget) is load-sensitive under full `-race` runs (passes in isolation); a
pre-existing timing-test fragility, not a correctness regression — worth
converting to a non-wall-clock assertion in a future hardening pass.
