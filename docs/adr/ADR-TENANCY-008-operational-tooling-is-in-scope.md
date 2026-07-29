# ADR-TENANCY-008 — Operational tooling is in scope for the trust model

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-003/004 (execution ownership, audit authority), ADR-TENANCY-006/007 (recovery, schema invariants) |

## Context

The trust model is verified under normal execution, replay, restore and schema
evolution. Operational tooling — the scripts, maintenance jobs and privileged
paths a trusted operator runs — is the last surface. The objective is not to
defend against a malicious DBA, who already holds the database; it is to prevent
a *trusted* operator from accidentally breaking an architectural invariant.

Inventory of the privileged operational surface:

| Capability | Kind | Ownership posture |
|---|---|---|
| `controlplane` binary | the platform | All privileged writes go through the shared framework: request path on the tenant-scoped pool, workers on the owning pool but outbound only via the authoritative resolver |
| `make db-backup` / `scripts/db-backup.sh` | recovery | `pg_dump`; read-only of the database |
| `make db-restore` / `scripts/db-restore.sh` | recovery | Full-database restore; any inconsistency it produces is caught at next startup |
| `make dr-drill` / `scripts/dr-drill.sh` | maintenance | Backs up, restores into a throwaway database, verifies; never touches the live database |
| `make db-reset` | emergency (dev only) | Drops and recreates the dev schema |
| `make migrate-hash` / `migrate-validate` | maintenance | Read-only over the migration files |
| Retention worker | background | Deletes terminal (`delivered`/`dead_letter`) deliveries by age, uniformly across tenants; performs no per-tenant action |
| Admin APIs (tenants, webhooks, policies, replay, dead-letter retry, integrity run) | privileged | On the tenant-scoped pool (ADR-TENANCY-002 wiring test); confined by row-level security |

There is deliberately **no separate repair or maintenance CLI** that writes to
the database outside the framework.

## Attack

Attacked before changing anything: the audit-log immutability guarantee.

ADR-TENANCY-004 makes `audit_event` authoritative *because it is append-only* —
a committed row's `tenant_id` cannot be rewritten, so the resolver's cross-check
against the governed object cannot be forced to agree with a forgery. That
guarantee is enforced by two triggers. A trigger is one operator `ALTER` away
from gone.

Demonstrated: with `trg_audit_event_no_row_mutate` dropped — the shape a repair
script or emergency fix produces — `UPDATE audit_event SET tenant_id = 'attacker'`
succeeded, rewriting ownership on a committed event. And the schema validator
(ADR-TENANCY-007) did not check for the trigger, so the platform started
normally. The foundation of audit authority could be removed by an operator and
nothing noticed.

## Decision

**Operational tooling is production code and is held to the trust model, and the
schema invariants the model depends on include the audit-immutability triggers.**

1. **Startup validates audit immutability.** `SchemaValidator` now verifies that
   `audit_event` and every one of its partitions carries both append-only guards
   — the row-level guard against UPDATE/DELETE and the statement-level guard
   against TRUNCATE — and refuses to boot when any is missing. Verified live:
   with the guard dropped, the platform refuses to start, naming the parent and
   the partition; restored, it boots. This closes the gap the same way
   ADR-TENANCY-007 closed RLS: a load-bearing schema property is checked before
   the platform trusts what depends on it.

2. **No new privileged binary may exist unregistered.** A binary under `cmd/` is
   where an operator capability — a bulk importer, a queue-repair tool, a
   backfill — would write outside the shared framework and bypass tenant
   stamping and authoritative ownership. An architecture test fails the build on
   any binary not in a registered inventory, forcing a reviewer to state how it
   obtains ownership: through the same stores and resolver as production, never
   by encoding a security decision in a script.

## Consequences

**Restore and manual SQL are already covered.** Whatever an operator does to the
database directly, the startup validators re-verify the ownership graph
(ADR-TENANCY-006), audit-vs-object agreement (ADR-TENANCY-004) and the schema
invariants including immutability (ADR-TENANCY-007, this ADR) before the platform
serves. Operational corruption that would weaken the model is refused at the next
boot rather than trusted.

**Residual risks, stated:**

- **Bulk import via direct SQL can mis-attribute, but cannot leak.** An import
  that omits `tenant_id` defaults it to `system` (a valid tenant), so the row is
  owned by `system` rather than its intended tenant. This is a data-integrity
  error, not a confidentiality one: `system`-owned data is reachable only by a
  `system`-scoped token, and a wrong non-system `tenant_id` is rejected by the
  foreign key or caught as dangling/divergent at startup. Correct attribution of
  a bulk import is the operator's responsibility; the platform guarantees only
  that mis-attribution cannot become a cross-tenant leak.

- **A change made to a *running* platform is caught at the next start, not
  continuously.** Dropping the audit guard or disabling RLS on a live system
  opens a window until the next boot. The guidance — schema-affecting changes
  ship as migrations, which run at boot behind the validators — bounds this, and
  the integration suite proves the exploit each such change would enable. A
  continuous catalogue check is possible future work; it is out of scope for
  migration/operational safety, whose boundary is the boot.

## The invariant

Operational tooling encodes no security decision. Ownership belongs to the shared
stores and the authoritative resolver, and every operator path either goes
through them or leaves the platform in a state the next startup rejects. The
schema properties the model depends on — row-level security, mandatory ownership,
and audit immutability — are verified before the platform trusts them, whoever
last touched the database.
