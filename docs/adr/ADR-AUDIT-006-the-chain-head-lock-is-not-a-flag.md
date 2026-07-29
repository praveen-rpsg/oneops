# ADR-AUDIT-006 — The chain-head lock is a method, not a flag

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-AUDIT-004 (the append step sequence), ADR-CONCURRENCY-004 (gapless prefix / no lost events), ADR-TENANCY-003/004 (audit-derived ownership), **EVR-004** |

## Context

From the Trust Register audit recorded in EVR-004. Entry 11's implementation was
correct — one append path, following ADR-AUDIT-004's sequence exactly. Its
*enforcement* was not: an integration test with no architecture tier, guarding an
invariant expressed as a boolean argument.

```go
ReadChainHead(ctx, tx, chainID string, forUpdate bool)
```

Everything downstream rests on that argument being `true`. The per-chain `seq` is
commit-ordered *because* of the `FOR UPDATE`; that ordering is what makes the
committed log a gapless prefix (ADR-CONCURRENCY-004), and the log's authority is
what makes ownership re-derivable (ADR-TENANCY-003/004).

Proven live:

```
with the lock:    12 concurrent appends -> seqs [1..12]
without it:       two transactions both read last_seq=0, both claim seq 1,
                  tx1 commits, tx2 is rejected — the second event is lost
```

The `(chain_id, seq)` unique constraint makes this fail-closed: the losing
transaction errors rather than corrupting the chain. The consequence is lost
governance operations, not a forged history — worth stating precisely, because it
bounds the severity.

## Decision

**The locking read is a distinct method. The unsafe form cannot be produced by
forgetting an argument.**

1. `ReadChainHeadForUpdate(ctx, tx, chainID)` is the locking read and the only
   one the appender's port declares. `ReadChainHead(ctx, tx, chainID)` remains
   for read-only verification and carries no flag.

2. **A tree-derived sweep enforces it.**
   `arch.TestAuditAppend_TakesTheChainHeadLock` fails if any production file
   calls the non-locking read, and if any file that appends to the audit log does
   not take the lock. It replaces an enforcement tier that did not exist.

The principle is the one ADR-GOV-002 used to remove a destructive repository
method: *you cannot call what cannot be expressed*. A boolean makes the wrong
choice invisible at the call site; a name makes it a reviewable act.

## Consequences

**What is now guaranteed.** No production code can read the chain head without
serialising on it, and no append path can exist without doing so — enforced over
the whole tree rather than by one integration test.

**What is *not* claimed.**

- The non-locking read still exists for verification paths. The sweep forbids it
  in production files; it remains callable in principle.
- ADR-AUDIT-004's boundary with ADR-AUDIT-003 remains **UNRECOVERABLE** in the
  corpus. This ADR validates and enforces the behaviour; the attribution defect
  belongs to the constitutional process, not to engineering.
- The genesis race is unchanged: fail-closed and retryable.

## Evidence

Before: the invariant was a boolean with no architecture tier.
After: a named method plus a whole-tree sweep; the original evidence reproduces,
and the lock is proven load-bearing by a deterministic two-transaction
interleaving.

Full suite green under `-race` against real PostgreSQL, all 19 packages, two
consecutive runs.

## Enforcement, mutation verification and self-validation

- Appender switched to the non-locking read → sweep fails with two diagnostics.
- Correct code → passes; the sweep strips `ReadChainHeadForUpdate(` before
  matching `ReadChainHead(`, because the short name is a substring of the long
  one. Without that step every correct call site would have been flagged.
- Appender detector broken → *"no audit append path found; the sweep would be
  vacuous"*.

Two defects were found and fixed in this audit's own test, and are recorded
because verification weaknesses are first-class: the attack was initially
*probabilistic* (12 goroutines, asserting fewer than 12 commit) and is now a
hand-stepped two-transaction interleaving; and creating the genesis head inside
both transactions deadlocked them on the unique key, which is the genesis race
rather than the invariant under test, so the head is now seeded and committed
first.
