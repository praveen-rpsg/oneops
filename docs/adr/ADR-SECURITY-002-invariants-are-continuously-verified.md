# ADR-SECURITY-002 — A security invariant is a property held continuously, not an event that happened at startup

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-007 (schema invariants validated at startup), ADR-TENANCY-001/002 (row-level isolation), ADR-TENANCY-003/004 (audit-derived ownership), ADR-AUDIT-003 (append-only audit), ADR-CONCURRENCY-003/006 (context cancellation as the stop mechanism) |

## Context

ADR-TENANCY-007 established that the platform refuses to boot unless the schema
still enforces the ownership model: row-level security enabled **and** forced,
with a policy, on every tenant-owned table; `tenant_id` present and `NOT NULL`;
every required migration applied; the audit log's append-only triggers in place.
It is a good check, and it is correct.

It runs once.

The validator's own comment names the threat precisely:

> "A migration or an operator can weaken any of these, and **nothing else at
> runtime would notice** — a disabled RLS policy is a silent, total cross-tenant
> leak."

The threat it names is a *runtime* event. The enforcement it provides is
*boot-time only*. The process then serves for days or weeks on a verdict that
was true when it started. Any of a routine migration, an operator's `ALTER`, a
restore from an older dump, or a rollback can weaken the boundary at 09:01 and
the instance that validated it at 09:00 will never know.

This was attacked live, against real PostgreSQL and the running binary.

**Damage.** With `ALTER TABLE configuration_object DISABLE ROW LEVEL SECURITY`
applied after startup, a connection bound to tenant A read tenant B's row
through `NewTenantScopedPool` — the same pool constructor the request path uses:

```
control (invariant intact):          tenant A sees 0 of tenant B's rows
after runtime weakening (RLS off):   tenant A sees 1 of tenant B's rows
```

**Detection.** Against the running control plane, throughout:

```
readyz=200   healthz=200   GET /v1/artifacts=200
log lines mentioning rls / isolation / schema: 0
```

The platform served tenant traffic, advertised itself as ready to the load
balancer, and said nothing, while its most load-bearing security boundary was
gone. A re-run of the *existing* validator reported the problem immediately — so
the platform was fully capable of detecting this and simply never looked.

Two further results from the same investigation, recorded because negative
results are evidence too:

- **`NO FORCE ROW LEVEL SECURITY` did not leak.** `oneops_app` is not the table
  owner, so the policy still applies to it. Only `DISABLE` is exploitable. The
  boundary is stronger than the validator's comment implies; the validator is
  right to check both, and the *class* is unchanged.
- **PostgreSQL restart recovery already works.** Restarting the database under
  the running binary produced correct behaviour with no code change: the leader
  detected the lost advisory lock, cancelled its workers, re-campaigned, and
  re-acquired leadership; the pool reconnected; readiness returned to 200. The
  previously-recommended "PostgreSQL failover" investigation was largely a
  non-finding, and this ADR exists instead because the evidence said so.

The failure class is: **a boundary whose truth can change at runtime, but which
is only ever verified once.** Tenant isolation is the instance that was
exploitable; the class also covers audit immutability, the ownership columns, and
migration completeness — every invariant ADR-TENANCY-007 checks.

## Decision

**An invariant the platform depends on is re-verified for the life of the
process, and a breach fails closed exactly as it does at startup.**

1. **A sentinel turns the check from an event into a property.** `ops.Sentinel`
   re-runs a check on an interval (`ONEOPS_SCHEMA_SENTINEL_INTERVAL_SECONDS`,
   default 30s) and holds the verdict. The schema sentinel re-runs *the same*
   `postgres.SchemaValidator` the startup sequence uses — two different checks
   would let boot and runtime disagree about what "valid" means.

2. **Unverified is not healthy.** A sentinel that has not completed a check
   reports an error. "Unknown" reads as "do not serve"; the alternative opens the
   gate on a boundary this process has never confirmed.

3. **A failed check is not a breach.** If the check cannot run — the database is
   unreachable mid-restart — the previous verdict is carried, the failure is
   counted and logged, and the platform does not flip to breached. Treating a
   blip as a breach would take every replica out of service at once and teach
   operators to ignore the signal. Readiness already covers dependency loss.
   Symmetrically, a failed check never *clears* a known breach.

4. **A breach closes the tenant-data surface.** The `/v1` group is gated ahead of
   authentication: when the boundary that makes tenant identity meaningful is
   gone, a perfectly valid credential is not a reason to serve tenant data.
   Requests get `503` with `Retry-After`, and the body does **not** name the
   broken invariant — that would disclose the exact weakness to an
   unauthenticated caller.

5. **A breach removes the instance from the load balancer.** Readiness fails on
   breach as well as on dependency loss, so an instance refusing every request
   stops advertising itself as ready.

6. **A breach stops the background workers.** The relay, dispatcher and executor
   read and write tenant-owned rows through the same privileged path; gating HTTP
   alone would be a gate with a hole in it. `ops.RunWhileHealthy` runs each worker
   under a context cancelled on breach — the same mechanism demotion already uses
   (ADR-CONCURRENCY-003), so a worker stopped this way releases its claims and
   records its outcomes exactly as on a leadership change (ADR-CONCURRENCY-006).

