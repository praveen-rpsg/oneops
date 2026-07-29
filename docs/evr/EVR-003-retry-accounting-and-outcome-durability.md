# EVR-003 — Retry accounting (entry 20) and outcome durability (entry 21)

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Auditor** | Acting CTO / Evidence Authority |
| **Trust Register entries** | 20, 21 |
| **Associated ADR** | ADR-CONCURRENCY-006 (the claim charges the attempt; outcomes outlive the worker) |
| **Confidence** | **PARTIALLY VALIDATED** — entry 21 **INVALIDATED as a class**; entry 20 validated with a stated exception |

## Why selected

By enforcement quality, not subject. `retry_accounting_test.go` carries **four**
enumerated subject lists, every one naming the same two stores and two workers:

```go
{"../store/postgres/webhook_store.go", "ClaimDue", "webhook"}
{"../store/postgres/policy_store.go",  "ClaimDue", "policy"}
{"../events/dispatcher.go", "attempt"} / {"../policy/executor.go", "attempt"}
```

A third queue (`webhook_replay_job`) and a third worker (the replay worker) were
introduced by ADR-CONCURRENCY-007 *after* those guards were written. Under the
enforcement heuristic, sibling instances were assumed until disproven.

## Original claims and evidence

**Entry 20.** The reclaim path left `retry_count` untouched, so a crash-looping
row was redelivered forever with no terminating state. Live: 6 crash cycles on a
budget of 3, `retry_count=0`, still `inflight`. Closed by moving attempt
accounting onto the claim.

**Entry 21.** Workers wrote `MarkResult` through the leadership context, so a
demotion mid-flight POSTed to the subscriber, recorded nothing, and incremented
the success metric anyway. Closed by `outcomeContext` — a context detached from
the worker's cancellation.

## Fresh investigation and evidence

**Entry 21 — sibling found.** The replay worker recorded its outcome as:

```go
if err := w.jobs.UpdateJob(ctx, job); errors.Is(err, ErrStaleClaim) { … }
w.metrics.IncReplayJob()
```

Two defects, both verbatim the ones entry 21 records as eliminated: the worker's
cancellable context, and an error compared only against `ErrStaleClaim` so a
`context.Canceled` falls through to the success metrics below it.

Proven live (`events.TestReplayOutcome_SurvivesWorkerCancellation`): executing a
replay under a cancelled context left the job

```
status=running  events_replayed=0
```

with the outcome lost. This queue has **no lease recovery**
(ADR-CONCURRENCY-007), so the job is stuck in `running` permanently — the two
residuals compound into an unrecoverable state.

**Entry 20 — no sibling, with a stated exception.** `webhook_replay_job` has no
`retry_count` column and `ClaimPendingJobs` selects only `pending`, so a failed
replay job terminates at `failed` and is never re-attempted. Entry 20's class is
*"an attempt whose outcome is not recorded is retried without bound"*; this queue
retries nothing, so the class does not apply. The **opposite** liveness gap —
a job stuck in `running` is never recovered — is already recorded as a residual
under ADR-CONCURRENCY-007 and is reinforced by this EVR.

## Root cause

The guards enumerated the workers and stores that existed when they were written,
and the platform grew a third of each. This is the fourth consecutive audit in
which enumerated enforcement concealed a sibling.

## Class status

- **Entry 21 — was CLASS CLOSED, now INSTANCE CLOSED → re-closed by
  ADR-CONCURRENCY-008.** The class had a live third instance.
- **Entry 20 — CLASS CLOSED**, with the explicit exception that the replay queue
  has no retry semantics at all, so there is nothing to bound.

## Evidence confidence

- **Entry 21: ECL-5** after this audit — fresh live evidence, tree-derived
  completeness (`TestEveryWorkerOutcomeWrite_UsesADetachedContext`), mutation
  verified in both directions plus self-validation. Previously ECL-3.
- **Entry 20: ECL-4.** Fresh evidence and a schema-derived queue sweep, but its
  budget-enforcement guard still names two stores; the third queue is exempt by
  design rather than by structure, so completeness rests on that stated
  exception.

## Permanent structural enforcement

`TestEveryWorkerOutcomeWrite_UsesADetachedContext` derives its subject set from
the tree: any file that runs a context loop is a worker file, and no outcome
write in one may take the raw `ctx`. It replaces four enumerated lists.

## Mutation verification and self-validation

| Control | Result |
|---|---|
| Revert the **replay** outcome write to `ctx` | sweep fails, naming `replay.go` |
| Revert the **dispatcher** outcome write to `ctx` | sweep fails, naming `dispatcher.go` — proves it covers the original two, not only the new one |
| Break the worker-file detector | fails with *"no worker files found; the sweep would be vacuous"* |
| Substring collision: `.UpdateJobStatus(ctx` | **not** flagged — the regex requires `(` immediately after the method name |

The arch sweep fired even while the mutated source did not compile, because it
reads source text — a useful property: it cannot be defeated by a change that
breaks the build elsewhere.

## Residual risk

- **The outcome-writer method set is a pattern (`MarkResult`, `UpdateJob`), not a
  derivation.** A new outcome-recording method under a different name would not
  be swept. This is the remaining enumeration in this guard and is stated rather
  than implied; deriving it from the store ports is possible future work.
- The worker-file detector keys on a context-loop signature; a worker written in
  a materially different shape would not be recognised.
- The replay queue's stuck-`running` gap remains open by design
  (ADR-CONCURRENCY-007) and is now *more* consequential, since it is the state a
  lost outcome would have left behind. Closing it requires lease recovery plus
  retry accounting for that queue.
