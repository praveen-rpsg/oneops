# ADR-POLICY-001 — Action-Decision composition delivers the reduced Workflow concept

| | |
|---|---|
| **Status** | Accepted (foundation increment — see Scope below) |
| **Date** | 2026-08-15 |
| **Decider** | Acting CTO |
| **Related** | Vol II §5.3 (reduces "Workflow" to *"composition of Actions & Decisions over Time"*), ADR-CP6 (rejected "Workflow/Execution owns descriptive Lifecycle" as an option — *"no workflow engine is present"*), ADR-GOV-005 (the Decision/quorum primitive this composition's Gate will consult), ADR-CONCURRENCY-002/005/006/007 (the claim/fence/retry machinery this composition reuses unchanged), ADR-TENANCY-001/002/009 |

## Context

The founder wants production-grade, multi-step, decision-gated process
automation: "run this action, wait for that approval, then run the next
action." Every conventional name for this — workflow, pipeline, orchestration
engine — is constitutionally unavailable. Vol II §5.3 is explicit and was
already tested against a real option analysis: ADR-CP6 rejected "Execution/
Workflow owns descriptive Lifecycle" as an option because *"no workflow
engine is present — NATS and Temporal were never adopted (ADR-ARCH-002)"* and
naming that owner would be "speculative architecture." The Engineering
Implementation Guide states the rule as code: *"No `workflow` package, no
`Workflow` entity. Compose Actions and Decisions."*

This platform already has both primitives, live and load-bearing:

- **Action** (`internal/policy`) — a `Policy` matches a committed event and
  runs one `ActionSpec` through a `Registry`, tracked by an `Execution` row
  with claim/fence/retry/dead-letter semantics (ADR-CONCURRENCY-002/005/006/007).
- **Decision** — the Governance Engine's multi-approver Approval quorum
  (ADR-GOV-005): an I/O-dependent precondition, evaluated inside a locked
  transaction, that gates whether a state transition proceeds.

Nothing today lets a Policy's response be more than one Action, and nothing
lets a Decision pause and later resume a sequence of Actions. This ADR
defines how those two existing primitives compose into that capability
without inventing a third, reduced-concept one.

## Decision

**A Policy's response becomes an ordered, optionally gated Sequence of
Actions. A composed run's progress is not a new stored entity — it is a
projection over the same `Execution` rows a single-action Policy already
produces, one row per step.** There is no `workflow` package, no
`Workflow`/`WorkflowDefinition`/`WorkflowInstance` type, and no run-progress
table.

### 1. The definition side: `Policy.Steps`, an ordered `Sequence`

`internal/policy/composition.go` adds:

```go
type GateSpec struct {
    Type   string          // selects the Decision evaluator, e.g. "approval"
    Config json.RawMessage
}

type ActionStep struct {
    Action ActionSpec // the existing primitive, unchanged
    Gate   *GateSpec  // optional: must resolve before the NEXT step may run
}

type Sequence []ActionStep
```

`Policy` gains `Steps Sequence` alongside its existing `Action ActionSpec`.
**Steps is additive, not a replacement.** An empty Sequence — every Policy
that existed before this ADR, and the default for every new one — means
"use `Action` as the whole response," exactly as today. This is the same
backward-compatibility shape ADR-GOV-005's `governance_required_approvals`
default (1) used: the new capability is invisible until a caller opts in.

`Sequence`/`ActionStep`/`GateSpec` each carry `Validate()`, checked
independently of storage (the same posture `ActionSpec`/`Condition` already
have — no domain-level `Validate()` call is wired into `PolicyStore.Create`
today, and this ADR does not change that; wiring it into a request path is
a W4 (HTTP) decision).

Storage: `policy.steps jsonb NOT NULL DEFAULT '[]'`
(`20260815000001_policy_composition.sql`), plus
`CHECK (action_type <> '' OR jsonb_array_length(steps) > 0)` — a Policy must
describe *some* response, enforced by the database, not by application code
that could be bypassed (the same shape `approval_record`'s
`UNIQUE (tenant_id, governance_id, approver_user_id)` is "the whole
enforcement mechanism," per ADR-GOV-005).

### 2. The run-progress side: Executions grouped by `RunID`, not a new entity

This is the decision this ADR is actually about. Two shapes were live
options:

- **Option A (rejected): a JSON "step progress" blob on the Execution row**
  — a `step_progress jsonb` column recording each step's outcome inline.
  Workable, but it creates a second bookkeeping mechanism parallel to the
  one `Execution`/`ExecutionStore` already is: retries, claim/fence, and
  status transitions would need to be re-derived or duplicated *inside* the
  blob for anything beyond the currently-running step, since the blob is
  not itself a claimable unit of work.
- **Option B (rejected): a new `step_run` or `action_step` table**, one row
  per attempted step, replicating `policy_execution`'s claim/fence/retry
  columns for a second, parallel work queue. This is what the brief
  anticipated as the fallback shape, but building it means re-proving
  ADR-CONCURRENCY-002/005/006/007 (atomic claim, fencing, retry accounting,
  detached-outcome writes) a second time for a queue that is structurally
  identical to the one that already has them.
- **Option C (adopted): a step of a composed Sequence IS an `Execution`.**
  `Execution` gains `RunID string`, `StepIndex int`, `Gate GateState`. A
  composed run's steps are ordinary `policy_execution` rows — claimed,
  fenced, retried, and dead-lettered by the exact same `ClaimDue`/
  `MarkResult`/`ReleaseClaim` machinery a single-action Policy's execution
  already uses — that happen to share a `RunID` and carry a `StepIndex`.
  `RunProgress` (in `composition.go`) is a **pure, uncached, uncommitted
  view**: given a run's `Sequence` and its `Execution` rows, `NextStepIndex()`
  walks the steps and returns the first one that has not yet succeeded, or
  whose Gate has not yet passed. It is never itself written to storage —
  there is no run-progress table, so run-progress can never disagree with
  the Executions it is computed from.

Option C is adopted. It is the literal reading of *"composition of Actions
… over Time"*: a Sequence's step is not a new kind of work, it is an
Action's Execution, and "the sequence" is nothing more than several
Executions agreeing to share an id. It also means **zero new queue-
correctness surface**: `internal/arch`'s `TestEveryWorkQueue_HasAFencingToken`
and `TestEveryQueueClaim_IsAtomicAndFenced` guards, which sweep
`policy_execution` today, cover a composed run's steps automatically,
because they are the same table and the same claim statement.

Storage: `policy_execution` gains `run_id text NULL`,
`step_index int NOT NULL DEFAULT 0`, `gate text NOT NULL DEFAULT ''`
(`policy.GateState`: `""`/`"pending"`/`"passed"`), and
`UNIQUE (tenant_id, run_id, step_index) WHERE run_id IS NOT NULL` — one
Execution per step of a given run, tenant-first per the tenant-key-scope
sweep's preferred route (`internal/store/postgres/tenant_key_scope_integration_test.go`),
even though `run_id` itself is platform-minted and cannot itself collide
across tenants.

`ExecutionStore` gains `ListByRun(ctx, runID) ([]Execution, error)` —
storage only, returning a run's steps ordered by `StepIndex`, satisfied by
`postgres.PolicyStore.ListByRun`.

### 3. How a Gate pauses and resumes a sequence (design, not yet executing)

A step with a `Gate` is not "done" for sequencing purposes merely because its
Action's Execution succeeded: `RunProgress.NextStepIndex()` also checks
`Execution.Gate == GatePassed`. Concretely, in W2's executor:

1. Step *i*'s Execution succeeds. If `Seq[i].Gate == nil`, step *i+1* is
   enqueued immediately (a new `Execution` with the same `RunID`,
   `StepIndex = i+1`).
2. If `Seq[i].Gate != nil`, the succeeded Execution's `Gate` is set to
   `GatePending` and **nothing is enqueued** — `NextStepIndex()` returns *i*,
   so the run is paused, visibly, at the step whose gate has not resolved.
3. Something external resolves the gate — for a `"type": "approval"` gate,
   the existing `governance.ApprovalRecorder`/quorum machinery reaching the
   required count (ADR-GOV-005) — and step *i*'s `Gate` is updated to
   `GatePassed`. The next poll's `NextStepIndex()` now returns *i+1*, and
   step *i+1* is enqueued.

No part of this requires a scheduler, a state machine, or a "workflow
engine" process: it is the same "tail committed state, react, enqueue the
next unit of work" shape `internal/policy`'s own consumer already uses for
audit events, applied to a run's own Executions instead of the audit log.

## What this increment does NOT do (explicitly out of scope)

- **No executor changes.** `internal/policy/executor.go` is untouched. It
  does not yet know how to enqueue step *i+1* on step *i*'s success, and it
  does not yet evaluate Gates. That is **W2**.
- **No gate evaluator.** `GateSpec.Type = "approval"` is a name a W3 gate
  evaluator will recognise and consult `governance.ApprovalRecorder`
  through; nothing consults it yet.
- **No HTTP.** There is no route to define a composed Policy, start a run,
  or inspect its progress. That is **W4**, and it is where `Sequence`/
  `ActionStep`'s `Validate()` gets wired into a request path (mirroring how
  `createPolicyRequest` validates `Action.Type` today).
- **No retry/backoff policy per step distinct from today's.** A step's own
  retries are `Execution.RetryCount`/`ClaimDue`'s existing budget — a
  composed run introduces no second retry concept, by design (see Option C).

## What this does NOT violate

- **Vol II §5.3.** No `workflow` package, no `Workflow`/`WorkflowDefinition`/
  `WorkflowInstance` type exists anywhere in the tree
  (`grep -ri "type Workflow"` / `grep -ri "^package workflow"` — both empty).
  The capability is data (`Sequence`, `ActionStep`, `GateSpec`) plus a pure
  projection (`RunProgress`) over the existing `Action`/`Execution`
  primitives.
- **ADR-CP6 (single writer of Lifecycle).** This ADR touches no governance
  table and does not call the Governance Engine. A gate's *evaluator* will,
  in W3, consult `governance.ApprovalRecorder` exactly as
  `handlers_governance.go` does today — it does not gain a second path to
  mutate Lifecycle.
- **ADR-CONCURRENCY-002/005/006/007.** A composed run's steps are ordinary
  `policy_execution` rows, claimed and fenced by the unmodified
  `ClaimDue`/`MarkResult`/`ReleaseClaim` statements. `internal/arch`'s
  queue-completeness and claim-fencing guards pass unchanged (`go test
  ./internal/arch/...`).
- **ADR-TENANCY-001/002/009.** No new table; `policy`/`policy_execution`
  remain in `postgres.TenantOwnedTables` under the RLS they already have.
  The new `steps`/`run_id`/`step_index`/`gate` columns live on rows already
  confined by `tenant_isolation`, proven directly
  (`postgres.TestRLS_PoliciesAreTenantIsolated`, extended in this story to
  create its secret policy as a composed Sequence).

## What is NOT claimed

- **This is not yet a runnable capability.** A Policy can be *stored* with a
  Sequence today; nothing runs it. The founder's "production-grade,
  scalable workflow capability" is delivered incrementally — this is the
  foundation W2's executor extends, not the executor itself.
- **Gate evaluators are not implemented.** `GateSpec.Type` is presently an
  uninterpreted string. Only W3 gives it meaning.
- **No self-referential or branching sequences.** `Sequence` is a flat,
  linear list. Conditional branching, loops, or fan-out/fan-in are not
  modelled and are not implied by this ADR; if the founder needs them, that
  is a new decision, not an extension of this one.

## Incremental plan

- **W1 (this ADR + this story):** `Sequence`/`ActionStep`/`GateSpec`/
  `RunProgress` domain model; `Policy.Steps`; `Execution.RunID/StepIndex/Gate`;
  migration; `PolicyStore` persistence; `ListByRun`; tests.
- **W2:** The executor learns to enqueue step *i+1* when step *i* succeeds
  and has no Gate (or its Gate is `GatePassed`), reusing
  `domain.ResolveAndAuthorize` per step exactly as it does today per
  single-action execution (ADR-TENANCY-003 is unaffected — each step's
  Event ownership is still re-derived, never trusted from the queued row).
- **W3:** Gate evaluators. `"approval"` consults `governance.ApprovalRecorder`
  (ADR-GOV-005); a plain `"condition"` gate re-evaluates a `Condition` against
  current state. A gate evaluator flips `Execution.Gate` from `GatePending`
  to `GatePassed` (or leaves it pending) — it never itself enqueues the next
  step; that stays the executor's job (W2), keeping "evaluate a gate" and
  "advance a sequence" as one responsibility each.
- **W4:** HTTP: define a Policy with `Steps`, start/observe a run (`GET
  .../runs/{run_id}` returning `RunProgress`-shaped JSON), following the
  same DTO-translation shape `handlers_policies.go` already uses for the
  single-action case.

## Evidence

- `go test ./... -race -cover`: full suite green (paste in the story's
  report).
- `go test ./... -tags=integration -race -cover`: full suite green against
  real PostgreSQL, including composition-specific integration tests below.
- `go test ./internal/arch/...`: green — no guard weakened; the queue and
  tenant-key-scope sweeps cover the new columns automatically because they
  derive their subject set from the live schema.
- `grep -ri "type Workflow"` / `grep -ri "^package workflow"` over the tree:
  both empty.
- `policy.TestGateSpec_Validate`, `TestActionStep_Validate`,
  `TestSequence_Validate` — domain validation.
- `policy.TestRunProgress_*` — the run-progress projection: no attempts yet,
  a failed/retrying step does not advance, a succeeded ungated step
  advances, a pending gate pauses the run, a passed gate advances it, and a
  fully-succeeded/fully-passed run reports `Complete()`.
- `postgres.TestPolicyStore_CompositionRoundTrip` — a Policy's `Steps`
  round-trips through real PostgreSQL (including through an `Update`), and
  a run's Executions are retrievable via `ListByRun`, ordered by
  `StepIndex`, with a single-action execution's `NULL run_id` correctly
  absent from any run.
- `postgres.TestRLS_PoliciesAreTenantIsolated` (extended) — a composed
  Sequence is confined by the same row-level policy as the rest of its
  Policy row; the owning tenant still reads its own Steps back in full.
- `postgres.TestEveryTenantScopedUniqueKey_IsTenantScoped` — the new
  `uq_policy_execution_run_step` key is tenant-keyed, not merely justified.
- `make migrate-hash` / `make migrate-validate`: clean.
