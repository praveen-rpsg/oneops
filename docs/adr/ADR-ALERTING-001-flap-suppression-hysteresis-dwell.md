# ADR-ALERTING-001 — Flap suppression is a hysteresis DWELL, not a cooldown, and lives entirely on the derived-firing config row

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-04 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-012 (privileged reads require an explicit tenant predicate — E3.1's evaluator, extended here), ADR-CONCURRENCY-006 (duplicate-over-silent-loss doctrine E3.1 already applies to notify/RecordTransition, unchanged by this ADR), docs/PLATFORM-BUILD-PLAN.md §4 / Vol II §5.3 (reduced-concept discipline: no reified Alert/Event/Signal) |

## Context

E3.1's evaluator (`internal/alerting.Evaluator`) commits a transition the
instant its computed candidate (`ok`/`firing`, from `sustainedBreach` over
`ForDuration`) differs from the rule's last committed state: it notifies,
optionally correlates into an Incident (E4.1), and persists the new state, all
in the same tick. That is correct for a metric that changes once and stays
changed. It is wrong for a metric that oscillates around its threshold with a
period comparable to or longer than `ForDuration`: each half-cycle can, on its
own, uniformly breach (or not) for the whole `ForDuration` window and so
legitimately earn a "sustained" verdict — E3.1 was never wrong about any
single tick, but strung together it produces one notification, one Incident
timeline note, and one correlation write PER CROSSING. That is an alert
storm, not a signal.

E3.2 closes this without inventing a new domain concept: `AlertRule` is
already the config half of "config + derived firing" (§4 of the build plan);
this only adds one more property of how a rule's firing is derived, plus a
small amount of purely internal bookkeeping.

## Decision

### 1. Hysteresis DWELL, not a rate-limiting cooldown

Two shapes were considered for "don't act on every flip":

- **Cooldown**: after any transition, refuse another transition for N
  seconds, regardless of what the metric does in between.
- **Dwell (chosen)**: a candidate that differs from the committed state must
  be observed CONTINUOUSLY for `FlapDwellSeconds` before it is committed. A
  reversal before that resets the clock rather than committing.

A cooldown is the wrong tool: it would suppress a SECOND, wholly unrelated,
genuine event that happens to follow the first closely (e.g. recover, then
five seconds later genuinely re-breach for a different reason) — exactly the
"missed alert" this platform's other at-least-once/duplicate-over-loss
decisions (ADR-CONCURRENCY-006, ADR-TENANCY-012 §2) have consistently refused
to accept. A dwell only ever delays or collapses INSTABILITY (a candidate
that keeps reversing before it has ever been observed as stable); it never
blocks a candidate that has genuinely settled, however soon after a prior
settle. `internal/alerting.Evaluator.dwellSatisfied`'s doc comment states this
directly.

### 2. `alert_rule.flap_dwell_seconds`: a rule-level config field, not a new table

`flap_dwell_seconds` (default `domain.DefaultAlertRuleFlapDwellSeconds` = 60,
bounds `[0, 86400]`) is a column on the existing `alert_rule` row — the same
table that already carries `for_duration_seconds`. No new table, no reified
Alert/Event/Signal/"Flap" entity: the dwell is one more property of HOW a
firing is derived, exactly the discipline `for_duration_seconds` itself
already set (§4). It is patchable through `domain.AlertRulePatch` and the
existing `PATCH /v1/admin/alert-rules/{id}` route, under the row's normal
optimistic-locking rules — no new endpoint.

`0` is a valid, explicit value: it opts a specific rule out of the dwell,
reproducing E3.1's original immediate-commit behaviour exactly (no pending
write is ever made for such a rule —
`Evaluator.dwellSatisfied`'s fast path). This is a deliberate escape hatch,
not a global bypass: it requires an operator to choose it, per rule, and is
bounded (`MinAlertRuleFlapDwellSeconds == 0`) rather than a separate "disable
flap suppression" switch that could be flipped platform-wide by accident.

### 3. Severity is unaffected — it is still the rule's, not the dwell's

`AlertRule.Severity` is unchanged by this ADR: `Evaluator.notify` and
`Evaluator.correlate` still read it straight off the rule exactly as E3.1 and
E4.1 already do. The dwell governs WHEN a transition commits; it has no
opinion on WHAT severity the resulting notification/correlated-incident
carries. This keeps the reduced-concept property from §4 intact in the other
direction too: a "firing" is not becoming a bigger reified thing with its own
severity field — it is still exactly the rule's `Severity`, attached at
commit time, the same as before.

### 4. Pending bookkeeping: two nullable columns, evaluator-owned, cleared by any commit or edit

`pending_state`/`pending_since` hold the not-yet-committed candidate a rule is
currently timing (`NULL`/`NULL` — the common case — means nothing is
pending). They are:

- **Written only by the evaluator's background path**
  (`alerting.Store.RecordPending`), never through the operator PATCH path —
  the identical split `last_state`/`last_transition_at`/`current_incident_id`
  already draw (`domain.AlertRulePatch`'s doc comment).
- **Cleared atomically by `RecordTransition`** the instant a candidate is
  actually committed: a settled rule never carries a stale pending value.
- **Cleared unconditionally by `AlertRuleRepository.Update`**, even a patch
  that only changes an unrelated field (or `flap_dwell_seconds` itself). This
  is the answer to "what happens if the dwell config changes mid-flight": a
  partially-elapsed dwell measured against the OLD comparator/threshold/dwell
  is discarded, not silently re-interpreted under a new one. The next tick
  starts a fresh measurement under whatever config now applies. The
  alternative — keep counting under the new config — was rejected because it
  lets an operator's edit silently inherit timing from a configuration that
  no longer exists, which is a worse surprise than a slightly later
  transition.
- **Not exposed in the HTTP contract.** `flap_dwell_seconds` (the config
  knob) is part of `AlertRule`/`CreateAlertRuleRequest`/
  `PatchAlertRuleRequest`; `pending_state`/`pending_since` are not. They are
  purely internal evaluator bookkeeping about a decision in progress, not
  something an operator administers or needs to observe to operate the
  system — keeping them out of the contract is itself part of the
  reduced-concept discipline (§4): the dwell's IN-FLIGHT STATE never becomes
  a second thing an API consumer has to reason about.

### 5. Restart / leader failover: the dwell clock lives in the row, not in the process

Because `pending_state`/`pending_since` are persisted (tenant-scoped,
RLS-protected `alert_rule`, written only through the tenant-scoped `appPool`
or the privileged pool's `alerting.Store`, exactly like every other E3.1/E4.1
write), a crash, redeploy, or leader failover loses nothing: the next
evaluator instance — sharing no in-memory state with the one that died — reads
`EnabledRules` and finds the same candidate at the same `PendingSince` a
fresh leader would need to continue timing correctly.
`TestEvaluator_FlapSuppression_PendingSurvivesNewEvaluatorInstance` proves
this directly at the orchestration level (two independent `*Evaluator`
values, one store); `TestAlertRuleStore_RecordPendingIsFencedAndClearedByRecordTransition`
proves the same fact at the storage level (a fresh `EnabledRules` read after
`RecordPending` returns the persisted candidate unchanged).

### 6. Edge cases

- **Exactly at the threshold.** Unaffected by this ADR: `AlertComparator.
  Breached` (`gt`/`lt` strict, `gte`/`lte` inclusive) decides the instant
  candidate exactly as E3.1 already does; the dwell only gates what happens
  to a candidate AFTER it is computed.
- **Config changes mid-flight.** Covered by Decision 4: any edit clears
  in-flight pending state; the dwell measurement restarts under the new
  config on the next tick. A rule is never held to a dwell target it can no
  longer see.
- **Leader failover.** Covered by Decision 5: the dwell clock is data, not
  process state. **Clock skew across leaders.** `PendingSince` is written with
  one leader's wall clock and, after failover, compared against a *different*
  leader's wall clock. Dwell precision is therefore bounded by inter-node clock
  skew: a rule with `flap_dwell_seconds` near the skew magnitude may commit
  slightly early or late across a failover. This is an accuracy bound, not a
  correctness defect (no state is lost or double-committed); operators who need
  tight dwell precision must keep cluster clocks synchronized (NTP/PTP), the
  same assumption every wall-clock dwell carries.
- **Operator PATCH on an actively-flapping rule.** Each dwell start/reset bumps
  the rule's `row_version` (the CAS token). A rule that is flapping fast can
  therefore repeatedly `409 Conflict` an operator's optimistic-locked PATCH
  landing inside a tick window. This is a pre-existing property — `RecordTransition`
  already bumped `row_version` — that this ADR only makes more frequent for
  flapping rules; the operator simply re-reads and retries. Not a correctness
  issue, recorded here for honesty.
- **First deploy over pre-existing E3.1 rules.** The migration backfills
  `flap_dwell_seconds = 60` on every existing row (a deliberate secure-default:
  `0` would leave pre-existing rules unprotected). Consequence: on first deploy,
  E3.1-era rules — including any with an in-flight recovery — gain up to a 60s
  commit delay. Defensible and intended, but operators running live rules
  should be told to expect it. Set a rule's dwell to `0` to restore exact
  E3.1 immediacy.
- **A rule whose dwell equals or exceeds its evaluation interval.** The
  commit can land up to one tick late relative to the exact dwell boundary
  (this package evaluates on a fixed `Interval`, default 30s — see
  `Config`'s doc comment) — a bounded latency, not a correctness defect; an
  operator who needs sub-interval precision should set a dwell smaller than
  the interval or accept the same rounding `ForDuration` itself already has.
- **What this is NOT.** This is threshold hysteresis (require continuous
  stability before acting), not a flap-PERCENTAGE score like Nagios's
  state-change-frequency detector, and not dependency/maintenance-window
  suppression (E3.3, explicitly out of scope here — a different, topology-
  aware kind of suppression that this ADR does not attempt).

## Consequences

**What is now guaranteed.** A rule whose candidate oscillates within its
configured dwell produces at most one eventual transition, never one per
crossing (`TestEvaluator_FlapSuppression_OscillationCollapsesToOneTransition`).
A rule whose candidate genuinely, continuously changes still transitions
promptly — exactly at its own dwell boundary, never indefinitely suppressed
(`TestEvaluator_FlapSuppression_SustainedChangeStillTransitionsPromptly`,
which proves both the firing and the recovery direction independently). The
dwell clock survives a restart or leader failover
(`TestEvaluator_FlapSuppression_PendingSurvivesNewEvaluatorInstance`,
`TestAlertRuleStore_RecordPendingIsFencedAndClearedByRecordTransition`).

**What is not claimed.** This does not eliminate every kind of alert noise —
E3.3 (maintenance windows, dependency-aware suppression using the CMDB graph)
remains a distinct, unbuilt kind of suppression this ADR does not attempt. A
dwell set too short for a genuinely noisy metric will still flap; the config
is a tool an operator must tune, not a guarantee that any given rule is
noise-free.

## Evidence

- `internal/alerting/flap_suppression_test.go` — the four orchestration-level
  tests named above, unit-tested with a controllable clock (every pre-E3.2
  test in this package uses one fixed `now`; flap suppression is entirely
  about behaviour across elapsed time, so these tests advance it explicitly).
  Mutation-verified by hand: short-circuiting `dwellSatisfied` to always
  return `true` (reverting to E3.1's immediate-commit behaviour) fails 3 of
  the 4 new tests, proving they are not vacuous.
- `internal/store/postgres/alert_rule_store_integration_test.go` —
  `TestAlertRuleStore_CreateDefaultsFlapDwellAndUpdatePatchesOrClearsPending`
  (default, patchability, and Update's unconditional pending-clear) and
  `TestAlertRuleStore_RecordPendingIsFencedAndClearedByRecordTransition`
  (fencing, persistence-survives-a-fresh-read, and RecordTransition's atomic
  clear), against real PostgreSQL.
- Every pre-existing E3.1/E4.1 test in `internal/alerting` passes unchanged
  with `FlapDwellSeconds = 0` (this package's `mkRule` test helper's own
  default, chosen so none of them had to be rewritten to reason about
  elapsed time they were never testing).
- `internal/kg/extract/schema.TestCorpusCensus` updated for the 3 new
  columns / 3 new constraints this migration adds to the existing
  `alert_rule` table (no new table — `TestEveryTableIsANode`'s table list is
  unchanged).

## Enforcement

- `alerting.TestEvaluator_FlapSuppression_OscillationCollapsesToOneTransition` /
  `..._SustainedChangeStillTransitionsPromptly` /
  `..._PendingSurvivesNewEvaluatorInstance` /
  `..._ZeroDwellCommitsImmediately` — Decisions 1, 2 and 5.
- `postgres.TestAlertRuleStore_CreateDefaultsFlapDwellAndUpdatePatchesOrClearsPending` /
  `..._RecordPendingIsFencedAndClearedByRecordTransition` — Decisions 2 and 4,
  against real PostgreSQL.
- `internal/arch.TestPrivilegedReads_AreScopedToATenant` — unchanged by this
  ADR (no new privileged `SELECT` filtered by `asset_id` was added; the new
  store methods are keyed on `rule_id`, the rule's primary key).
- `internal/kg/extract/schema.TestCorpusCensus` / `TestEveryTableIsANode` —
  the schema census stays exact.
