# ADR-TENANCY-012 — A privileged cross-tenant READ requires an explicit tenant predicate

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-03 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-002 (isolation is a property of wiring), ADR-TENANCY-009 (privileged **mutations** are scoped to their work — this is its read-side analogue), ADR-TELEMETRY-001 (pluggable telemetry interface), ADR-CONCURRENCY-006 (the at-least-once/duplicate-over-loss doctrine Decision 2 below restates for notifications) |

## Context

E3.1 (alert rules + a leader-gated evaluator) added the platform's first
privileged, cross-tenant background worker that **reads** tenant-owned rows in
order to decide something about one tenant at a time, rather than to act
identically on every row it sees (the rollup worker) or to fan out unchanged
labels (the retention sweep, the dispatcher's audit-owner resolution).

Every existing tenant-owned repository — `TelemetryStore.QueryRange` included
— carries no `tenant_id` predicate in its SQL at all. That is correct **only**
because every such repository is built exclusively from the tenant-scoped
pool: row-level security supplies the missing filter, and
`QueryRange`'s own doc comment says so explicitly ("isolation is enforced by
row-level security on this store's tenant-scoped connection"). The evaluator
runs on the PRIVILEGED pool — the same pool the rollup worker, the collector
scheduler and the webhook dispatcher already use, and which holds
`BYPASSRLS` by design (ADR-TENANCY-002 §"isolation is a property of wiring").
Calling `QueryRange` from that pool would filter a rule's telemetry read on
nothing but `asset_id`/`metric`, trusting that no two tenants' rows can ever
match them — an assumption that happens to hold today only because
`asset_id` is a platform-minted ULID, not because anything enforces it.

This was caught and fixed during E3.1's own construction (not a live
exploit against a running deployment — no Trust Register EVR is attached to
this ADR), but it is exactly the same shape of gap ADR-TENANCY-009 closed for
privileged **writes**: an operation that serves every tenant from one
connection, where the ambient isolation control (RLS) has been deliberately
switched off, and where nothing in the type system distinguishes a query
that is safe on that connection from one that is not. ADR-TENANCY-009 states
the mutation half of the rule. This ADR states the other half so the next
privileged read does not have to rediscover it.

## Decision

### 1. A privileged connection's read of tenant-owned data must carry `tenant_id` as an explicit SQL predicate, sourced from the same authoritative row the rest of the operation trusts — never assumed from RLS.

