# ADR-CONCURRENCY-003 — Production is idempotent by content-derived identity, and a demoted leader stops its workers

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-001 (leadership), ADR-CONCURRENCY-002 (delivery is at-least-once) |

## Context

ADR-CONCURRENCY-002 established that delivery is at-least-once and that every
attempt of a logical delivery carries a stable id in `X-OneOps-Delivery`, so a
compliant receiver can make it *effectively-once*. That guarantee has a
precondition that was never verified: that the stable id is stable across
*re-production*, not merely across *re-delivery of one row*. It was not.

Two defects were proven live, treating leadership and the dedup key as guilty
until proven otherwise.

**1. The producer was not idempotent.** The relay minted each delivery row's id
from `crypto/rand`, and the policy consumer did the same for executions. The
`ON CONFLICT (id) DO NOTHING` on enqueue was therefore inert — two productions of
the same logical event never shared an id, so nothing collided. A single leader
delivered one governance event (id `dlv_c579…`), and the relay's per-chain cursor
was then reset — exactly the state a crash between enqueue and `SetCursor`
leaves, and exactly what a second relay sees during a leadership overlap. On the
next pass the relay re-produced the same event as a **second row with a new id**
(`dlv_5dc8…`). The receiver saw **two deliveries with two different dedup keys** —
a duplicate it could not collapse, because the key it was told to deduplicate on
was different each time. This is strictly worse than the crash-window duplicate
of ADR-CONCURRENCY-002: that one is dedup-able; this one defeated the very
mechanism ADR-CONCURRENCY-002 relies on.

**2. A demoted leader kept running its workers.** `watchLeadership` detected a
lost lock connection and only *logged* — "workers must be restarted under a fresh
election" — then returned. It never stopped the workers. A leader whose lock was
released out from under it (backend terminated, partition, connection reset) went
on producing and dispatching indefinitely alongside the newly promoted standby: a
permanent two-leader overlap, not a bounded one.

## Decision

**Production is made idempotent by giving every produced row a content-derived
identity, and a leader that loses its lock stops its workers and re-enters the
election.**

1. **Content-derived row identity.** A delivery's id is now
   `DeliveryID(webhook_id, chain_id, seq)` — a SHA-256 over the subscriber and
   the committed event's position in the append-only log, which together are the
   logical identity of "this event delivered to this webhook". An execution's id
   is `ExecutionID(policy_id, chain_id, seq)`, the analogous function. Both are
   pure: the same logical unit always produces the same id, so a re-produced event
   collides on the primary key and `ON CONFLICT (id) DO NOTHING` collapses it to
   one row. The relay, the replay worker, and the policy consumer all use these;
   the random generators are gone from the produced-row paths. Replay of the same
   window is now idempotent as a side benefit — re-enqueuing collides rather than
   duplicating.

2. **The dedup key is now stable across re-production, not just re-delivery.**
   Because the id is a function of the event, every production of a logical
   delivery — first pass, cursor-reset re-read, or a second relay during an
   overlap — carries the *same* `X-OneOps-Delivery`. The effectively-once claim in
   ADR-CONCURRENCY-002 now rests on a key that is genuinely stable, not one that
   was stable only per row.

3. **A demoted leader stops its workers.** `RunAsLeader` now runs the workers
   under a *leadership context* — a child of the process context that is cancelled
   the instant the lock is lost. On losing the lock the process cancels that
   context (stopping the relay, dispatcher, consumer, executor, replay, retention
   and integrity workers) and re-enters the election; if it re-acquires, it starts
   a fresh set of workers under a fresh leadership context. The health-watch
   interval was shortened to five seconds so demotion is noticed quickly.

## Consequences

**The non-dedup-able duplicate class is eliminated.** A re-processed event can no
longer become a second row with a new id. Verified live after the fix: the same
cursor-reset that produced two rows and two ids now produces **one row, one id,
one POST** with a single stable dedup key.

**An overlap is now safe, not merely short.** Between a leader losing its lock and
noticing it (bounded by the five-second watch), two instances may briefly run the
workers. That window no longer duplicates: two relays produce the same
deterministic id (one row), and the atomic claim of ADR-CONCURRENCY-002 stops two
dispatchers from claiming the same row. Correctness during the overlap comes from
idempotent production and the atomic claim, and the step-down bounds how much
redundant (but safe) work the old leader does. Verified live: a leader's lock
backend was terminated, the old leader logged that it stopped its workers and
re-entered the election, exactly one leader re-established, and an event produced
after the disruption yielded one row, one id, one POST.

**The honest contract is unchanged and now better-founded.** Delivery and
execution remain **at-least-once** (ADR-CONCURRENCY-002); the two-generals
crash-window duplicate is inherent and remains, bounded by the lease and
dedup-able by the stable id. What changed is that the stable id is now genuinely
stable, so the effectively-once-for-a-compliant-receiver property is real rather
than conditional on never re-producing.

**Enforcement.**

- `events.TestDeliveryID_DeterministicAndIdempotent` and
  `policy.TestExecutionID_DeterministicAndIdempotent` prove the ids are pure and
  that distinct logical units do not collide (including field-boundary aliasing).
- `arch.TestProducers_UseDeterministicRowIdentity` parses the relay, replay worker
  and consumer and fails the build if a produced row sets its `ID` from anything
  other than `DeliveryID`/`ExecutionID` — the exact regression that reintroduces a
  random id. Verified to bite: swapping in `newDeliveryID()` fails it.
- `postgres.TestProducerIdempotency_{Delivery,Execution}ReenqueueCollapses`
  enqueue the same logical unit twice and assert a single row survives.
- `ops.TestRunAsLeader_DemotedLeaderStopsWorkersAndReElects` terminates the
  leader's lock backend and asserts the leadership context is cancelled (workers
  stop) and leadership is re-established.

## Residual risks

- **The crash-window duplicate remains** (ADR-CONCURRENCY-002): a worker that dies
  between the outbound POST and persisting the result re-delivers the *same row*
  with the *same id* after the lease. Inherent to outbound HTTP; dedup-able.
- **Idempotency is keyed on `(subscriber, chain_id, seq)`.** It assumes the audit
  log's `(chain_id, seq)` is a stable, unique coordinate for a committed event —
  which it is by construction (append-only, per-object chain). If that ever
  changed, the identity function would have to change with it. Called out so the
  coupling is explicit.
- **The step-down window is bounded, not zero.** Up to five seconds of redundant
  work by a demoted leader is possible; it is made safe by this ADR, not removed.
  True fencing of an in-flight outbound call is still possible future work and is
  not claimed here.

## The invariant

A produced row's identity is a function of what it represents, not of when it was
produced. Re-producing a logical event — after a crash, a cursor reset, or a
leadership overlap — yields the same row, never a duplicate the receiver cannot
collapse. And an instance that has lost leadership does not keep acting as leader:
it stops its workers and stands for election again. The duplicate window is as
narrow as an outbound side effect allows, and every remaining duplicate is the
same id.
