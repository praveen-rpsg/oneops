# ADR-CONCURRENCY-002 — Delivery is at-least-once, with a bounded duplicate window and a stable dedup key

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-001 (leadership), ADR-TENANCY-003/004 (execution ownership) |

## Context

ADR-CONCURRENCY-001 introduced leader election and, in an early draft, claimed
delivery was "exactly-once in steady state." Distributed-systems review treated
that claim as guilty until proven and attacked it.

Exactly-once was disproved live. A single leader delivered a webhook, and its
process was killed after the receiver had accepted the POST but before the
result was persisted. The delivery row stayed `pending`; on restart it was
re-claimed and re-delivered, and the receiver recorded the **same delivery id
twice**. This is the two-generals problem: a system cannot atomically "perform an
outbound side effect and record that it did." Exactly-once delivery of an
outbound HTTP request is not achievable, in steady state or otherwise.

The evidence supports at-least-once, and nothing stronger.

Two things then mattered: how wide the duplicate window is, and whether a
receiver can collapse duplicates itself.

## Decision

**The delivery contract is at-least-once. The duplicate window is narrowed to a
crash between the outbound action and its result, and every attempt of a logical
delivery carries the same stable id so receivers can deduplicate.**

1. **Atomic claim with a lease.** `ClaimDue` was a plain `SELECT` of due rows
   with no claim, so during a leadership handoff or a lock-loss two workers
   selected the same rows and both performed the outbound action — a duplicate
   with no crash involved. It is now a compare-and-set: due rows, and stale
   claimed rows whose worker crashed, are moved to a claimed state (`inflight`
   for deliveries, `running` for policy executions) with `claimed_at` set, under
   `FOR UPDATE SKIP LOCKED`. No two workers hold the same row; the status change
   stops re-selection until the lease elapses. Proven: concurrent claims are
   disjoint, an inflight row is not reclaimed within its lease, and a stale one
   is reclaimed after it.

2. **Crash recovery is bounded by the lease, not immediate.** A worker that dies
   after claiming leaves the row claimed; it is retried only once `claimed_at` is
   older than the lease (default two minutes, comfortably above the request
   timeout). Work is never lost, and re-execution after a crash is bounded rather
   than instant.

3. **A stable dedup key reaches the receiver.** Each delivery carries its row id
   in the `X-OneOps-Delivery` header, and a re-delivery of the same row carries
   the same id — verified: the duplicate above bore an identical delivery id.
   A receiver that deduplicates on it observes each logical delivery once. This
   is the mechanism that makes at-least-once *effectively-once for a compliant
   receiver*, which is the strongest honest guarantee the architecture provides.

## Consequences

**The guarantee is stated plainly and is not overstated anywhere.** Delivery is
at-least-once. Duplicates occur only when a worker dies between sending and
recording, and each duplicate is the same delivery id. Consumers that require
effectively-once must deduplicate on `X-OneOps-Delivery`; this is the documented
contract, not an implementation detail.

**Policy execution shares the guarantee and the mechanism.** Its queue got the
same atomic claim and lease. A policy action is an outbound side effect with the
same two-generals limit; consumers of policy-driven actions should treat them as
at-least-once. Policy executions also carry a stable id.

**Failure model, characterised against the attack plan:**

| Scenario | Outcome |
|---|---|
| Crash after side effect, before persistence | Row stays claimed; reclaimed after lease; re-delivered with the same id. **Duplicate, dedup-able.** |
| Crash after persistence | Row is terminal (`delivered`/`succeeded`); never re-selected. **No replay.** |
| Crash during claim | Row is claimed but unworked; reclaimed after lease. **No lost work; bounded re-execution.** |
| Concurrent workers (handoff / lock-loss overlap) | Atomic claim + SKIP LOCKED: each row claimed once. **No concurrent double-send of a row.** |
| Leader flapping | Bounded: each promotion re-runs only its own claims; no row is delivered by two workers at once. |

## Residual risks

- **The crash-window duplicate is inherent and remains.** It cannot be removed
  for outbound HTTP; it is bounded (lease) and dedup-able (stable id), and that
  is the ceiling. Receiver-side deduplication is required for effectively-once
  and is the consumer's responsibility.
- **Lease tuning is a trade.** Too short risks re-delivering a still-running slow
  request as a duplicate; too long delays recovery after a crash. The default
  (two minutes) sits well above the request timeout; it is configurable.
- **The lock-loss overlap is narrowed, not eliminated.** A paused leader (GC,
  SIGSTOP) can still perform one in-flight POST after losing its lock before it
  notices; the atomic claim ensures it cannot also claim new rows the standby is
  working, so the exposure is a single already-claimed in-flight request, which
  the dedup key covers. True fencing of the outbound call is possible future work
  and is not claimed here.

## The invariant

Delivery is at-least-once. The platform makes the duplicate window as narrow as
an outbound side effect allows — no concurrent double-claim, crash recovery
bounded by a lease — and gives every logical delivery a stable identity so a
receiver can make it effectively-once. The guarantee is stated as what the
architecture can prove, never as what would read better.
