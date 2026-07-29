# EVR-007 — Leader step-down (entry 16)

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Trust Register entry** | 16 |
| **Associated ADR** | ADR-CONCURRENCY-003 |
| **Confidence** | **VALIDATED** — no sibling. ECL-2 → **ECL-5**; maturity **L1 → L3**. |

## Ranking

Scored **7.05**, third: enforcement integration-only (Level 1), distributed-
systems correctness.

## Original claim and evidence

A demoted leader used only to *log* the lock loss and kept running its workers,
giving a permanent two-leader overlap. Closed by running workers under a
leadership context cancelled the instant the advisory lock is lost.

## Fresh evidence

Original evidence reproduced directly:

```
--- PASS: TestRunAsLeader_DemotedLeaderStopsWorkersAndReElects (5.06s)
```

## Sibling search

The mechanism has **two halves**, and only the first was ever enforced:

1. the leader must cancel the leadership context — one line in `ops.campaign`,
   covered by the integration test above;
2. **every worker must honour that cancellation** — never swept. A worker whose
   loop ignores `ctx.Done()` keeps running after demotion and cancelling
   achieves nothing for it.

All seven worker `Run` loops were checked: dispatcher, relay, replay, retention,
policy consumer, policy executor, integrity sweeper. **All seven observe
`ctx.Done()`. No sibling instance.**

## Class status — CLASS CLOSED · ECL-5 · maturity L1 → L3

Enforcement is now tree-derived: any type with a `Run(ctx context.Context) error`
loop is a worker, and its loop must observe cancellation. A new worker is covered
the day it is written.

## Mutation verification

| Control | Result |
|---|---|
| A worker stops observing cancellation | fails, naming `retention.go` |
| Break the worker-signature detector | fails: *"no worker Run loops found; the sweep would be vacuous"* |

## Residual risk

- The sweep proves a loop *observes* cancellation, not that every blocking call
  inside `RunOnce` is context-aware; a worker could still block past a demotion
  inside a single pass.
- The step-down window remains **bounded, not zero** (up to `leaderWatchInterval`,
  5s), made safe rather than eliminated by idempotent production and the atomic
  claim — unchanged from ADR-CONCURRENCY-003 and already recorded in the
  register's "guarantees stated, not overstated".
