# EVR-002 — Authorization scope (entry 2) and producer identity (entry 15)

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Auditor** | Acting CTO / Evidence Authority |
| **Trust Register entries** | 2, 15 |
| **Associated ADR** | ADR-AUTHZ-001 (permission scope and the platform boundary), ADR-CONCURRENCY-003 (production is idempotent) |
| **Confidence** | **VALIDATED** — both classes confirmed closed. Evidence Confidence raised **ECL-3 → ECL-5**. |

## Why these two were selected

Not by subject matter. Both were selected because their enforcement was
**enumerated**, which has predicted a sibling instance in three consecutive
audits:

- ADR-AUTHZ-001's guard is AST-based but its subject set is
  `platformHandlers = []string{"listTenants", "createTenant", "patchTenant"}`.
- ADR-CONCURRENCY-003's guard names three files: `relay.go`, `replay.go`,
  `consumer.go`.

Under the enforcement philosophy, sibling instances were assumed to exist until
disproven.

## Original claims and evidence

**Entry 2.** Tenant-registry operations were routed under `PermAdmin` — the same
permission that administers webhooks *inside* a tenant — so any tenant
administrator could enumerate every customer, register tenants binding
identifiers of their choosing, and suspend a different customer. Verified live;
closed by routing them through `requirePlatformAdmin`.

**Entry 15.** Delivery and execution rows were minted with random ids, so a
re-produced row became a new row with a new dedup key. Closed by content-derived
`DeliveryID`/`ExecutionID`, which collide on the primary key instead.

## Fresh investigation

**Entry 2 — sibling search.** Every route registration in the router was walked
by AST and filtered on the `/admin/tenants` prefix rather than on handler names.
Three routes found; **all three** registered through `requirePlatformAdmin`. No
fourth tenant-registry route exists. **No sibling instance.**

**Entry 15 — sibling search.** Every non-test `.go` file in the tree was walked,
and every composite literal constructing a `Delivery{` or `Execution{` with an
`ID:` field examined. Three producers found; all derive identity from the helper.
**No sibling instance.**

One near-miss was investigated and cleared: `newDeliveryID()` — the random
generator ADR-CONCURRENCY-003 replaced — is still present and still called, by
`randDeliveryHex()` for `NewReplayJobID()`. That produces a **replay job**
identifier, not a queued row. A job id is not content-addressable work and is
correctly random. Not a defect.

## Current evidence

Both classes reproduce as closed under fresh, structural analysis. The
enforcement was replaced:

- `TestEveryTenantRegistryRoute_RequiresPlatformAdmin` derives its subject set
  from the router's route paths, so a new tenant route is covered the moment it
  is added.
- `TestEveryQueuedRowProducer_UsesDerivedIdentity` walks the tree, so a new
  producer anywhere is covered.

**Mutation verification.** Adding a fourth `/admin/tenants/{id}` route under
tenant-scope admin fails the build with the ADR-AUTHZ-001 diagnostic — precisely
the sibling the enumerated guard would have missed. Switching a producer back to
`newDeliveryID()` fails with the ADR-CONCURRENCY-003 diagnostic.

**A hole was found in the sweep itself and fixed.** The producer sweep initially
matched its helper with `strings.Contains`, and `newDeliveryID(` *contains*
`DeliveryID(` — so the mutation passed silently. The check now matches on a word
boundary. A sweep that silently passes is worse than no sweep, because it is
believed; this is recorded so the next sweep author checks for it.

## Confidence: VALIDATED — ECL-5

Both entries earn **ECL-5**: fresh engineering evidence, whole-tree and
router-derived completeness, structural enforcement, and mutation verification in
both directions. Previously **ECL-3** (fresh evidence, known-instance
enforcement).

## Class status

- **Entry 2 — CLASS CLOSED.** No sibling instances; enforcement is now
  route-derived.
- **Entry 15 — CLASS CLOSED.** No sibling instances; enforcement is now
  tree-derived.

## Required Trust Register changes

Both entries recorded as CLASS CLOSED at ECL-5, with their enforcement style
noted as structural. No reopening required — the first audit in this programme to
**confirm** prior conclusions rather than overturn them.

## Residual risk

- The route sweep keys on the `/admin/tenants` prefix. If the tenant registry is
  ever mounted elsewhere, the guard must move with it; it fails loudly (`no
  tenant-registry routes found`) rather than passing vacuously if the prefix
  disappears.
- The producer sweep recognises a producer by a composite literal with an `ID:`
  field within a 400-character window. A producer that assigns the id in a
  materially different shape would not be detected.
- Entry 15's guarantee remains what ADR-CONCURRENCY-003 claimed: identity is
  stable across re-production. It does not make delivery exactly-once.
