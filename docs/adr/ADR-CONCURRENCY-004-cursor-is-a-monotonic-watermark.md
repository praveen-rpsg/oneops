# ADR-CONCURRENCY-004 — The relay/consumer cursor is a monotonic watermark over a gapless committed prefix

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-001 (leadership), ADR-CONCURRENCY-002 (at-least-once), ADR-CONCURRENCY-003 (idempotent production), ADR-AUDIT-004 (chain append) |

## Context

Investigation 1 eliminated *duplicate* deliveries you cannot deduplicate. This
investigation attacked the dual: can the relay or the policy consumer *lose* an
event — advance its per-chain cursor past a committed event that was never
enqueued? A lost event is silent: a webhook that should have fired never fires, a
policy that should have triggered never triggers, and nothing records it.

Both workers tail with an unlocked read-modify-write per chain: `GetCursor →
ListEvents(seq > cursor) → Enqueue → SetCursor(max seq)`. The safety of advancing
the cursor to `max(seq)` rests entirely on one assumption, which was treated as
guilty until proven: **that when the relay sees an event at seq = M, every event
with seq < M on that chain is already visible** — otherwise a straggler with a
lower seq, committing after the cursor passed M, is skipped forever.

Two things were established, one by evidence that already existed and one by a
live attack.

**The assumption holds, and it is not luck.** `seq` is assigned per chain as
`last_seq + 1` under a `SELECT … FOR UPDATE` on the chain-head row, held until
commit (ADR-AUDIT-004). A transaction writing seq = N+1 cannot even read the head
until the transaction that wrote seq = N has committed and released the lock; a
unique `(chain_id, seq)` constraint is the backstop. Under READ COMMITTED this
makes the committed log of any chain **a gapless prefix `[1..k]` at every instant**
— seq = 10 can never be visible while seq = 9 is not. Advancing the cursor to
`max(seq)` therefore skips nothing. This is already enforced live by
`postgres.TestAppenderConcurrentSerialization`: twelve concurrent appends to one
chain produce exactly seqs 1..12, contiguous, no gap, no duplicate. The
no-lost-event property is a consequence of the chain-head lock, not of the relay.

**But the cursor write was not monotonic.** `SetCursor`/`SetPolicyCursor` did a
blind `ON CONFLICT DO UPDATE SET last_seq = EXCLUDED.last_seq` — it stored
whatever sequence it was handed, higher *or lower* than the current value. A
watermark must never move backward, and this one could. Proven live: writing 10
then 5 left the cursor at **5**. The exposure is real, not theoretical: the
bounded leadership step-down window of ADR-CONCURRENCY-003 is exactly when two
relays run, and a demoted leader carrying an older sequence could rewind the
watermark under the new leader's advance, forcing already-processed events to be
re-read. That is safe *today* only because production is idempotent
(ADR-CONCURRENCY-003) and the events are still in the log to be re-read — the
cursor was borrowing its safety from downstream properties instead of enforcing
its own invariant.

## Decision

**The cursor is a monotonic watermark: it never moves backward, and it is never
advanced past an event that has not been durably enqueued.**

1. **Monotonic write.** Both cursor writers now upsert
   `last_seq = GREATEST(<table>.last_seq, EXCLUDED.last_seq)`. A stale or
   overlapping writer with an older sequence is a no-op against the watermark; a
   genuine advance still moves it forward. The cursor can only ever increase.

2. **Gapless-prefix source, unchanged and relied upon.** The monotonic cursor is
   correct *because* the log it indexes is a gapless committed prefix per chain
   (the chain-head lock). This ADR names that dependency explicitly so it cannot
   be weakened silently: any future change that lets a lower seq become visible
   after a higher one — a global sequence assigned at insert, dropping the
   `FOR UPDATE`, a non-locking append path — would reintroduce lost events and
   must be rejected.

3. **Enqueue before advance, unchanged.** The relay and consumer enqueue the
   batch and only then set the cursor; on an enqueue error they return without
   advancing. The cursor never leads the durably-enqueued set, so it never needs
   to regress — which is why making it strictly monotonic is safe and loses no
   legitimate behaviour. The only writers of these cursors are the tail loops
   themselves; replay and administration do not move them.

## Consequences

**No lost events per chain, and the watermark is exact.** Verified live end to
end across a leader failover: twelve committed ratification events produced twelve
deliveries with twelve distinct ids; every cursor equalled its chain head exactly
(zero ahead, zero behind); the delivered set `(chain_id, seq)` equalled the
committed set with zero lost and zero phantom events.

**An overlap is safe by construction, not by re-read.** A demoted leader's stale
cursor write can no longer rewind the watermark, so the new leader's progress is
never undone. Idempotent production still covers the re-delivery of an in-flight
row; the cursor no longer *depends* on it to avoid regression.

**The guarantee is stated honestly.** Delivery remains at-least-once
(ADR-CONCURRENCY-002); this ADR is about *completeness* (no event is dropped), not
*ordering* or *uniqueness*. It does not claim exactly-once and does not need to.

**Enforcement.**

- `postgres.TestCursor_WebhookWriteIsMonotonic` and `…PolicyWriteIsMonotonic`
  write a higher then a lower sequence and assert the cursor holds at the higher —
  the exact live exploit, now a passing regression test (it failed against the
  blind write, regressing to 5).
- `arch.TestCursorWriters_AreMonotonic` parses each writer's own function body and
  fails the build if it drops the `GREATEST` guard or assigns `last_seq` directly
  from `EXCLUDED` — verified to bite on reintroduction of the blind form.
- `postgres.TestAppenderConcurrentSerialization` (pre-existing) enforces the
  gapless-prefix property the cursor depends on.

## Residual risks

- **Cross-chain ordering is not provided and not claimed.** Cursors and seqs are
  per chain; there is no global order across chains, and none is needed —
  subscriptions and policies are evaluated per event. A consumer that assumes a
  global order across different objects is mistaken; the platform never promised
  one.
- **Delivery order to a receiver is not guaranteed.** Dispatch is concurrent and
  retried with backoff, so event 7's POST may arrive before event 6's. Each
  delivery carries its `(chain_id, seq)`; a receiver that needs per-object order
  must sort on it. This is a contract to document for consumers, not a defect in
  the cursor.
- **Restore must be a single consistent snapshot.** The cursor's correctness
  assumes `webhook_cursor`/`policy_cursor` and `audit_event` are restored from one
  atomic snapshot. A restore that captured cursors after events (cursor ahead of
  the log) would skip the missing range. This is the province of
  ADR-TENANCY-006 (recovery is a verification boundary); a periodic check that no
  cursor exceeds its chain head would make it self-detecting and is noted as
  future hardening.
- **First-append genesis race.** Two concurrent first events on a brand-new chain
  can transiently collide on the unique `(chain_id, seq)` and roll back one
  governance transaction (fail-closed, retryable). No duplicate seq, no lost
  event; a caller-side retry smooths it. Noted, not a correctness hole.

## The invariant

A cursor is a watermark over a gapless committed prefix, and a watermark only
rises. The relay and the consumer advance only past events they have durably
enqueued, over a log whose per-chain sequence is commit-ordered by the chain-head
lock; the stored cursor is the maximum ever reached and can never be rewound. No
committed event is silently dropped.
