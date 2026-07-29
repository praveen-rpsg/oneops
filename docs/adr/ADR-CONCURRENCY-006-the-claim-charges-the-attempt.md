# ADR-CONCURRENCY-006 — The claim charges the attempt, and the outcome outlives the worker

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-002 (at-least-once, the lease), ADR-CONCURRENCY-003 (idempotent production, leader step-down), ADR-CONCURRENCY-005 (claim fencing), ADR-CONCURRENCY-001 (leadership) |

## Context

The four preceding concurrency ADRs are all statements about **safety**: no two
workers act on the same row (002), no re-production mints a duplicate id (003),
no cursor rewinds (004), no evicted worker corrupts the row it lost (005). Every
one of them holds. None of them says anything about **liveness** — whether a row
that keeps failing ever *stops*.

The retry budget exists: `webhook.max_retries` and `policy.max_retries`, compared
by the worker after an attempt. That comparison is the only place the budget was
ever consulted, and it sits in code that runs *after the attempt returned*. So the
budget bound the wrong population: attempts the worker survived.

Two paths bypassed it, and they compose.

**1. The reclaim did not count.** `ClaimDue` recovers a crashed worker's row by
moving a stale claimed row back into the claimed state — with `retry_count`
untouched. That reclaim path exists precisely for workers that did not survive,
and it was the one path that charged nothing. A row whose attempt kills its worker
(OOM, node loss, SIGKILL, a crash-looping pod) is therefore reclaimed with the
budget intact, forever. Each reclaim is another outbound POST to the subscriber,
or another policy action executed against the outside world.

**Proven live against real PostgreSQL.** A delivery with `max_retries = 3` was
put through six cycles of *claim → worker dies before reporting*:

```
after 6 crash cycles: claims_handed_out=6 status="inflight" retry_count=0
```

Six attempts against a budget of three, the counter still at zero, and the row
still in a non-terminal state. There is no number of cycles that ends this; the
queue had no terminating state for the row. The policy-execution queue, which
shares the shape, behaved identically.

**2. The outcome write was cancellable, which made the first path routine.**
ADR-CONCURRENCY-003 made demotion a normal event: losing the advisory lock
cancels the leadership context and the workers stop. The workers wrote their
outcome — `MarkResult` — through that same context. So a delivery in flight
across a demotion did the outbound work and then failed to record any of it: the
error was discarded (`_ =` on the dead-letter paths), the success path did not
even inspect it before incrementing `IncDelivered()`, and the row was left
claimed. That row then falls into path 1.

**Proven live.** With a subscriber that accepts the POST and the leadership
context cancelled mid-flight:

```
dispatcher returned status="failed"; receiver got 1 POST(s);
db row status="inflight" retry_count=0
```

The subscriber received the event. The platform recorded nothing, reported a
metric that disagreed with the database, and left the row queued for an
unbounded re-send. This is the seam between two closed investigations: 003's
step-down cancellation feeding 002's uncounted reclaim. Neither ADR is wrong;
their composition was never examined.

The failure class is therefore: **an attempt whose outcome is not recorded is
retried without bound.**

## Decision

**Attempt accounting belongs on the claim, not in the worker; and an outcome the
platform has already produced in the outside world is not the worker's to
forget.**

1. **`retry_count` means "attempts started", and the claim advances it.**
   `ClaimDue` increments `retry_count` in the same statement that hands the row
   out. The claim is the only event a failing worker cannot skip, so it is the
   only honest place to charge the attempt. A worker that never reports back has
   still spent budget.

2. **The claim enforces the budget and terminates the row.** `ClaimDue` joins the
   row to its budget source (`webhook` / `policy`) and, for any candidate whose
   next attempt would exceed `max_retries`, moves it to `dead_letter` instead of
   claiming it — atomically, in the same statement, under the same
   `FOR UPDATE … SKIP LOCKED`. A missing budget source is `COALESCE(…, 0)`: an
   orphaned row (subscriber deleted) can never succeed, so it terminates on
   first sight rather than circulating.

3. **The worker compares against the attempt number it was given.** With the
   claim charging the attempt, the worker's own check becomes
   `del.RetryCount >= wh.MaxRetries` — no local increment. Worker and claim now
   apply the same rule to the same number, so they cannot disagree. The worker's
   eager dead-letter on a completed final failure is kept: it terminates the row
   immediately rather than one claim later.

4. **Outcomes are written on a context detached from the worker's.**
   `outcomeContext(ctx)` = `context.WithTimeout(context.WithoutCancel(ctx), 5s)`.
   It keeps the worker context's values (tracing, request identity) and drops
   only its cancellation, and carries its own deadline so a stuck database cannot
   hold a demotion or shutdown open indefinitely. Every `MarkResult` in both
   workers' attempt paths uses it.

5. **A failed outcome write is reported, not swallowed.** The success path no
   longer increments `IncDelivered()` when the write failed; it logs
   `delivered but outcome not recorded` and returns the error. A metric must not
   claim an outcome the database does not hold.

