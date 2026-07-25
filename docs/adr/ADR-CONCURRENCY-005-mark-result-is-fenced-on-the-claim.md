# ADR-CONCURRENCY-005 — A claimed row's outcome is written under a fence on the claim

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-002 (at-least-once, the lease), ADR-CONCURRENCY-003 (idempotent production), ADR-CONCURRENCY-001 (leadership) |

## Context

ADR-CONCURRENCY-002 gave the delivery and policy queues an atomic claim with a
lease: a due row is moved to a claimed state (`inflight`/`running`) with
`claimed_at` set, and a row whose claimer crashed is reclaimed once `claimed_at`
is older than the lease. That ADR explicitly left one thing open — *"True fencing
of the outbound call is possible future work and is not claimed here"* — and
named the lease a tuning trade: too short and a still-running slow request is
re-claimed as a duplicate.

That residual was attacked. The reclaim is not the whole story; the question is
what happens when the *evicted* worker finally finishes. `MarkResult` wrote
`UPDATE … WHERE id = $1` — keyed on the row id alone, with no check that the
caller still held the claim. So a worker whose lease expired and whose row was
reclaimed by another could still write the row.

Proven live against real PostgreSQL. A delivery was claimed by W1 (a slow POST
begins), its lease expired, and W2 reclaimed the row. W2 delivered it
successfully (`delivered`). Then W1's slow POST failed and W1 called
`MarkResult(failed, reschedule)` — and, unfenced, **it landed**: the delivered
row was resurrected to `failed` with a `next_attempt_at`, guaranteeing a third
delivery of an event already delivered. The unfenced completion of an evicted
worker corrupts the reclaiming worker's terminal state and amplifies duplicates.
This is the classic distributed-locking fencing problem: a lease holder that has
been evicted must not be able to act as though it still holds the lease.

## Decision

**A worker records a row's outcome only under a fence on the claim it was granted.
`MarkResult` writes iff the row is still claimed under the same token; otherwise
it changes nothing and reports that it was fenced.**

1. **The claim is the fencing token.** `ClaimDue` already stamps `claimed_at` when
   it moves a row into the claimed state; each reclaim advances it. That value is
   now surfaced on the claimed row (`Delivery.ClaimedAt` / `Execution.ClaimedAt`)
   and carried by the worker into `MarkResult`.

2. **The write is fenced.** `MarkResult` updates
   `WHERE id = $1 AND ($token IS NULL OR claimed_at = $token)`. A worker whose row
   was reclaimed holds the *old* `claimed_at`; the predicate fails, zero rows
   change, and the store returns `ErrStaleClaim`. The current owner's token
   matches and its write lands. A zero token — the admin test path, whose row was
   never claimed — writes unfenced, unchanged.

3. **The evicted worker discards its result.** The dispatcher and executor treat
   `ErrStaleClaim` as "reclaimed mid-flight": they log it and do **not** record
   the outcome or reschedule, leaving the authoritative result to the current
   owner. The outbound side effect already happened (at-least-once); the receiver
   collapses it on the stable delivery id (ADR-CONCURRENCY-003).

## Consequences

**The state-corruption class is eliminated.** An evicted worker can no longer
resurrect a delivered row, overwrite the reclaimer's outcome, or corrupt its
retry/backoff bookkeeping. Verified live: the same eviction that resurrected a
delivered row to `failed` before the fix is now rejected with `ErrStaleClaim`,
and the row keeps the reclaiming owner's `delivered` state. The same fence
protects policy executions.

**The honest guarantee is unchanged and now completely characterised.** Delivery
and execution remain **at-least-once** (ADR-CONCURRENCY-002). A lease that expires
under a slow outbound call still yields a *concurrent* second send by the
reclaiming worker — that duplicate is inherent to at-least-once and is dedup-able
on the stable id. What the fence removes is the *amplification*: without it, the
duplicate compounded into extra reschedules and resurrected terminal rows; with
it, exactly one worker — the current owner — decides the row's terminal state.

**Enforcement.**

- `postgres.TestLeaseFencing_WebhookEvictedWorkerIsFenced` and
  `…PolicyEvictedWorkerIsFenced` reproduce the eviction deterministically (two
  time-shifted claims across the lease boundary), assert the reclaim advances the
  token, and assert the evicted worker's write returns `ErrStaleClaim` and leaves
  the owner's terminal state intact. Pre-fix the delivery test failed — the row
  was resurrected.
- `arch.TestMarkResult_IsFencedOnTheClaim` parses both `MarkResult` bodies and
  fails the build if either drops the `claimed_at` fence or the `ErrStaleClaim`
  signal — the exact regression that reopens the unfenced write. Verified to bite.

## Residual risks

- **The concurrent double-send remains, bounded and dedup-able.** The fence stops
  state corruption, not the second outbound call a lease-expiry reclaim triggers.
  That is the at-least-once ceiling; the receiver deduplicates on
  `X-OneOps-Delivery`. Eliminating the second *call* would require cancelling an
  in-flight request on eviction and is not claimed.
- **The token is `claimed_at`, sound because reclaims are ≥ lease apart.** Two
  distinct claims of a row are separated by at least the lease, so their
  `claimed_at` values differ; the token is effectively unique per claim. A
  dedicated monotonic fencing counter would make this independent of the lease and
  is possible future hardening.
- **The lease is not yet operator-tunable.** The dispatcher and executor are
  built with default configs, so the lease is the two-minute default;
  ADR-CONCURRENCY-002's "it is configurable" is true of the struct field but not
  yet wired to configuration. Wiring it is minor operational work, tracked
  separately; it does not affect the fence's correctness.

## The invariant

A worker may record a row's outcome only while it still holds the claim it was
granted. Eviction is silent to the evicted worker, so the write itself is fenced:
the row's terminal state is always decided by its current owner, never by a
straggler whose lease has passed. Duplication stays at the at-least-once ceiling
and is never amplified by a stale write.
