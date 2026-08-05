# ADR-ALERTING-002 — Maintenance windows suppress the incident/notify SIDE EFFECT of a firing, never its derivation, and cover a single asset

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-05 |
| **Decider** | Acting CTO |
| **Related** | ADR-ALERTING-001 (flap suppression — this ADR composes downstream of it, never inside it), ADR-TENANCY-012 (privileged reads require an explicit tenant predicate — extended here a third time), ADR-ASSET-001 §6 (ownership re-verified against the writer's own tenant-scoped connection — extended to `maintenance_window.asset_id`), ADR-TENANCY-009 (privileged mutations scoped to an owner — `Suppress`'s CTE has no caller-supplied id set to key on, so this ADR does not add a new instance of that class), docs/PLATFORM-BUILD-PLAN.md §4 / Vol II §5.3 (reduced-concept discipline) |

## Context

E3.1/E3.2/E4.1's evaluator (`internal/alerting.Evaluator`) commits an
`ok->firing` transition by enqueuing a `Notification` and, if wired,
creating-or-linking an `Incident` — unconditionally, the instant a candidate
clears E3.2's dwell. That is correct for an unplanned failure. It is wrong for
a CI an operator has already taken down on purpose (a patch window, a planned
network change): the platform pages for something nobody needs to be told
about, because nothing in the evaluator's decision path today knows the
difference between "broke" and "we broke it on purpose."

E3.3 (the original, bundled backlog item) named two different kinds of
suppression this problem needs: a simple, operator-declared time window over
one CI, and a harder, topology-aware suppression that infers "this CI is only
down because its dependency is down" from the CMDB graph
(`docs/PLATFORM-BUILD-PLAN.md` E3.3b). They were split (`067df2e`) because
they carry unrelated risk: the first is a small, well-understood CRUD +
one-predicate-more read; the second needs graph traversal, cycle handling,
and a sound "is the root actually down" signal. This ADR is E3.3a only — the
first kind. E3.3b remains unbuilt and is not attempted here.

## Decision

### 1. Target model: a single `asset_id`, not a tag/label selector, not dependency-aware

A maintenance window names exactly one Configuration Item
(`maintenance_window.asset_id`, a real `REFERENCES asset (asset_id)`), the
same shape `alert_rule.asset_id` and `collector_check.asset_id` already use.
Two broader models were considered and rejected for this story:

- **A tag/label selector** ("suppress every CI matching `env=staging`").
  Rejected: it would need a query-time selector-to-CI resolution the platform
  does not have anywhere else yet (CMDB tagging is out of this story's
  scope), and it silently widens blast radius as new CIs are tagged after the
  window is declared — an operator who meant "these three hosts" could
  accidentally suppress a fourth added later.
- **Dependency-aware** (suppress a root CI's declared downstream/dependent
  CIs too, via `asset_relationship`). This is exactly E3.3b's job — graph
  traversal, cycle handling, and a correctness bar this story does not
  attempt to clear. Building even a partial version of it here would smuggle
  E3.3b's real risk into a story sized around it not existing yet.