6. **An unused claim is released, refunding its attempt.** Because the claim
   charges up front, a worker stopped between claiming a batch and attempting it
   (demotion, shutdown) would otherwise burn budget it never spent — and a few
   restarts would dead-letter healthy deliveries that were never sent.
   `ReleaseClaim` returns the row to `pending` and gives the attempt back. It is
   fenced on the claim token exactly like `MarkResult` (ADR-CONCURRENCY-005), so
   an evicted worker cannot refund or reset a row its reclaimer now owns.

7. **The dead-letter requeue refills the budget.** Terminating a poison row is
   only acceptable if an operator can bring it back. `RequeueDeadLetters` resets
   `retry_count` to zero and clears the stale claim token, so a requeued row is
   genuinely claimable rather than one the claim immediately refuses.

## Consequences

**What is now guaranteed.** A delivery or policy execution is handed to a worker
at most `max_retries` times in total — counting attempts whose outcome was never
recorded — and then terminates in `dead_letter`. The queue has a terminating
state for every row, including rows whose workers never come back. An outcome
produced against the outside world is recorded even when the worker producing it
is being stopped.

**What is deliberately *not* claimed.**

- **This does not make delivery exactly-once.** ADR-CONCURRENCY-002's ceiling
  stands: a crash between the outbound action and its recorded outcome still
  produces a duplicate, dedup-able by the stable id (ADR-CONCURRENCY-003). What
  changes is that such duplicates are now *bounded* — previously they were not.
- **The bound is on attempts, not on wall-clock time.** A row can still sit
  claimed for a full lease per attempt before being reclaimed.
- **`retry_count`'s observable meaning changed** from "retries completed" to
  "attempts started". A row in flight on its first attempt now reads `1`, not
  `0`, on the admin endpoints. This is the more honest number — it is the one the
  budget is actually enforced against — but it is a visible change.
- **The 5s outcome-write deadline is a fixed constant**, not operator-tunable, as
  the claim lease still is not (noted in ADR-CONCURRENCY-005 and still open).

**Behavioural change to orphaned rows.** A delivery whose webhook has been
deleted is now dead-lettered by the claim rather than by the worker one cycle
later. The terminal state is identical; it is reached without an outbound
attempt. `webhook_delivery.webhook_id` has no foreign key to `webhook`, so
orphans are genuinely reachable and this path is exercised.

## Evidence

Live exploit, before the change:

- Crash-loop, `max_retries=3`, six cycles → `claims_handed_out=6`,
  `status="inflight"`, `retry_count=0`. Policy queue identical.
- Demotion mid-flight → subscriber received the POST, `status="inflight"`,
  `retry_count=0`, success metric incremented anyway.

Live re-attack, after the change, same tests unmodified:

- Crash-loop, six cycles → `claims_handed_out=3`, `status="dead_letter"`,
  `retry_count=3`. Policy queue identical.
- Demotion mid-flight → subscriber received the POST, `status="failed"`,
  `retry_count=1` — the outcome survived the cancellation.
- Orphaned delivery → `claims_handed_out=0`, `status="dead_letter"`.
- Five claim/release cycles → `retry_count=0`, `status="pending"`; a stale
  worker's release changes nothing on the reclaimer's row.
- Requeued dead-letter is claimable again.

Full suite green under `-race` against real PostgreSQL, including every
pre-existing concurrency, fencing, tenancy and audit test.

The architecture tests were mutation-checked: reverting the claim's
`retry_count` advance, and reverting one `MarkResult` to the worker context,
each fails the build with the diagnostic naming this ADR.

## Enforcement

- `arch.TestClaimDue_ChargesTheAttemptAndBoundsIt` — both stores' `ClaimDue`
  advance `retry_count`, read `max_retries`, defend a missing budget source with
  `COALESCE`, and contain the `dead_letter` transition.
- `arch.TestWorkerOutcomeWrites_AreNotCancelledByDemotion` — no `MarkResult(ctx,`
  survives in either worker's attempt path.
- `arch.TestOutcomeContext_IsDetachedAndBounded` — the outcome context is built
  from `context.WithoutCancel(ctx)` and carries its own deadline.
- `arch.TestWorkers_ReleaseUnusedClaimsOnStop` — both `RunOnce` loops release
  claims they will not attempt, on the detached context.
- `postgres.TestRetryLiveness_{Webhook,Policy}ReclaimIsBounded` — the live
  crash-loop exploit, as a regression test.
- `postgres.TestRetryLiveness_OrphanedDeliveryTerminates`
- `postgres.TestRetryLiveness_ReleasedClaimRefundsTheAttempt`
- `postgres.TestRetryLiveness_ReleaseIsFencedOnTheClaim`
- `postgres.TestRetryLiveness_RequeuedDeadLetterIsDeliverableAgain`
- `postgres.TestOutcomeDurability_ResultSurvivesWorkerCancellation`
