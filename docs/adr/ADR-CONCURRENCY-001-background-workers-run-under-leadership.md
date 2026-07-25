# ADR-CONCURRENCY-001 — Background workers run on one instance, under leadership

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-003 (execution ownership) |

## Context

The trust model was verified under normal execution, replay, restore, schema
evolution and operator action — all assuming one process. The deployment does
not run one process. The Helm chart ships `replicaCount: 2`, and every replica
runs its own copy of every background worker: the relay, the dispatcher, the
policy consumer and executor, the replay and retention workers, and the
integrity scheduler.

Those workers are singletons by construction. The relay advances a per-chain
cursor with a read-modify-write (`GetCursor` → `ListEvents` → `SetCursor`) and no
lock. `ClaimDue` is a plain `SELECT ... WHERE status IN ('pending','failed')`
with no atomic claim — no `FOR UPDATE SKIP LOCKED`, no transition to a claimed
state. There is no leader election, no advisory lock, nothing that makes one
instance the writer.

Attacked with two replicas against one database. One governance event produced
**two signed webhook deliveries** — the relay double-produced the delivery rows,
and both were dispatched. The same race double-executes policy actions, whose
side effects (outbound HTTP: provisioning, notification) are not idempotent. A
second, separate failure surfaced immediately: two replicas booting at once race
in `migrate.Up`, and one crashed on a duplicate-key error creating
`schema_migrations`.

At-least-once delivery can be a deliberate contract, but this was not it: it was
an unbounded, unacknowledged duplication that no consumer was told to expect, and
double execution of state-changing actions.

## Decision

**Exactly one instance runs the background workers at a time, chosen by a
PostgreSQL session advisory lock. Every instance serves the HTTP API.**

The request path is safe concurrently — every handler is request-scoped and
tenant-confined — so all replicas serve it. The workers are gated:

- At startup each instance calls `ops.RunAsLeader`, which attempts
  `pg_try_advisory_lock` on a dedicated connection. The instance that acquires
  it runs the workers and holds the lock for its lifetime; the others become
  standbys and run none.
- The lock lives with its connection. If the leader crashes, PostgreSQL releases
  the lock automatically; a standby, retrying on an interval, acquires it and
  starts the workers. Verified: the leader was killed, a standby logged
  promotion, and a post-failover event was delivered exactly once.
- If the leader's lock connection fails but the process survives, it detects the
  lost ping and stops behaving as leader rather than risk two active leaders.

Migrations are serialised on their own advisory lock, so concurrent boots no
longer race: the losers wait, then find the migrations applied and skip them.
Verified: two replicas booting simultaneously both reach healthy.

## Consequences

**Delivery and execution are now exactly-once in steady state and at-least-once
only across a failover** — a promoted standby may re-run work the dead leader
had claimed but not marked complete, which is the correct and bounded trade for
availability. The authoritative resolver (ADR-TENANCY-003/004) makes a re-run
safe with respect to ownership; it does not make an outbound action idempotent,
so consumers of webhooks and policy actions should still treat delivery as
at-least-once across leader changes. That contract is now honest and narrow
rather than silent and unbounded.

**No new infrastructure.** Leadership and migration serialisation both use
PostgreSQL advisory locks, fitting the single-database architecture. There is no
external coordinator to run, secure or fail.

**Enforcement.** `ops.TestLeaderLock_MutualExclusion` proves only one holder at a
time and that the lock is re-acquirable after release (the failover path). An
architecture test fails the build if a worker's `Run` is launched directly in a
goroutine in the composition root instead of being registered and started under
`ops.RunAsLeader` — the exact regression that reintroduces double execution.

**Residual risks.**

- **Failover re-execution window.** Work the dead leader claimed but did not
  finish is re-run by the new leader. Bounded and safe for ownership; not
  idempotent for outbound side effects. Making delivery idempotent end to end
  (a claimed-state transition, or consumer-side dedup on `delivery_id`) is
  possible future work.
- **A standby is not instantaneous.** Between a leader's death and a standby's
  next retry (interval-bounded), no worker runs — delivery pauses, it does not
  fail. Acceptable for asynchronous delivery; tunable.
- **Advisory locks are per-database.** All replicas must share one PostgreSQL
  database, which is already the architecture. A future multi-region or
  multi-primary topology would need a different mechanism, and this ADR would be
  revisited.

## The invariant

Work that is a singleton by construction is run as a singleton by the platform.
Exactly one instance holds leadership and runs the background workers; the others
serve requests and stand ready to take over. Concurrency is made safe by
election and serialisation, not assumed away.
