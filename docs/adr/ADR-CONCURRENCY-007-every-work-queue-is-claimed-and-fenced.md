# ADR-CONCURRENCY-007 — Every work queue is claimed atomically and fenced

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-CONCURRENCY-002 (atomic claim — **class reopened by this ADR**), ADR-CONCURRENCY-005 (claim fencing — **class reopened by this ADR**), ADR-CONCURRENCY-001 (leadership), ADR-CONCURRENCY-004 (monotonic cursors), ADR-SECURITY-003 (the preceding audit, same failure mode of enforcement) |

## Context

This ADR comes from an audit of the Trust Register rather than from a defect
report. Entries 14 (atomic claim) and 18 (claim fencing) were recorded as
eliminated **classes**. They were verified on `webhook_delivery` and
`policy_execution`, and their architecture tests named those two queues.

`webhook_replay_job` is a third claimed resource. It had neither mechanism:

- no `claimed_at` column at all;
- `ClaimPendingJobs` was a plain `SELECT … WHERE status='pending'`;
- the worker then issued a **separate** unconditional
  `UPDATE … WHERE id=$1` to mark the job running;
- `UpdateJob` had no fencing token.

That is verbatim the shape ADR-CONCURRENCY-002 eliminated, quoted from its own
text:

> ClaimDue was a plain SELECT of pending/failed rows with no claim, so two
> workers running at once — the overlap window during a leadership handoff or a
> lock-loss — both selected the same rows and both performed the outbound action.
> Leadership makes that window small; it does not close it.

The replay worker is leader-gated, so this is precisely that bounded overlap.

**Proven live** against real PostgreSQL:

```
two workers claiming simultaneously:
  replay jobs handed out: 8 distinct, 8 claimed by BOTH workers

a worker that no longer owns a job writing its outcome:
  owner recorded   completed / 42 events replayed
  stale worker then wrote  failed / 0 / "stale worker's verdict"   ← accepted
```

Every pending job went to both workers, and a stale write silently replaced the
owner's verdict.

### Why the original enforcement failed

Both ADRs were enforced by architecture tests that asserted on the queues then
known. An architecture test that enumerates known instances cannot close a
class; it can only pin the instances already known. This is the second
consecutive audit to find the same failure mode (ADR-SECURITY-003 found it in
the invariant and egress guards), which makes it a pattern in how this programme
wrote enforcement, not an isolated oversight.

## Decision

**Every work queue is claimed by an atomic compare-and-set and completed under a
fence, and the guards derive the queue set from the schema rather than naming
it.**

1. **`webhook_replay_job` gains `claimed_at`**, nullable and unset for existing
   rows — the same convention the other two queues use, so pre-existing jobs stay
   claimable.

2. **`ClaimPendingJobs` becomes the claim.** The status transition happens *in*
   the claim, under `FOR UPDATE SKIP LOCKED`, returning the claimed rows. The
   worker's separate "mark running" write — the window in which a second worker
   could select the same pending job — is gone.

3. **`UpdateJob` is fenced on the token** and returns `ErrStaleClaim` when the
   job is no longer held under it, which the worker logs and discards. This is
   the same fence `MarkResult` uses.

4. **The guards sweep the schema.** `TestEveryWorkQueue_HasAFencingToken` derives
   the queue set from the migrations — any table whose `status` defaults to
   `'pending'` is work someone claims — and requires `claimed_at` on each.
   `TestEveryQueueClaim_IsAtomicAndFenced` requires each queue's claim to stamp
   the token and take `FOR UPDATE … SKIP LOCKED`. A fourth queue cannot be added
   without both. `TestEveryCursor_IsWrittenMonotonically` gives entry 17 the same
   treatment, deriving cursors from the schema instead of listing two.

## Consequences

**What is now guaranteed.** No two workers hold the same unit of work on any
queue the schema defines, and no worker can record an outcome for work it no
longer holds. The guarantee is over the queue *set*, not over three named tables.

**What is *not* claimed.**

- **A replay job whose worker dies is not recovered.** `ClaimPendingJobs` selects
  only `pending`, so a job left `running` is neither retried nor terminated — it
  is stuck. This is recorded, tested (`TestReplayJob_StuckRunningIsNotReclaimed`
  documents it) and left open deliberately: unlike the delivery queue, a stuck
  replay job produces no repeated outbound effect, so it is a liveness gap rather
  than a correctness one. Giving it lease recovery means giving it retry
  accounting too (ADR-CONCURRENCY-006), which is a larger change than this audit
  should smuggle in.
- **The claim is exclusive; the work is still at-least-once.** A replay that
  crashes mid-run may have enqueued some deliveries; those are idempotent by
  content-derived id (ADR-CONCURRENCY-003), so the effect is bounded, not absent.
- **The schema sweep defines a queue as "status defaults to 'pending'".** A
  future queue that expresses its pending state differently would not be detected.
  That is stated rather than implied.

## Evidence

Live exploit, before: 8 of 8 jobs claimed by both workers; a stale write
overwrote `completed/42` with `failed/0`.

Live re-attack, after: `8 distinct, 0 claimed by BOTH workers`; the stale write
returns `ErrStaleClaim` and the job keeps `completed/42`.

Full suite green under `-race` against real PostgreSQL, all 19 packages.

## Enforcement

- `arch.TestEveryWorkQueue_HasAFencingToken` — schema-derived; fails if any
  queue lacks `claimed_at`, and fails if no queues are detected so it cannot go
  vacuous.
- `arch.TestEveryQueueClaim_IsAtomicAndFenced` — every claim stamps the token and
  is exclusive; a plain SELECT of pending rows fails the build.
- `arch.TestEveryCursor_IsWrittenMonotonically` — schema-derived cursor sweep.
- `postgres.TestReplayJobClaim_IsExclusive` — the live exploit as a regression
  test.
- `postgres.TestReplayJobUpdate_IsFenced` — a stale token changes nothing.
- `postgres.TestReplayJob_StuckRunningIsNotReclaimed` — documents the residual.

Mutation-verified: reverting the claim to a plain SELECT, and removing
`claimed_at` from the migration, each fail the build with the diagnostic naming
this ADR.

Two false positives were found and fixed while writing these sweeps — matching
across SQL literals split by string concatenation, and treating `ReleaseClaim`
as a claim because it mentions the working state in its `WHERE`. Both are
recorded in the test comments: a guard that cries wolf gets switched off, which
is worse than no guard.
