# ADR-CONCURRENCY-008 — Every worker's outcome write is detached from the worker's cancellation

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-006 (outcome durability — **class reopened by this ADR**), ADR-CONCURRENCY-007 (the replay queue's claim and its stuck-`running` residual), ADR-CONCURRENCY-003 (demotion cancels the leadership context), **EVR-003** |

## Context

From the Trust Register audit recorded in EVR-003. Entry 21 claims *"outcome lost
when the worker is stopped"* as an eliminated class. Its guard names two workers.
ADR-CONCURRENCY-007 introduced a third — the replay worker — after that guard was
written, and it recorded its outcome like this:

```go
if err := w.jobs.UpdateJob(ctx, job); errors.Is(err, ErrStaleClaim) { … }
w.metrics.IncReplayJob()
```

Both defects entry 21 records as eliminated are present: the write rides the
worker's cancellable context, and the error is compared only against
`ErrStaleClaim`, so a `context.Canceled` falls through to the success metrics
below it — the metric claims an outcome the database does not hold.

Proven live: executing a replay under a cancelled context left the job at
`status=running, events_replayed=0`, with deliveries already enqueued. Because
this queue has **no lease recovery** (ADR-CONCURRENCY-007), the job is stuck in
`running` permanently. The two residuals compound: a lost outcome here is not
merely lost, it is unrecoverable.

## Decision

**Every worker records an outcome on a context detached from its own
cancellation, and a failed outcome write is reported rather than counted as
success.**

1. The replay worker writes through `outcomeContext(ctx)` —
   `WithTimeout(WithoutCancel(ctx), 5s)` — the same helper the dispatcher and
   executor use, so there is one mechanism rather than three.

2. A non-`ErrStaleClaim` error returns before the success metrics, and is logged
   as *"job ran but outcome not recorded"*.

3. **The guard is derived from the tree, not from a list.**
   `TestEveryWorkerOutcomeWrite_UsesADetachedContext` treats any file running a
   context loop as a worker file and fails if an outcome write in one takes the
   raw `ctx`. It replaces four enumerated subject lists that named the same two
   workers and two stores.

## Consequences

**What is now guaranteed.** No worker in the tree can record an outcome through a
context a demotion or shutdown cancels, and no worker's success metric can
outrun its database write.

**What is *not* claimed.**

- **The outcome-writer method set is a pattern** (`MarkResult`, `UpdateJob`), not
  a derivation from the store ports. A new outcome-recording method under a
  different name would not be swept. This is the remaining enumeration in the
  guard and is stated rather than implied.
- **The replay queue's stuck-`running` gap is still open** (ADR-CONCURRENCY-007).
  This ADR removes the most likely *cause* of a job being abandoned there; it
  does not add the lease recovery that would let one be reclaimed.
- The 5s outcome deadline remains a fixed constant, as in ADR-CONCURRENCY-006.

## Evidence

Before: replay under a cancelled context → `status=running, events_replayed=0`,
outcome lost, job unrecoverable.
After: the outcome is recorded (`status=failed`), and the success metrics are not
incremented on a failed write.

Full suite green under `-race` against real PostgreSQL, all 19 packages.

## Enforcement and mutation verification

`arch.TestEveryWorkerOutcomeWrite_UsesADetachedContext`, plus
`events.TestReplayOutcome_SurvivesWorkerCancellation` as the regression test.

Negative controls, both directions:

- reverting the **replay** write to `ctx` → sweep fails naming `replay.go`;
- reverting the **dispatcher** write to `ctx` → sweep fails naming
  `dispatcher.go`, proving the sweep covers the originally-guarded workers and
  not merely the newly-found one;
- breaking the worker-file detector → fails with *"no worker files found; the
  sweep would be vacuous"*, so it cannot pass by finding nothing;
- `.UpdateJobStatus(ctx` → **not** flagged, confirming no substring collision.

The sweep fires even when the mutated source does not compile, because it reads
source text rather than a built package.
