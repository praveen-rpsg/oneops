# ADR-TENANCY-009 — A privileged mutation is confined to the work it was given

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-003/004 (ownership re-derived — **class reopened by this ADR**), ADR-TENANCY-005 (replay owns no authority), ADR-TENANCY-008 (operational tooling in scope), ADR-CONCURRENCY-006 (the retry budget this defect refilled), **EVR-001** |

## Context

From the Trust Register audit, recorded in EVR-001. Entries 5 and 6 claim
"privileged-worker ownership drift" as an eliminated class: every privileged
consumer re-derives the owner instead of trusting what it was handed. That was
verified on the dispatcher and the policy executor.

The replay worker is a further privileged consumer with two branches.
`replayWindow` is correct — it reads the audit log and applies
`domain.SameOwner`, and its comment says exactly why. The **by-id branch** was
not:

```go
return w.ops.Requeue(ctx, job.DeliveryIDs)   // ids came from the request body
```

```sql
UPDATE webhook_delivery SET status='pending', retry_count=0, next_attempt_at=now()
 WHERE id = ANY($1)      -- privileged pool; no owner, not even the job's webhook
```

Proven live with two tenants: naming a victim's delivery id in a replay of the
attacker's own webhook reset it from `dead_letter`/`retry_count=3` to
`pending`/`retry_count=0`. The delivery was resurrected **and** its retry budget
refilled (ADR-CONCURRENCY-006), so the victim's subscriber received it again.

The outbound send is still ownership-checked by the dispatcher, so this is a
cross-tenant **write and integrity** primitive rather than a disclosure. Delivery
ids are content-derived and therefore computable (ADR-CONCURRENCY-003), which
raises the exposure from blind guessing to derivation for anyone who knows the
webhook and chain.

**Why the original enforcement failed:** it named the consumers then known. This
is the third consecutive audit to find that same cause.

## Decision

**A privileged mutation of tenant-owned rows is confined to the work the caller
was given, and the confinement is a required parameter rather than an optional
filter.**

1. `Requeue(ctx, webhookID, ids)` — the webhook is required, and the statement
   filters `WHERE id = ANY($1) AND webhook_id = $2`. The caller proved that
   webhook is theirs at the API boundary on the tenant-scoped pool, and a
   delivery is reachable only through the webhook it belongs to. Both callers —
   the replay worker and the single-delivery retry endpoint — pass the webhook
   they were routed for.

2. **The guard derives its subject set from the registry.**
   `TestPrivilegedMutations_AreScopedToAnOwner` reads
   `postgres.TenantOwnedTables` and fails any privileged `UPDATE`/`DELETE` on one
   of those tables that is keyed on a caller-supplied id set without an owning
   predicate. A table added to the registry is swept automatically.

## Consequences

**What is now guaranteed.** No privileged consumer mutates tenant-owned rows
outside the work it was handed, across every table in the tenancy registry.

**What is *not* claimed.**

- The guard proves an owning predicate is **present**, not that it is
  **correct**. A statement could name one and scope it wrongly.
- `Requeue` is confined to the webhook; it does not re-derive the *event* owner
  from the audit log. That check remains at send time in the dispatcher, which is
  where the outbound effect happens — this ADR confines the write, it does not
  move the send check.
- The retention sweep is deliberately owner-agnostic (ADR-TENANCY-008) and is
  excluded by the id-set criterion.

## Evidence

Before: `1 row affected`, victim `dead_letter/3` → `pending/0`.
After: `0 rows affected`, victim unchanged.

Full suite green under `-race` against real PostgreSQL, all 19 packages.

## Enforcement

- `arch.TestPrivilegedMutations_AreScopedToAnOwner` — registry-derived sweep.
- `postgres.TestReplayRequeue_CannotTouchAnotherOwnersDeliveries` — the live
  exploit as a regression test.
- `httpapi.TestDeliveryInspection`/retry — asserts the requeue is scoped to the
  webhook in the route.

Mutation-verified: unscoping the requeue fails both the architecture sweep and
the integration test.

Two false positives were found and fixed while writing the sweep: `UPDATE
webhook` matched inside `UPDATE webhook_delivery` (substring collision), and
`status = ANY($2)` in the retention sweep matched a bare `= ANY($` test. Both are
recorded in the test comments — this is the third sweep in this programme to need
that tuning, and it is cheaper than the enumerated guards it replaces.