A single `asset_id` is the smallest model that satisfies the story's actual
acceptance criterion ("an affected CI inside an ACTIVE window does not
page") and composes with nothing else: one row, one FK, one predicate at
evaluation time.

### 2. Suppression is a property of the SIDE EFFECT, never of the derived state

`Evaluator.evaluateRule`'s pipeline is, in order: derive the candidate
(`sustainedBreach`, E3.1) → gate on dwell (`dwellSatisfied`, E3.2) → **[new]**
gate on an active maintenance window, but only for `ok->firing` → notify →
correlate (E4.1) → `RecordTransition`. The maintenance check is inserted
after dwell and before notify — it does not participate in *deriving*
`next`, and it does not touch `sustainedBreach`, `dwellSatisfied`, or any of
E3.2's `pending_state`/`pending_since` bookkeeping. This is deliberate and is
the single most important property of this design:

- **`RecordTransition` is skipped entirely when suppressed.** `rule.LastState`
  stays exactly what it was (`ok`), and if a dwell is configured,
  `rule.PendingState`/`PendingSince` stay exactly what `dwellSatisfied`
  already persisted for this same tick (`firing`, since the moment the
  candidate first appeared) — `dwellSatisfied`'s own persisted value is never
  rewritten by the suppression check. On the *next* tick, the evaluator
  re-detects the identical `ok != firing` mismatch, re-consults the window,
  and — because `PendingState` already equals `next` — `dwellSatisfied`
  reports the dwell as already satisfied without writing again. This is what
  makes end-of-window resumption immediate rather than restarting a second
  dwell: the platform already decided this transition is ready; the window
  only withheld *acting* on that decision.
- **Recovery (`firing->ok`) is never checked at all.** The gate is
  `if next == domain.AlertRuleStateFiring`, full stop. A maintenance window
  suppresses the thing an operator explicitly declared unwanted (a page for
  planned work); it has no opinion on good news, and an operator watching a
  dashboard during a maintenance window still needs to see recovery happen.
  This is the one point at which E3.3a is asymmetric on purpose: firing is
  gated, recovery is not.
- **The metric-derived state itself (`sustainedBreach`) is completely
  unaware this feature exists.** A maintenance window is not a third
  ok/firing-like state — the reduced-concept discipline (§4) that already
  refuses a reified `Alert`/`Event`/`Signal` applies equally here: this is
  not a new state a firing can be in, it is a decision about whether a
  state's *consequence* is allowed to happen right now.

The alternative — commit the transition to `firing` but suppress only the
notify/correlate calls — was rejected. It would make `LastState` lie about
what actually happened operationally (a page an operator can see never went
out, but the row claims "firing" as if it had), and it would break end-of-
window resumption: once `LastState == firing`, the very next tick's `next ==
rule.LastState` check would see *no transition at all* and do nothing —
the condition would silently never page once its window ended, exactly the
opposite of this story's stated success criterion. Not persisting the
transition is what makes "committed but withheld" and "genuinely still
pending" indistinguishable to every other invariant in this file, which is
the property that makes the design safe rather than merely convenient.

### 3. Boundary semantics: half-open `[starts_at, ends_at)`, matching the codebase's existing convention

A window is active at `at` iff `starts_at <= at AND ends_at > at`. This is
the identical half-open convention `domain.TelemetryRepository.QueryRange`'s
`[from, to)` already uses — chosen here for the same reason: it gives a
boundary exactly one unambiguous owner. A firing at precisely `starts_at` IS
suppressed (the window has begun); a firing at precisely `ends_at` is NOT
(the window has, at that instant, already ended) — so back-to-back windows
compose cleanly with no double-covered or uncovered instant between them.

### 4. Suppression is RECORDED, never a silent drop

`maintenance_window.suppressed_count`/`last_suppressed_at` are written
atomically, in the SAME statement as the active-window read
(`postgres.MaintenanceWindowStore.Suppress`'s `SELECT ... FOR UPDATE` CTE
feeding an `UPDATE`), so a suppression is never lost to a race between two
rules firing on the same asset in the same tick. Both columns are visible
through `GET /v1/admin/maintenance-windows/{id}` — an operator can always
answer "did this window actually suppress anything, and when" without
grepping logs. They are written ONLY by the evaluator's privileged path,
never through the tenant-scoped `Create`/`Delete` surface — the same
evaluator-owned/operator-owned split `alert_rule.last_state`/
`pending_state` already draw (ADR-ALERTING-001 Decision 4).

### 5. Tenant isolation: re-derived from the firing rule's own row, exactly like every other privileged read in this package

`Evaluator.maintenanceSuppressed` sources `tenantID` from `rule.TenantID` —
never assumed, cached, or taken from anything else — the identical
non-decision ADR-TENANCY-012 already requires of `TelemetryReader` and
`IncidentCorrelator`, applied here a third time.
`postgres.MaintenanceWindowStore.Suppress` runs on the PRIVILEGED pool (row-
level security is off there by design, ADR-TENANCY-002) and therefore
carries an explicit `tenant_id = $1` predicate alongside `asset_id = $2` in
its `WHERE` clause — `internal/arch.TestPrivilegedReads_AreScopedToATenant`'s
canary would flag the read otherwise, exactly as it already does for
`TelemetryStore.QueryRange`.
`TestMaintenanceWindowStore_SuppressIsTenantIsolated` proves this live: two
tenants sharing one `asset_id` (an adversarial collision a real, globally-
unique id would not itself produce), only the tenant that actually declared
a window is ever suppressed. `MaintenanceWindowStore.Create`'s asset-
existence probe (`SELECT EXISTS(... WHERE asset_id=$1)`) is the one exempted
read on this type, for the identical reason `AlertRuleStore.Create`'s is
already exempted (a boolean gate on an RLS/FK-confined `INSERT`, discloses no
row data) — recorded in `privilegedReadExemptions`.

### 6. Contract surface: Create/List/Get/Delete, no PATCH

`domain.MaintenanceWindowRepository` has no `Update`. A window's bounds are
declared once; changing them is delete-and-recreate, the same choice
`AlertRuleRepository` already makes for `asset_id`/`metric` (its own doc
comment: "delete and recreate the rule instead"). There is no other
lifecycle beyond "exists" / "does not exist" — `Delete` is how an operator
cancels or withdraws a window, before or during it, at any time.
`suppressed_count`/`last_suppressed_at` are read-only on the DTO; there is no
route that lets a caller set or reset them.

### 7. Deferred, explicitly

- **Recurrence** ("every Sunday 02:00–04:00 UTC"). A window is a single,
  explicit `[starts_at, ends_at)` interval. Recurrence needs a scheduling
  framework this story does not build; an operator who needs a recurring
  window declares each occurrence individually until that framework exists.
- **Dependency-aware suppression.** E3.3b, unchanged, unbuilt, out of scope
  here — see Decision 1.
- **Tag/label-selector windows.** Rejected in Decision 1, not merely
  deferred: it is the wrong model even once CMDB tagging exists, for the
  blast-radius reason given there.

## Consequences

**What is now guaranteed.** A sustained breach whose asset sits inside an
ACTIVE maintenance window commits neither the notification nor the incident
create/link (`TestEvaluator_ActiveMaintenanceWindowSuppressesFiringNoIncidentNoNotify`).
A window that has expired, has not yet started, or does not name the firing
asset never suppresses
(`TestEvaluator_ExpiredOrFutureWindowDoesNotSuppress`,
`TestEvaluator_NoActiveWindowFiringProceedsNormally`). The boundary is
exactly half-open in both directions
(`TestEvaluator_WindowBoundaryIsHalfOpen`,
`TestMaintenanceWindowStore_SuppressIsActiveOnlyInsideHalfOpenBounds`). A
condition still firing once its window ends pages on the very next
evaluation tick, not "eventually"
(`TestEvaluator_EndOfWindowResumesPagingOnNextTick`). Recovery is never
suppressed, window or not
(`TestEvaluator_RecoveryIsNeverSuppressedByAnActiveWindow`). A suppression
inside an active E3.2 dwell does not corrupt or restart the dwell's own
bookkeeping — it commits immediately once the window ends, exactly at the
moment the dwell had already been satisfied
(`TestEvaluator_SuppressedFiringDoesNotCorruptDwellPending`). Tenant
isolation holds under an adversarial shared-`asset_id` collision, at both the
orchestration level (`TestEvaluator_MaintenanceWindowIsTenantIsolated`) and
the storage level against real PostgreSQL
(`TestMaintenanceWindowStore_SuppressIsTenantIsolated`). A suppression is
always visible, never a silent drop
(`suppressed_count`/`last_suppressed_at`, asserted in
`TestMaintenanceWindowStore_SuppressIsActiveOnlyInsideHalfOpenBounds` and
surfaced through the HTTP contract in
`TestMaintenanceWindows_GetSurfacesSuppressionRecord`).

**What is not claimed.** This does not suppress a downstream/dependent CI's
own alerting when only its root is under planned maintenance — that is
E3.3b, unbuilt. This does not support recurring windows — an operator must
declare each occurrence. This does not model a maintenance window as
covering more than one CI at a time — a fleet-wide change needs one window
per asset today.

## Evidence

- `internal/alerting/maintenance_suppression_test.go` — the orchestration-
  level tests named above (suppression, non-suppression, both boundaries,
  expired/future, end-of-window resumption, recovery-is-unaffected,
  cross-tenant isolation, E3.2 dwell composition, checker-error-does-not-
  persist). Mutation-verified by hand: short-circuiting the `if next ==
  domain.AlertRuleStateFiring { ... }` gate around
  `Evaluator.maintenanceSuppressed` to never run flips 6 of these tests to
  FAIL (`TestEvaluator_ActiveMaintenanceWindowSuppressesFiringNoIncidentNoNotify`,
  `TestEvaluator_WindowBoundaryIsHalfOpen`,
  `TestEvaluator_EndOfWindowResumesPagingOnNextTick`,
  `TestEvaluator_MaintenanceWindowIsTenantIsolated`,
  `TestEvaluator_SuppressedFiringDoesNotCorruptDwellPending`,
  `TestEvaluator_MaintenanceCheckErrorDoesNotPersistTransition` — the never-called
  checker never returns its error), then reverted. (Count corrected 5→6 in the
  E3.3a review.)
- `internal/store/postgres/maintenance_window_store_integration_test.go` —
  CRUD against real PostgreSQL, the ADR-ASSET-001 §6 cross-tenant/nonexistent-
  asset defense, RLS isolation on the admin surface
  (`TestMaintenanceWindowIsolation_RLSByTenant`), the exact half-open boundary
  against real SQL (`TestMaintenanceWindowStore_SuppressIsActiveOnlyInsideHalfOpenBounds`),
  and the privileged-pool tenant-isolation proof
  (`TestMaintenanceWindowStore_SuppressIsTenantIsolated`). Mutation-verified
  by hand: replacing `tenant_id = $1` with `(tenant_id = $1 OR true)` in
  `MaintenanceWindowStore.Suppress`'s `WHERE` clause makes the isolation test
  fail (tenant B suppressed by tenant A's window), then reverted.
- `internal/httpapi/handlers_maintenance_window_test.go` — authorization,
  501-until-configured, resolved-tenant (not caller-supplied), 422 on a
  non-positive window, 404 mapping for a cross-tenant/nonexistent asset and
  for a missing window, and that `suppressed_count`/`last_suppressed_at`
  round-trip through the DTO.
- `internal/kg/extract/schema.TestCorpusCensus`/`TestEveryTableIsANode`/
  `TestIndexOnClauseMaySpanLines`/`TestMultiLineAlterTableIsExtracted` —
  updated for the new table (1 table, 12 columns, 2 indexes, 5 constraints,
  0 triggers) and its inline-declared `tenant_id` (31 tenant-scoped tables,
  up from 30).
- `internal/arch.TestPrivilegedReads_AreScopedToATenant` /
  `TestPrivilegedMutations_AreScopedToAnOwner` — both pass; `Suppress`'s
  `WHERE tenant_id = $1 AND asset_id = $2` is recognised as tenant-predicated
  (not flagged), `MaintenanceWindowStore.Create`'s asset-existence probe is
  exempted for the same reason `AlertRuleStore.Create`'s already is, and
  `Suppress`'s `UPDATE` has no caller-supplied id set to key on (it is fenced
  by the CTE's own `FOR UPDATE`, not by a client-provided id list) so it adds
  no new privileged-mutation exposure.
- `internal/store/postgres/tenant_key_scope_integration_test.go` /
  `uniqueness_integration_test.go` — `maintenance_window_pkey` justified as
  server-minted (`domain.NewMaintenanceWindow`), the same shape every other
  ULID primary key on a tenant-owned table already carries.

## Enforcement

- `alerting.TestEvaluator_ActiveMaintenanceWindowSuppressesFiringNoIncidentNoNotify`
  / `..._NoActiveWindowFiringProceedsNormally` /
  `..._ExpiredOrFutureWindowDoesNotSuppress` / `..._WindowBoundaryIsHalfOpen`
  / `..._EndOfWindowResumesPagingOnNextTick` /
  `..._RecoveryIsNeverSuppressedByAnActiveWindow` /
  `..._MaintenanceWindowIsTenantIsolated` /
  `..._SuppressedFiringDoesNotCorruptDwellPending` /
  `..._MaintenanceCheckErrorDoesNotPersistTransition` — Decisions 2, 3, 5.
- `postgres.TestMaintenanceWindowStore_SuppressIsActiveOnlyInsideHalfOpenBounds`
  / `..._SuppressIsTenantIsolated` / `TestMaintenanceWindowIsolation_RLSByTenant`
  / `..._CreateRejectsCrossTenantOrNonexistentAsset` — Decisions 3, 4, 5,
  against real PostgreSQL.
- `internal/arch.TestPrivilegedReads_AreScopedToATenant` — Decision 5;
  `privilegedReadExemptions["MaintenanceWindowStore.Create"]` is the one
  justified exception.
- `internal/kg/extract/schema.TestCorpusCensus` / `TestEveryTableIsANode` /
  `TestMultiLineAlterTableIsExtracted` — the schema census stays exact.
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — Decision 6's four routes are exactly
  the published contract, no more, no less.