`domain.TelemetryRepository.QueryRangeForTenant(ctx, tenantID, assetID, metric, from, to)`
is `QueryRange`'s privileged-pool counterpart: same table, same shape, one
additional required parameter, and `WHERE tenant_id = $1 AND asset_id = $2 AND
metric = $3 ...` in the SQL itself. The evaluator passes `rule.TenantID` —
read off the `alert_rule` row `EnabledRules` returned, itself written under
RLS at rule-creation time — never a value guessed, cached across rules, or
taken from ambient context. This mirrors exactly how the rollup worker's
`tenant_id` "flows straight through" from the row it aggregates
(`telemetry_rollup_worker.go`'s own doc comment) rather than being chosen by
the worker: the evaluator never *decides* whose data to read, it *reads what
the row in front of it says*, the same non-decision ADR-TENANCY-009 requires
of a privileged write.

`QueryRange` itself is unchanged and remains correct: it is never called from
the privileged pool, and nothing about this decision claims RLS is
insufficient for a tenant-scoped connection. The rule is narrower and sharper
than "always filter by tenant": it is "a connection that has RLS switched off
must not pretend it does not."

**This will recur, and each occurrence must repeat this pattern rather than
reinvent it.** Every future privileged consumer that reads tenant-owned data
to decide something per-tenant needs the same explicit predicate:

- **E4** (event correlation) — correlating alerts/events into incidents from
  a privileged, cross-tenant worker must never let one tenant's correlation
  window pull in another tenant's events.
- **E9** (reports & dashboards) — any aggregation engine that runs
  cross-tenant and answers a query "for tenant X" is the identical shape.
- **E14** (executive/reporting rollups, if built cross-tenant) — same shape
  again.

### 2. The evaluator's transition write is ordered notify-then-persist: duplicate over silent loss.

`alerting.Evaluator.evaluateRule` enqueues the transition's `Notification`
**before** calling `RecordTransition`. If the process crashes or is demoted
between the two, the next tick still sees the pre-transition `LastState`,
re-detects the same real-world transition, and enqueues a second
notification. Persisting first and crashing before the notification would
instead lose the alert silently and permanently — nothing would ever retry
it, because the stored state would already say the transition happened.

Between "the operator sees one alert twice" and "the operator never sees a
real alert at all", this codebase has already chosen the first for every
other at-least-once delivery path it has (ADR-CONCURRENCY-006's retry
budget on webhook/notification delivery is the same doctrine applied to
*sending*; this is the same doctrine applied to *deciding to send*). The
residual is bounded and named: a duplicate can occur only in the narrow
crash/demotion window between the two writes, and only produces at most one
extra notification per real transition, never an unbounded stream (E3.1 is
transition-only — a repeated tick that observes no change writes and sends
nothing at all).

## Consequences

**What is now guaranteed.** The evaluator's telemetry read cannot return
another tenant's samples regardless of what `asset_id` collision might exist
now or in the future, and this no longer depends on `asset_id` uniqueness
holding by accident.

**What is not claimed.** `QueryRangeForTenant`'s predicate is *present*; this
ADR does not prove every future caller will *supply the right value* for it —
that is exactly the same residual ADR-TENANCY-009 names for a mutation's
owning predicate, restated for a read.

**Follow-up — not built here, tracked.** ADR-TENANCY-009's mutation guard
(`arch.TestPrivilegedMutations_AreScopedToAnOwner`) derives its subject set
from `postgres.TenantOwnedTables` and sweeps every privileged `UPDATE`/
`DELETE`. No equivalent sweep exists for privileged **reads** — a future
change adding a second privileged, cross-tenant `SELECT` against a
`TenantOwnedTables` entry with no `tenant_id` predicate would compile, pass
every existing test, and read correctly, exactly the failure mode
`arch.TestServerWiringUsesTenantScopedPool`'s own doc comment describes for
wiring. A build-failing architecture guard — `SELECT ... FROM <tenant-owned
table>` inside a function that also, in the same file, constructs a store
over the privileged pool, with no `tenant_id =` in the statement — should be
added the next time a privileged reader is introduced (E4 is the next
candidate). This ADR records the obligation; it does not close it.

## Evidence

Both mutation-verified by hand, reverted clean afterward:

- Removing the `tenant_id = $1` predicate from `QueryRangeForTenant`'s SQL
  (neutralised to `($1 = $1)`, keeping the parameter bound so the statement
  still compiles) made `TestTelemetryStore_QueryRangeForTenantIsolatesTenant`
  return **both** tenants' samples for tenant A's own query — proving the
  predicate, not an accident of unique `asset_id`s, is what isolates it.
- `TestEvaluator_CrossTenantTelemetryIsolation` (20 tenants sharing one
  `asset_id`/`metric`, evaluated concurrently) fails the moment the
  evaluator is made to pass any single fixed tenant id instead of each
  rule's own — caught immediately, 20/20 calls misattributed.

Full suite green under `-race`; full `internal/store/postgres` integration
suite green against real PostgreSQL.

## Enforcement

- `postgres.TestTelemetryStore_QueryRangeForTenantIsolatesTenant` — the
  store-level regression test for Decision 1.
- `alerting.TestEvaluator_CrossTenantTelemetryIsolation` — the
  orchestration-level regression test for Decision 1 (many tenants,
  concurrent dispatch).
- `alerting.TestEvaluator_ConcurrentEditDuringTransitionIsHandled` — proves
  Decision 2's ordering: a notification already sent survives a losing CAS
  on the transition write.
- No architecture-tier (build-failing) guard exists for either decision yet
  — see the Follow-up above.
