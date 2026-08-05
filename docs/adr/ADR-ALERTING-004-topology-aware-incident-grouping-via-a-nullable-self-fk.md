# ADR-ALERTING-004 — Topology-aware incident grouping via a nullable self-FK, not a reified Group

- **Status:** Accepted
- **Date:** 2026-08-05
- **Story:** E4.2 (noise reduction / grouping / root-cause candidate suggestion — heuristic first, ML later in E13)
- **Supersedes / amends:** none. Composes with ADR-ALERTING-003 (dependency-aware
  suppression — see "Complementarity with E3.3b" below), ADR-ALERTING-001 (flap
  dwell), ADR-TENANCY-001/003/012 (isolation, ownership re-derivation, privileged
  reads require an explicit tenant predicate), ADR-ASSET-001 (CMDB asset +
  relationship model), and E4.1/E5.1 (alert-correlation, incident lifecycle).

## Context

By E4.1 an alert-rule firing correlates into an `Incident`
(`alert_rule.current_incident_id`, find-or-create per asset). By E3.3b, a
collateral firing on a CI whose dependency is *already* down is suppressed
before it ever becomes a page. But suppression is a **point-in-time** decision
made once, at the `ok→firing` transition: it does not cover a root detected
*after* its collateral already opened an incident, a root's own rule firing
*later* than a dependent's, or a suppression window that simply did not apply
(e.g. the dependent's rule was already firing before the root went down). In
every one of those cases the platform ends up with N+1 separate open incidents
for what is operationally one outage, and nothing links them.

The CMDB topology (`asset_relationship`, `depends_on`/`runs_on`,
`AssetGraphRepo`/`internal/graph`'s cycle-safe recursive-CTE traversal) and the
open-incident set (`incident`, `source = 'alert'`) already carry everything
needed to group after the fact. E4.2 composes them into a periodic
reconciliation pass, the same shape E3.3b composes the topology and the
firing-state signal into a point-in-time check.

The same hard constraint from ADR-ALERTING-003 still applies: **there is no
symptom-class on alert rules.** E3.4 (founder-approved, not yet built) is
named there as the future primitive that would let both suppression and this
feature reason about *what kind* of condition is firing, not just severity.
E4.2 does not wait for it — see the Honest Bound below.

## Decision

**1. No reified Group/Correlation/Alert/Event noun.** The entire mechanism is
one nullable self-referencing column: `incident.root_incident_id`. `NULL`
means "this incident is itself a root, or standalone." A non-`NULL` value
names the `incident_id` of the OPEN, alert-sourced incident this one is
grouped under. A "group," as an operator sees it, is **derived** — every
incident sharing a `root_incident_id`, plus that root — **never a stored
membership row.** This is the identical reduced-concept treatment
`alert_rule.current_incident_id` (E4.1) already gets, mirrored here for the
grouping relationship instead of the correlation one (docs/PLATFORM-BUILD-PLAN.md
§4, Vol II §5.3).

**2. The root heuristic (heuristic first, per the story; ML is E13).** For an
open, alert-sourced incident on asset `X`: walk `X`'s transitive dependency
closure — the CIs `X` *needs* — via the same edge-type filter and direction
ADR-ALERTING-003 §2 already uses (`depends_on`/`runs_on` only, cycle-safe
recursive CTE, `RecursiveDependenciesOfTypes`). Among the nodes in that
closure that are themselves **down** (have their own open, alert-sourced
incident — the identical "down" definition ADR-ALERTING-003 §1 uses, restated
per-tenant against the current open-incident set rather than `alert_rule.
last_state` directly, since grouping reasons about incidents, not rules), the
**deepest one wins**; ties break toward the lexicographically-smaller
`asset_id`. No down dependency in the closure ⇒ `root_incident_id = NULL` (`X`
is a root or standalone).

Because `RecursiveDependenciesOfTypes(X)` already returns `X`'s FULL transitive
closure (not merely its direct dependencies), picking the deepest down node
produces **flat** grouping directly: if `X` depends on `A` which depends on
`DB`, and both `A` and `DB` are down, `DB` (the deepest) is a candidate in
`X`'s own closure already — `X` is never first pointed at `A` and re-pointed at
`DB` in a second pass. Direction is load-bearing and easy to invert, exactly as
ADR-ALERTING-003 §2 warns: a CI that merely **depends on** `X` being down must
never make it `X`'s root — that would require walking `X`'s *dependents*, which
this feature never does.

**3. A separate, leader-gated periodic reconciliation pass — not a hook inside
`alerting.Evaluator.correlate`.** Unlike E3.3b's point-in-time suppression
check, grouping must reconsider the CURRENT full set of open alert-incidents
per tenant on every pass, because a root can appear or resolve independently
of any single rule's own transition. `internal/grouping.Reconciler` mirrors
`alerting.Evaluator`'s own shape (`Config`/`Store` port/`Run`/`RunOnce`),
sweeping every tenant with at least one open alert-sourced incident, on a
default 60s interval — longer than the evaluator's 30s, because grouping's
payoff is noise **reduction** on an already-open incident, not a page an
operator is waiting on.

**4. Self-healing, idempotent, and cycle-safe by construction, not by
locking.** Each pass recomputes every incident's candidate root independently,
purely from the CURRENT down-set and topology; a value is written only when it
differs from what is stored. A resolved root's former children are
automatically re-evaluated on the very next pass — re-rooted to a still-down
deeper dependency, or cleared to `NULL` — with **no special-cased "resolve"
logic**, only the same computation re-run against updated input. Because
`candidate[incidentID]` is computed independently from each incident's OWN full
transitive closure, it can never legitimately chain through another incident's
candidate — a pointer cycle can only arise from a genuine cycle in the
underlying CMDB dependency graph itself (two currently-down assets each
depending on the other). `resolveFinalRoots` detects that case and breaks it
deterministically by anchoring the lexicographically-smallest `incidentID` in
the cycle to `NULL` — itself self-healing and idempotent (recomputed
identically every pass from current state, never a stored decision). A
same-row self-pointer is additionally refused at the schema level
(`ck_incident_root_not_self`) as a last line of defense, though it is
structurally impossible to produce from the algorithm.

**5. Grouping never mutates incident STATE.** The reconciler's only write is
`grouping.Store.SetRootIncidentID` — a method whose signature cannot express a
status change, an acknowledgement, a resolution, or a close. It writes
`root_incident_id` (and bumps `row_version`/`updated_at`, the same "any write
invalidates a stale optimistic-lock read" discipline every other column change
on this table already gets) and nothing else. There is no HTTP write path for
this column at all — the same evaluator-only write discipline
`alert_rule.last_state` already has.

**6. Tenancy.** `internal/grouping.Store` (`*postgres.IncidentGroupingStore`)
is built over the PRIVILEGED pool — the identical dual-role split
`AlertRuleStore`/`IncidentStore` already draw between their admin-CRUD and
evaluator/background-worker surfaces — so every read and write carries an
explicit `tenant_id` predicate sourced from the caller's own argument, never
assumed from RLS (ADR-TENANCY-012), mutation-verified to bite on both the read
(`OpenAlertIncidents`) and the write (`SetRootIncidentID`). The dependency walk
itself runs on the TENANT-SCOPED pool with `domain.WithTenant` bound per
tenant, per reconciliation pass — never the privileged one — the identical
non-decision `DependencySuppressionStore.Suppress`'s own doc comment already
requires of E3.3b's walk (ADR-TENANCY-001): bypassing RLS there would let a
firing on one tenant's asset be "explained away" by a dependency edge that in
fact belongs to a different tenant merely sharing the same `asset_id`.

**7. Additive read-only DTO projection.** `incidentDTO` gains
`root_incident_id` (`omitempty`, absent for a root/standalone incident — the
JSON shape for every pre-E4.2 incident is unchanged) and `is_root` (always
present; `true` iff `root_incident_id` is absent). Both are read-only: there is
no field on `createIncidentRequest`/`incidentPatchRequest` for either, and none
is planned — the only writer is the reconciler. No new group-management API
(list-a-group, merge, split) is introduced; a caller derives "the group" by
listing incidents that share a `root_incident_id`.

## Complementarity with E3.3b (ADR-ALERTING-003)

These are two different mechanisms solving two different moments of the same
storm, and both are needed:

- **E3.3b (suppression)** prevents a NEW collateral page from ever being
  created, at the instant of the `ok→firing` transition, when its root is
  *already* firing.
- **E4.2 (grouping)** organizes the incidents that DO get created anyway — a
  root detected after its collateral, a root's rule firing later than a
  dependent's, a suppression window that did not apply — so an operator still
  sees one root incident with N grouped collateral, not N+1 equal pages.

E4.2 does not replace E3.3b's suppression, and E3.3b's suppression does not
make E4.2 redundant: a platform running only E3.3b still accumulates ungrouped
incidents whenever suppression's point-in-time window is missed; a platform
running only E4.2 still pages on every collateral firing before grouping it
after the fact. Both share the identical edge-type filter, direction, and
cycle-safety property by construction (E4.2 restates rather than imports
E3.3b's constants, since the two packages have no dependency on each other) —
a future refactor collapsing them into one shared traversal helper is possible
but out of scope here.

## Honest Bound (the accepted limitations)

- **Single-root pick, not a ranked set.** An incident has AT MOST one root.
  When a genuine CMDB cycle makes two candidates equally valid, the
  tie-break is a deterministic, arbitrary anchor (lexicographically-smallest
  `incidentID`), not a judgment that one is "more root" than the other — see
  Decision §4.
- **Heuristic, not ML.** "Deepest down dependency" is a topology-shape
  heuristic (ADR-ALERTING-004 §2), not a probabilistic root-cause ranking.
  E13 is where a learned model, if ever built, would replace or augment this.
- **A resolved CHILD's root pointer is frozen, by design.** The reconciler acts
  only on OPEN alert-incidents; once a collateral incident resolves it leaves
  the working set, so its `root_incident_id` is never re-evaluated or cleared
  and its historical record (and DTO) keeps showing the group it belonged to at
  resolution. This is deliberate — the grouping is a true historical fact about
  that outage, not live state — but it is a decision, not a surprise: a resolved
  incident's `is_root`/`root_incident_id` reflect the topology at its resolution,
  not "now". (Contrast the resolved-ROOT case, which DOES re-heal its still-open
  children on the next pass — Consequences below.)
- **A silently-dead root with no open incident of its own is not recognized as
  a root at all.** "Down" is defined exactly as ADR-ALERTING-003 §1 defines it:
  an asset with its own open, alert-sourced incident. An asset that is
  genuinely broken but has no `alert_rule` configured on it, or whose rule
  never fired, is invisible to this heuristic — its dependents simply show no
  root, each standing alone. This is the same limitation class E3.3b's own
  down-check accepts, restated here for the read side.
- **No symptom-class exists** (Context, shared with ADR-ALERTING-003): grouping
  cannot distinguish "X is down because of the same reachability failure as its
  dependency" from "X has its own, unrelated resource failure that happens to
  coincide with a dependency also being down." Both group identically today.
  E3.4 (founder-approved, not yet built) is the named future primitive for
  symptom-class-scoped refinement of both this feature and E3.3b.
- **Eventually consistent, not instant.** Grouping is a periodic sweep
  (default 60s), not a hook on the firing path — an operator may see an
  ungrouped incident for up to one interval after its true root is detected.
  This is a deliberate tradeoff (Decision §3), not an oversight: the payoff is
  noise reduction on an already-open incident, not a page an operator is
  waiting on.
- **Max-depth bound (default 10, `DefaultMaxDepth`)** is the identical
  defensive performance cap ADR-ALERTING-003 §3 already documents for its own
  walk — a real CMDB dependency chain deeper than this is vanishingly
  unlikely; the cap bounds pathological work, not correctness.

## Consequences

**Guaranteed.** Collateral incidents under a common down root are grouped flat
under the deepest one, never chained through an intermediate
(`TestReconciler_GroupsCollateralUnderDeepestRoot`, `TestDeepestDownRoot_
PicksDeepestAmongDownCandidates`); direction is correct in both senses — a
dependency's own incident can root a dependent, never the reverse
(`TestReconciler_DirectionCorrectness`, mutation-provable via
`TestDeepestDownRoot_ExcludesOwnAssetID`'s depth-rigged case); a resolved root's
children re-root or clear on the very next pass, and a further pass against
unchanged state writes nothing (`TestReconciler_SelfHealingAndIdempotent`);
a genuine CMDB dependency cycle never produces a stored pointer cycle or
self-pointer (`TestReconciler_NoSelfOrCyclicRootPointer`,
`TestResolveFinalRoots_TwoCycleBrokenAtLexicographicallySmallest`,
`TestResolveFinalRoots_SelfPointerNeverPersisted`); grouping never crosses
tenants even when two tenants share identical `asset_id` strings with
different topologies (`TestReconciler_TenantsAreIndependentEvenWithSharedAssetIDs`,
`TestIncidentGroupingStore_OpenAlertIncidentsIsTenantIsolated`,
`TestIncidentGroupingStore_SetRootIncidentIDIsTenantIsolated`, both
store-level tests mutation-verified); grouping touches ONLY `root_incident_id`
— status, `acknowledged_at`/`resolved_at`/`closed_at`, and every other column
are provably untouched by the same write
(`TestIncidentGroupingStore_RoundTrip`'s status/row_version assertions,
`TestReconciler_NeverTouchesAnythingButRootIncidentID`); a dependency-walk
error leaves the stored value untouched and retries next pass
(`TestReconciler_GraphErrorLeavesStoredValueUntouched`); a resolved/closed or
manually-filed incident is never a grouping candidate on either side of the
relationship (`TestIncidentGroupingStore_ExcludesResolvedAndClosedIncidents`,
`TestIncidentGroupingStore_ExcludesManualIncidents`).

**Not claimed.** No ranked/multi-root suggestion, no ML (E13). No
symptom-class-scoped precision (E3.4, not yet built — see Honest Bound). No
new group-management API — a group is read by filtering on
`root_incident_id`, never listed as its own resource. No instant
(sub-reconciliation-interval) grouping. No recognition of a "down" dependency
that has no alert-rule-sourced incident of its own.

## Evidence

- **Algorithm unit (pure functions, mutation-verified):**
  `internal/grouping/algorithm_test.go` — deepest-selection, lexicographic tie
  break, own-asset-id exclusion (rigged so removing the guard flips the test),
  non-down nodes ignored, empty-closure/no-down-dependency ⇒ nil, and
  `resolveFinalRoots`'s no-cycle/self-pointer/two-cycle/chain-into-cycle/
  determinism cases. Confirmed live: replacing `n.Depth > bestDepth` with `>=`
  flips the tie-break test; deleting the `n.CfgID == ownAssetID` guard flips
  the own-asset-id-exclusion test. Both reverted clean.
- **Reconciler unit (fakes, mutation-provable):**
  `internal/grouping/reconciler_test.go` — grouping bites + deepest-root
  selection (combined fixture), direction correctness (both directions), self-
  healing + idempotence (combined fixture: root resolves ⇒ re-root, unchanged
  re-run ⇒ zero writes), no self/cyclic pointer against a real cyclic
  topology, tenant independence with shared `asset_id` strings across two
  tenants, walk-error retry safety, and the structural "only
  `SetRootIncidentID`" state-mutation proof.
- **Store integration (real PostgreSQL):**
  `internal/store/postgres/incident_grouping_store_integration_test.go` —
  round-trip (including the status/row_version non-mutation proof), excludes
  resolved/closed, excludes manual-sourced incidents, tenant isolation on both
  `OpenAlertIncidents` (read) and `SetRootIncidentID` (write), and FK
  enforcement against an unknown root. Both tenant-isolation tests
  mutation-verified live: replacing `tenant_id = $1`/`tenant_id = $2` with an
  always-true, type-pinned predicate (`$N::text = $N::text`) makes each fail
  with a real cross-tenant read/write; both reverted clean.
- Full unit suite (race) and full integration suite (race, real DB) green;
  both privileged arch guards (`TestPrivilegedReads_AreScopedToATenant`,
  `TestPrivilegedMutations_AreScopedToAnOwner`) pass with `IncidentGroupingStore`
  swept and no new exemption required; `TestEveryTenantScopedUniqueKey_
  IsTenantScoped`/`TestUniquenessIsScopedToTenant` pass unchanged (no new
  table, no new unique key); PKG schema census
  (`internal/kg/extract/schema`) updated for the one new column + two new
  constraints + one new index and green; `make lint` clean; `make
  migrate-hash`/`migrate-validate` clean; `make contract-breaking` clean (the
  DTO addition is additive-only).
