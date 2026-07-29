# ADR-TENANCY-006 — Recovery is a verification boundary

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-001..005 (isolation, wiring, execution ownership, audit authority, replay) |

## Context

Execution security re-derives ownership from the governed object and its audit
log (ADR-TENANCY-003/004), and startup already refuses a database whose audit
log and objects disagree. Both assume the tenant an object names still exists.

Backup/restore validation attacked that assumption. A restore is not
necessarily internally consistent: operators restore single tables, snapshots
from different points in time, or partial PITR, and `pg_restore
--disable-triggers` suspends the foreign keys that normally hold ownership
together. The concrete case: the `tenant` table is restored from an older
snapshot that predates a tenant, while that tenant's `configuration_object`,
`audit_event` and `webhook` rows remain. The data is now owned by a tenant that
does not exist.

Verified against the running service. The platform started and served normally.
The foreign key on `webhook_delivery.tenant_id` did prevent the orphaned event
from being delivered — no disclosure — but it did so by failing the relay's
insert in an infinite five-second retry loop, logging a foreign-key error
forever, with nothing detecting the inconsistency and nothing surfacing it to an
operator. The trust property held by accident of a constraint; the platform's
posture was "serving, silently broken."

## Decision

**Recovery is a verification boundary, not a repair mechanism. A restored
platform proves ownership before it accepts traffic, and refuses to guess.**

1. **Startup validates the whole ownership graph and refuses to boot on any
   inconsistency.** `OwnershipValidator.Validate` runs before the server binds:
   it reports the ADR-TENANCY-004 divergence and, for every tenant-owned table —
   `configuration_object`, `audit_event`, `webhook`, `webhook_delivery`,
   `policy`, `policy_execution` — any row whose tenant is not in the registry.
   Foreign keys enforce this during normal operation, but a restore with
   triggers disabled bypasses them and `audit_event` carries no foreign key at
   all (it is partitioned), so startup re-verifies what the constraints normally
   guarantee. Every problem is logged with an example key; the platform refuses
   to boot until a human repairs it.

2. **Runtime fails closed independently, in case startup is bypassed.**
   `ResolveEventOwner` now joins through to the `tenant` table: the resolved
   owner must be a live tenant. An object owned by a dropped tenant is
   unresolvable (`ErrEventNotFound`) and its work is refused, without relying on
   the startup scan having run.

## Consequences

**The platform will not silently repair.** It cannot know which of two
disagreeing records is the corrupt one, so it names the inconsistency and stops.
Repair is an operator decision, taken with the platform down, not a guess made
while serving.

**Each restore scenario now has a defined outcome, verified:**

| Restore | Outcome |
|---|---|
| Tenant registry only, dropping a referenced tenant | **Startup refuses**, naming every dangling table; runtime unresolvable if bypassed |
| Audit-only (no governed object) | Execution refuses (`ErrEventNotFound`); a genuine deleted object is the same shape and is correctly inert |
| Queue-only (delivery/execution, no audit) | Execution refuses — the event is not in the log |
| Object-only (no audit) | Boots; an object with no governance operations has no execution path |
| Mixed-snapshot divergence | Startup refuses (ADR-TENANCY-004) |

**Suspension is out of scope here and stated so.** Whether a *suspended* (as
opposed to missing) tenant's committed work should still produce outbound
actions is a behavioural policy, not an ownership one: the tenant exists and
ownership is unambiguous. The authentication boundary already refuses a
suspended tenant's requests; background outbound work for suspended tenants is
left for a dedicated decision rather than folded into recovery validation.

**Residual risk.** Startup validation is a full-table scan of six tables; on a
very large estate it adds seconds to boot. That is acceptable for a verification
boundary and can later be scoped or indexed if it matters. The scan proves
consistency at a point in time; it does not prevent an operator from corrupting
the database while the platform is down and skipping the check — but the runtime
resolver then still fails closed, which is the defence-in-depth the two layers
exist to provide.

## The invariant

A restored database is untrusted until proven consistent. The platform
establishes ownership for its entire estate before it serves, refuses to start
when any ownership is dangling or ambiguous, and — independently — refuses at
execution to act for an owner that is not a live tenant. Recovery verifies; it
never repairs, and it never guesses.
