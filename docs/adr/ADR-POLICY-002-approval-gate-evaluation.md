# ADR-POLICY-002 — Approval Gate evaluation: the run's own event is the subject, the reconciler is the evaluator

| | |
|---|---|
| **Status** | Accepted (W3 increment — see Scope below) |
| **Date** | 2026-08-16 |
| **Decider** | Acting CTO |
| **Related** | ADR-POLICY-001 (defines `GateSpec`/`GateState`, defers evaluation to "W3"), ADR-GOV-005 (the multi-approver quorum this gate consults, unchanged), ADR-TENANCY-003 (ownership is re-derived, never trusted from a queued row) |

## Context

ADR-POLICY-001 gave a composed run's step a `Gate` that the executor sets to
`GatePending` the instant a gated step's Action succeeds — and stopped there:
*"Gate evaluators are not implemented. `GateSpec.Type` is presently an
uninterpreted string."* A run that reaches a pending gate today pauses
forever, because nothing anywhere calls `ExecutionStore.SetGate` with
`GatePassed`. This ADR is the "W3" that ADR-POLICY-001 named but explicitly
deferred: it gives `GateSpec.Type = "approval"` meaning, by consulting the
*existing* multi-approver quorum ADR-GOV-005 already built for §8 Approval,
never a second quorum implementation.

Two real design questions had no answer yet:

1. **What does an approval-gated step wait on?** A gate is data attached to a
   *Policy definition*, but the thing an approval concerns — a governance
   object's approvals — is a property of a *run*, not of the Policy. The
   linkage has to be picked once, here.
2. **Where does evaluation run, and how does a resolved gate reach the step
   it was blocking?** ADR-POLICY-001 already built a leader-gated periodic
   sweep (`policy.Reconciler`) for a different but related job: healing runs
   whose next step failed to enqueue. A second worker would be a second
   periodic driver for the same class of problem.

## Decision

### 1. The subject is the run's own triggering Event — no per-gate config

**An approval Gate's subject is `Execution.Event.CfgID` (tenant-scoped by
`Execution.Event.TenantID`) — the governance object whose committed event
started this composed run.** `GateSpec.Config` carries nothing for the
`"approval"` type; there is no per-gate override of what to check approvals
against, and none of the required-count either.

Two shapes were live options:

- **Option A (rejected): `GateSpec.Config` names an explicit
  `governance_id`/`required` pair.** This reads naturally at first, but a
  Policy's `Steps` are defined once, in advance, for every future run that
  Policy will ever produce — a gate's `Config` would have to name a
  governance object before any run that uses it exists, which is backwards.
  Every run would need its own Policy (or its own gate `Config`, minted at
  start time by something that does not exist), reintroducing exactly the
  per-run bookkeeping ADR-POLICY-001 Option C rejected for run progress
  itself.
- **Option B (adopted): the subject is implicit — the run's own triggering
  Event.** Every run already has exactly one triggering Event, propagated
  unchanged from step to step (`advanceRunSteps` carries `pred.Event`
  forward). `Event.CfgID` is already the same identifier space
  `governance.Command.CfgID`/`approval_record.governance_id` use: an audit
  chain id that, when the triggering event happened to be (or relate to) a
  governance operation, is a real, approvable, `configuration_object`. "Run
  this action, wait for that approval, then run the next action" reads
  literally as "wait for approvals *of the object this run is about*" — the
  natural reading the founder's own request (ADR-POLICY-001 §Context) uses.

  The required count is **not** carried per-gate either: it is the tenant's
  `governance_required_approvals` Setting (ADR-GOV-005), resolved fresh on
  every evaluation via `domain.EffectiveRequiredApprovals`, the identical
  decode every other reader of that Setting uses. An operator changing the
  threshold changes it for every pending approval gate at once, exactly as
  it already changes it for every future call to `POST
  /governance/{id}/approve` — one threshold, one place it lives.

This means: **do not compose an approval gate onto a step whose run was not
triggered by (or does not name, via its Event) a real governance object you
intend to approve.** The gate will simply never resolve — which is the
correct, fail-safe behaviour (see §3), not a bug to work around with a
config field.

