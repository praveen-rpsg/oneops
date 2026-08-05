# ADR-ALERTING-003 — Dependency-aware suppression suppresses collateral firings, not root causes

- **Status:** Accepted
- **Date:** 2026-08-05
- **Story:** E3.3b (the second half of the split E3.3; E3.3a = ADR-ALERTING-002, maintenance windows)
- **Supersedes / amends:** none. Composes with ADR-ALERTING-001 (flap dwell),
  ADR-ALERTING-002 (maintenance windows), ADR-TENANCY-003 (ownership
  re-derivation), ADR-TENANCY-012 (privileged reads require an explicit tenant
  predicate), ADR-ASSET-001 (CMDB asset + relationship model).

## Context

By E3.3a the alerting pipeline detects a breach, dampens flapping (E3.2), and —
unless an operator has declared a maintenance window — creates/links an incident
and notifies (E4.1). But when a *root* CI fails, every CI that depends on it
tends to breach at the same moment: a database goes down and every service that
reads it starts erroring. Paging on all of them buries the one page that
matters. This is the classic NOC symptom-storm.

The CMDB already models the topology: `asset_relationship`
(`from_asset_id → to_asset_id`, `type ∈ {depends_on, runs_on, connected_to,
member_of}`) plus a tenant-scoped, cycle-safe recursive-CTE traversal
(`AssetGraphRepo`, `internal/graph`). The alert-rule model already records a
per-rule firing state (`alert_rule.last_state`). E3.3b composes these two facts.

A hard constraint shaped the whole design: **there is no symptom-class on alert
rules.** `AlertRule` has `Severity` (a paging label) but nothing that classifies
*what kind* of condition a rule detects (reachability vs. a resource metric like
disk/cpu). So the platform cannot, today, say "suppress only the *reachability*
alerts on a downstream CI while leaving its independent *disk-full* alert alone."
That absence is the source of this ADR's Honest Bound, below.

## Decision

**1. The "down" signal.** A CI `Y` is *down* iff it has ≥1 `alert_rule` with
`enabled = true AND last_state = 'firing'`. This reuses what exists (no new
health-aggregate table), and is a cheap indexed lookup (`ix_alert_rule_asset`).
It is implemented as a **plain privileged `SELECT`** (not hidden inside a
write-CTE) so `arch.TestPrivilegedReads_AreScopedToATenant` actively covers it,
and it carries an explicit `tenant_id = $` predicate per ADR-TENANCY-012.

**2. The dependency set + direction.** When rule `R` would transition
`ok→firing` on asset `X`, we walk `X`'s dependencies — the CIs `X` *needs* —
via the existing tenant-scoped typed traversal (`DirectionDependencies`,
`from = X`), filtered to the edge types that mean "X needs Y":
`depends_on` and `runs_on`. `connected_to` and `member_of` are peer/grouping
relations, not "needs", and are excluded. **Direction is load-bearing and
easy to get backwards:** we suppress `X` when a CI `X depends on` is down, never
when a CI that *depends on `X`* is down. A reversed traversal would silence the
root cause and page the symptoms — the exact inversion of the goal. This is
pinned by an explicit both-directions test at the store layer
(`TestDependencySuppressionStore_DirectionCorrectness`) and the tenant-isolation
tests at both layers.

**3. Cycle & depth safety.** The traversal is cycle-safe by construction (the
recursive CTE dedupes visited nodes). A configurable **max depth** (default 10)
is a defensive performance cap, documented, not a correctness mechanism.
`TestDependencySuppressionStore_CycleSafety` proves an `X↔Y` `depends_on` cycle
terminates and still finds the down dependency.

**4. Suppression semantic — mirror E3.3a exactly: suppress the side effect, not
the state.** At the same hook site as the maintenance check (after E3.2's
`dwellSatisfied`, before notify/correlate/RecordTransition, and only for
`next == firing`): if any dependency is down, `evaluateRule` **returns without
calling `RecordTransition`** — no incident, no notification, and crucially
`rule.LastState` stays `ok` (and any dwell candidate stays exactly as E3.2
persisted it). This is deliberately *not* "commit but suppress the page":
leaving the transition uncommitted is what makes suppression **self-clearing**.
The moment the root recovers (the checker stops reporting a down dependency), the
still-breaching `X` re-enters `ok→firing` on the very next tick and pages
normally — no "eventually", no manual un-suppress. Recovery (`firing→ok`) is
never dependency-suppressed; good news is never withheld (the checker is not even
consulted for it).

**5. Precedence with maintenance windows (E3.3a).** Maintenance-window
suppression and dependency suppression are independent reasons; either
suppresses. The **maintenance window is checked first** and short-circuits — an
operator's explicit declaration is what the metric/log trail attributes the
suppression to, over an inferred topology fact. Pinned by
`TestEvaluator_MaintenanceWindowPrecedesDependencyCheck` (when both apply, the
dependency checker is not consulted).