7. **The process stays alive and diagnosable.** `/healthz`, `/readyz` and
   `/metrics` remain outside the gate. Refusing to serve is fail-closed; exiting
   would crash-loop the fleet and destroy the evidence an operator needs. This is
   the runtime equivalent of "refusing to start": it refuses to *serve*.

8. **Repair restores service without a redeploy.** When the invariant is
   repaired the sentinel clears, readiness returns, the gate opens, and the
   workers restart.

## Consequences

**What is now guaranteed.** The window between weakening a schema invariant and
the platform refusing to serve on it is bounded by the sentinel interval (30s by
default) rather than being unbounded. A breach is loud: one `ERROR` naming every
problem, `oneops_invariant_breached 1`, readiness red, and every tenant request
refused.

**What is deliberately *not* claimed.**

- **This does not prevent the weakening.** An operator with DDL rights can always
  disable row-level security; nothing inside the application can stop that. The
  platform's obligation is to notice and stop trusting itself — which is what
  changed. Reads that occur inside the detection window are not prevented.
- **The window is not zero.** Up to one sentinel interval of traffic can be
  served through a weakened boundary. Shortening the interval shortens the
  window; it never removes it.
- **This covers the invariants `SchemaValidator` checks**, not every property the
  platform relies on. The sentinel is general (`ops.Sentinel` takes any check),
  and further invariants should be added to it rather than to a new mechanism.
- **A determined operator can still cause a leak** by weakening the boundary and
  reading within the window, or by disabling the sentinel. This is a detection
  and containment control, not a defence against the database superuser.

**Operational change.** A schema problem now takes instances out of service
instead of being ignored. That is the intended behaviour and it is a real
availability trade: a bad migration that drops a constraint will now stop the
fleet serving rather than silently leak. `oneops_invariant_breached` is the
signal to alert on.

**Architectural note.** The sentinel is started outside `ops.RunAsLeader`, which
the leadership wiring test otherwise forbids. That is deliberate and now
explicit: it is not singleton *work* but each replica's supervision of itself —
running it only on the leader would leave every follower serving through a
boundary it never re-verified. The allowance is recorded in
`arch.perReplicaSupervisors`, which requires both "per-replica by necessity" and
"no duplicable side effects".

## Evidence

Live exploit, before the change:

- RLS disabled post-startup → tenant A read 1 of tenant B's rows through the
  tenant-scoped pool (0 before).
- Running binary throughout: `readyz=200`, `healthz=200`,
  `GET /v1/artifacts=200`, zero log lines about isolation or schema.

Live re-attack, after the change, against the rebuilt binary:

| | before breach | breached | after repair |
|---|---|---|---|
| `/readyz` | 200 | **503** | 200 |
| `/v1/artifacts` | 200 | **503** | 200 |
| `/healthz` | 200 | 200 | 200 |
| `oneops_invariant_breached` | 0 | **1** | 0 |

with the log line:

```
ERROR SENTINEL BREACH: an invariant proven at startup no longer holds;
refusing to serve until it is repaired
  invariant="ownership-model schema invariants" problems=1
  detail="configuration_object does not have row-level security enabled — tenant isolation is off"
```

and the workers stopping (`retention worker stopped`, `replay worker stopped`)
then restarting after repair (`event relay started`, `event dispatcher started`)
with no redeploy.

Negative results: `NO FORCE` is not exploitable for the non-owner app role;
PostgreSQL restart recovery already worked (leadership re-election observed
live).

Full suite green under `-race` against real PostgreSQL.

## Enforcement

- `arch.TestSchemaValidator_IsRunContinuouslyNotOnlyAtStartup` — a sentinel
  exists, is started, and re-runs the *same* startup validator.
- `arch.TestInvariantBreach_FailsClosedOnEveryTenantDataPath` — the `/v1` group
  is gated, the gate is wired, readiness consults the sentinel, and the workers
  run under `RunWhileHealthy`.
- `arch.TestSentinel_TreatsUnverifiedAsUnhealthy` — unverified fails closed, and
  a failed check is distinguished from a breach.
- `ops.TestSentinel_*` — detection after startup, recovery after repair,
  unverified-is-unhealthy, a check error neither creates nor clears a breach.
- `ops.TestRunWhileHealthy_*` — the worker stops on breach, restarts on repair,
  and returns on context cancellation so shutdown never hangs.
- `httpapi.TestInvariantGate_*` — refuses while breached with `Retry-After`,
  does not disclose which invariant broke, serves while healthy, and an unwired
  gate is inert.
- `postgres.TestRuntimeInvariant_RLSDisabledAfterStartupIsDetectedContinuously`
  — the live exploit as a regression test, asserting a *named* breach (not merely
  "unhealthy", which an unverified sentinel also reports).
- `postgres.TestRuntimeInvariant_AuditImmutabilityDropIsDetectableButUnwatched`
  — the same class on the audit-immutability triggers.

All four architecture guards were mutation-verified: removing the `/v1` gate,
un-gating the workers, never starting the sentinel, and making unverified read as
healthy each fail the build with the diagnostic naming this ADR. The gate
assertion is checked against comment-stripped source, because a commented-out
`rt.Use(s.invariantGate)` still contains the substring and would otherwise pass.