### 2. Evaluation folds into the existing Reconciler sweep — one periodic driver

**`policy.Reconciler.RunOnce` gains a first phase, `evaluateGates`, that runs
before its existing stalled-run scan.** No second worker, no second
`ops.RunAsLeader` closure, no second ticker.

- `evaluateGates` calls a new `ExecutionStore.ListPendingGates` (mirrors
  `ListStalledRuns`'s shape: a cheap, schema-derived query — `run_id IS NOT
  NULL AND status = 'succeeded' AND gate = 'pending'`) and, for each row,
  dispatches on the step's `GateSpec.Type`. `"approval"` consults the new
  `ApprovalGate` port; any other Type (none exist yet) is logged and left
  pending — **never guessed at**.
- The approval evaluator (`evaluateApprovalGate`) is the **only** code path
  in this platform that may call `SetGate(id, GatePassed)`. It does so on
  exactly one condition: `CountDistinct(tenantID, governanceID) >=
  RequiredApprovals(tenantID)`. Every failure mode — no subject, a failed
  count, a failed threshold lookup — returns without writing anything. A
  gate that appears to resolve without the quorum genuinely holding is the
  one failure this design exists to prevent (see Quality Bar in the
  originating brief).
- **Resumption is not this responsibility's job.** Flipping `GatePending` to
  `GatePassed` makes the very same row match `ListStalledRuns`'s existing
  candidate predicate (`gate IN ('', 'passed')` and no successor row yet) —
  so the *second* phase of the same `RunOnce` call, unmodified from
  ADR-POLICY-001, enqueues the next step. Evaluating gates first means a
  gate this very pass resolves is picked up by the stalled-run scan
  immediately after, not merely on the sweep's next tick. There remains
  exactly one place a composed run's next step is ever created
  (`advanceRunSteps`); gate evaluation reaches it *through* the row it wrote,
  never by calling it directly.

### 3. `ApprovalGate` is a new read-only port, backed by explicit tenant filtering

`internal/policy/ports.go` adds:

```go
type ApprovalGate interface {
    CountDistinct(ctx context.Context, tenantID, governanceID string) (int, error)
    RequiredApprovals(ctx context.Context, tenantID string) (int, error)
}
```

satisfied by `postgres.ApprovalGateStore`, built over the **admin pool** —
the same unscoped pool `PolicyStore`'s own background instance already uses
(`cmd/controlplane/main.go`), because the Reconciler processes every
tenant's runs, not one request's. This is *not* the same object as
`governance.ApprovalRecorder` (`postgres.ApprovalStore`), which is
transaction-scoped and runs inside the Governance Engine's own RLS-bound
connection. `ApprovalGateStore`'s two queries are the identical SQL —
`count(DISTINCT approver_user_id) FROM approval_record WHERE ... `, and the
same `domain.EffectiveRequiredApprovals` decode — made explicit-tenant
because this pool carries no RLS binding to rely on instead
(ADR-TENANCY-003): `WHERE tenant_id = $1 AND governance_id = $2`, never a
bare `governance_id = $1`. **This is a second, read-only caller of
ADR-GOV-005's data, not a second quorum implementation** — no counting logic
is duplicated, only re-scoped for a different caller.

`Reconciler.SetApprovalGate(g ApprovalGate)` wires it, using the identical
optional-port idiom ADR-GOV-005's `SetApprovalRecorder` uses — **with one
deliberate difference**: an unwired `ApprovalGate` does **not** fail the
Reconciler closed. Gate evaluation is one of two responsibilities this sweep
now carries, not its whole purpose, and a run paused on a gate this process
cannot evaluate is already sitting in the fail-safe state (`GatePending`);
`evaluateGates` simply has nothing to do and leaves every pending gate
exactly as it found it.

## What this does NOT violate

- **Vol II §5.3.** No `workflow` package, `Workflow` type, or run-progress
  table is added. Gate evaluation is a pure read (`ListPendingGates`) plus a
  single-column write (`SetGate`) on the same `policy_execution` rows
  ADR-POLICY-001 already uses; `grep -ri "type Workflow"` /
  `grep -ri "^package workflow"` remain empty.
- **ADR-GOV-005.** The quorum threshold and the distinct-approver count are
  read, never recomputed or duplicated. Approval's own transactional path
  (`governance.ApprovalRecorder`) is untouched; this ADR adds a second
  *reader*, not a second writer, of `approval_record` and
  `governance_required_approvals`.
- **ADR-TENANCY-003.** `ApprovalGateStore` never trusts a queued
  `Execution.Event`'s `TenantID`/`CfgID` as an *authorization* claim — it
  uses them only as coordinates into `approval_record`, exactly as the
  executor already treats `ex.Event.CfgID` as coordinates into the audit log
  via `domain.ResolveAndAuthorize`, not as the ownership claim itself. Every
  `ApprovalGateStore` query is explicit-tenant because the pool it runs on
  carries no RLS.
- **ADR-CONCURRENCY-002/005/006/007.** No new queue, no new claim/fence
  path. `SetGate` was already unfenced-by-claim-token in ADR-POLICY-001 (it
  runs after a step's own outcome is durably recorded); this ADR adds no new
  writer contention to it — a race between two evaluations of the same row
  converges on the same `UPDATE ... SET gate = 'passed'`.

## What is NOT claimed

- **Only `"approval"` is implemented.** A `"condition"` gate (re-evaluating a
  `Condition` against current state) or a timer-based gate are not built;
  `evaluateGate`'s `switch` leaves any other `Type` pending and logs a
  warning, by design — never defaulted to passed.
- **No self-service gate configuration.** There is still no HTTP route to
  define a composed Policy or start a run (W4, unchanged from
  ADR-POLICY-001).
- **An approval gate on a run not triggered by an approvable governance
  object never resolves.** This is fail-safe, not a defect: nothing may
  guess a subject the run's own Event does not name.

## Evidence

- `go test ./internal/policy/... -race -cover`: full suite green, including:
  - `TestGateEvaluator_ApprovalGate_PausesBelowQuorum_ThenResolvesAndResumes`
    — the load-bearing case: a 3-step run pauses at an approval gate across
    several sweeps while below quorum, then the SAME sweep that sees quorum
    met both flips the gate and enqueues the next step, and the run
    completes. Verified to bite: temporarily disabling the `evaluateGates`
    call reproduces the run staying paused forever at 2/3 executions.
  - `TestGateEvaluator_ApprovalGate_Idempotent_DoubleEvalFlipsOnce` — a
    second sweep over an already-passed gate changes nothing.
  - `TestGateEvaluator_ApprovalGate_MissingSubject_LeavesPending`,
    `TestGateEvaluator_ApprovalGate_CountError_LeavesPending`,
    `TestGateEvaluator_UnrecognisedGateType_LeavesPending` — every failure
    and non-approval path leaves the gate exactly as it found it.
  - `TestReconciler_DoesNotAdvancePastPendingGate` (pre-existing, unchanged)
    — an unwired `Reconciler` (no `SetApprovalGate` call) never advances a
    gated run, proving the fail-safe posture by construction, not merely by
    a passing assertion.
- `go test -tags=integration ./internal/store/postgres/... -race`: green,
  including
  `TestGateEvaluator_ApprovalGate_PausesBelowQuorum_ThenResolvesAndResumes_Integration`
  — the same end-to-end proof against real PostgreSQL: a real
  `configuration_object`, a real `governance_required_approvals=2` Setting
  row, real `approval_record` rows recorded through `ApprovalStore.Record`.
  Below quorum (1 of 2), three consecutive sweeps leave the gate pending; an
  explicitly *unwired* second `Reconciler` also leaves it pending even once
  quorum is met (proving the flip is the only path past the gate, not
  incidental timing); the wired sweep then flips the gate and enqueues step
  2 in one call; a repeated sweep is a no-op; one executor pass completes
  the run.
- `go test ./internal/arch/...`: green — no guard weakened.
- `grep -ri "type Workflow"` / `grep -ri "^package workflow"`: both empty.
- `make lint` / `make vet`: clean.
- No migration touched (`ListPendingGates` and `ApprovalGateStore` read
  columns ADR-POLICY-001 already added).