**6. Recording — never a silent drop.** Every dependency suppression is recorded
in the tenant-owned `dependency_suppression` table (FORCE RLS + fail-closed
policy, in `TenantOwnedTables`), keyed `(tenant_id, affected_asset_id,
root_asset_id)` with `suppressed_count` + `last_suppressed_at`, written
**atomically in the same privileged statement** as the check (upsert-increment,
not a second row). It names the *root* CI that caused the suppression, so an
operator can always see "X's alerts are being suppressed because Y is down"
rather than wondering why X went quiet.

**7. Tenancy.** The down-check is privileged (RLS-bypassing pool, like the rest
of the evaluator) and therefore carries an explicit `tenant_id` predicate,
mutation-verified to bite; the graph traversal runs on the RLS-scoped pool and
is never privilege-bypassed. Tenant is always re-derived from `rule.TenantID`
(ADR-TENANCY-003), never a shared/queue label; a root `Y` must belong to the
same tenant.

## Honest Bound (the accepted limitation)

Because **no symptom-class exists** (see Context), suppression is
**all-or-nothing per asset**: *any* firing on `X` is suppressed if *any*
dependency of `X` is down — including a genuinely independent failure on `X`
(e.g. `X`'s disk fills while its database dependency happens to be down). That
independent failure is masked until the dependency recovers. This is a real,
known tradeoff, accepted for E3.3b and mitigated by three properties, not
hand-waved:

- **Visible recording** — the suppression is queryable and names the root, so it
  is *suppressed*, not *hidden*.
- **Self-clearing** — the mask lifts automatically the instant the dependency
  recovers; nothing stays silenced longer than the root outage.
- **Bounded** — it only applies while a dependency is *actually* firing, and
  only along `depends_on`/`runs_on` edges.

**Named future refinement (NOT built here):** add a rule symptom-class
(e.g. `availability`/`reachability` vs `resource`) so dependency suppression can
fire only on reachability-class symptoms of a downstream CI and never mask an
independent resource failure. That is a modeling decision in its own right
(what the classes are; whether a rule is classified explicitly or inferred from
its metric) and is deliberately deferred to a future story, not bolted on here.

## Consequences

**Guaranteed.** A collateral firing on a CI whose dependency is down does not
page or open an incident (`TestEvaluator_DependencyDownSuppresses…`,
`TestDependencySuppressionStore_SuppressesWhenADependencyIsDown`); it self-clears
the instant the root recovers (`TestEvaluator_DependencySelfClearsWhenRootRecovers`);
it never crosses tenants (`…IsTenantIsolated` / `…DownCheckIsTenantIsolated`,
both mutation-verified); it never corrupts E3.2's dwell bookkeeping
(`…SuppressedFiringDoesNotCorruptDwellPending`); recovery is never suppressed;
a checker error is a retry, never a silent loss
(`…DependencyCheckErrorDoesNotPersistTransition`); and it composes with E3.3a
with maintenance-first precedence.

**Not claimed.** No incident grouping / root-cause suggestion (E4.2). No symptom
-class scoping (see Honest Bound). No suppression of a firing whose dependency is
merely `connected_to`/`member_of` (`…ConnectedToAndMemberOfAreExcluded`).

## Evidence

- **Evaluator unit (mutation-verified):** `internal/alerting/dependency_suppression_test.go`
  — 9 tests (suppress + record, non-suppress consulted, self-clearing, recovery
  never suppressed, tenant isolation, E3.2 dwell composition, checker-error
  retry, maintenance precedence, non-vacuity). Defeating the `dependencySuppressed`
  gate in `evaluateRule` flips 4 of them to FAIL (`DependencyDownSuppresses…`,
  `DependencySelfClears…`, `DependencySuppressionIsTenantIsolated`,
  `DependencySuppressedFiringDoesNotCorruptDwellPending`); `MaintenanceWindow
  PrecedesDependencyCheck` correctly still passes (maintenance short-circuits
  ahead of the defeated gate). Reverted clean.
- **Store integration (real PostgreSQL):** `internal/store/postgres/dependency_suppression_store_integration_test.go`
  — basic suppression + atomic upsert recording, no-down, **both-direction
  correctness**, `connected_to`/`member_of` exclusion, cycle safety, and
  cross-tenant down-check isolation via the FK-bypasses-RLS shape. Removing
  `tenant_id = $1` from the down-check makes the isolation test fail.
- Full suite (race), arch guards (both privileged guards), PKG census, lint,
  `migrate-validate`, and `contract-breaking` — all green at merge (see the
  merge commit).
