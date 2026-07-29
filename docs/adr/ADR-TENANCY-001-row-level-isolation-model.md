# ADR-TENANCY-001 — Row-level tenant isolation, enforced by the database

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-24 |
| **Decider** | Acting CTO |
| **Supersedes** | none |
| **Related** | ADR-AUDIT-003 (append-only audit), ADR-AUDIT-005 (atomic constitutional mutation) |

## Context

The platform parsed a `tenant` claim from the bearer token and discarded it.
No table carried a tenant, and no query filtered on one. Every authenticated
caller could read every row.

`ADR-TENANCY-001` follows the tenant registry (migration
`20260727000001_tenancy.sql`), which established the entity and made the
authentication boundary reject claims it does not recognise. That closed the
edge. It did not isolate the data: a single missing predicate anywhere below
the middleware still crosses tenants.

Three isolation models were considered.

**Database per tenant.** Strongest isolation, and the easiest to reason about
under audit. Rejected: it multiplies migration, backup and connection cost by
the tenant count, and the platform's own audit-integrity sweeper would need to
fan out across every database. The governance corpus targets tens to low
hundreds of enterprise tenants, which is precisely the range where this model
is most expensive and least necessary.

**Schema per tenant.** Middle ground. Rejected: migrations must run N times and
partially-applied migrations across schemas are a genuinely bad failure mode.
`audit_event` is already LIST-partitioned, which compounds the complexity.

**Row-level with `tenant_id` + PostgreSQL row-level security.** Chosen.

## The problem with RLS in this codebase

Two properties of the current design make naive RLS ineffective or harmful.

**1. The application owns its tables.** PostgreSQL exempts a table's owner from
its row-level policies unless the table is declared `FORCE ROW LEVEL SECURITY`.
The control plane connects as `oneops`, which owns every table. Enabling RLS
without forcing it would produce policies that are never evaluated — isolation
that reads as present in the schema and is absent at runtime. That is worse
than no RLS, because it invites the belief that the problem is solved.

**2. Background workers legitimately cross tenants.** The event dispatcher, the
relay, the retention worker, the policy consumer and executor, and the audit
integrity sweeper all process work for every tenant from one process. Under
forced RLS with a request-scoped tenant setting, every one of them would read
zero rows and silently stop — the integrity sweeper in particular would report
healthy precisely because it could no longer see anything to check.

A policy of the form `USING (tenant_id = current_setting(...) OR
current_setting(...) IS NULL)` solves the worker problem by failing **open**:
any code path that forgets to set the tenant sees everything. The failure mode
of that design is total cross-tenant exposure arising from an omission, which
is the exact class of bug RLS exists to prevent.

## Decision

Row-level isolation, enforced by the database, with two distinct database
roles and `FORCE ROW LEVEL SECURITY`.

1. **Every tenant-owned table carries `tenant_id text NOT NULL REFERENCES
   tenant(tenant_id)`**, backfilled to `system` for all existing rows. The
   foreign key is the point: a tenant that does not exist cannot own a row.

2. **Policies are fail-closed.** `USING (tenant_id = current_setting('app.tenant_id'))`
   with no `IS NULL` escape. A connection that has not declared a tenant sees
   nothing. An omission causes an empty result, not a leak.

3. **Two effective roles, two pools, one connection string.**
   - `oneops_app` — a `NOLOGIN` role holding no `BYPASSRLS` and owning nothing.
     Request-scoped connections assume it with `SET ROLE` on acquire, so every
     query below the authentication boundary is subject to the policies.
   - The migrating/owning role (superuser in development, table owner in
     production) keeps `BYPASSRLS`. Background workers use it directly. Their
     cross-tenant access is explicit, greppable and auditable rather than an
     implicit consequence of connecting as the owner.

   Separating the two identities is what lets the policy stay fail-closed.
   Without it, the only way to keep the workers running is an escape hatch in
   the policy, and that escape hatch is the vulnerability.

   **Revision (2026-07-24).** This ADR originally specified two connection
   strings, one per role. Implementation showed that unnecessary.
   `pgxpool.Config.BeforeAcquire` receives the request context, so a single
   pool can assume `oneops_app` and apply the resolved tenant at acquire time
   — no changes to any repository, and no second production secret to
   provision, rotate and leak. A `NOLOGIN` role cannot be used to connect at
   all, which is a stronger default than a second credential.

   The residual risk is that `SET ROLE` is reversible within its session, so an
   attacker able to execute arbitrary SQL could `RESET ROLE` and escape the
   policies. That is accepted: arbitrary SQL execution is already total
   compromise of a system where the same process holds owner credentials, and
   every query in the codebase is parameterised. Should the platform later
   split the workers into their own process, that process should hold the only
   owner credential and the API process should connect as `oneops_app`
   directly. This decision does not foreclose that; it declines to pay for it
   before the process split exists to justify it.

4. **Tables excluded from RLS**, because they hold no tenant data:
   `schema_migrations`, `tenant` itself (administering tenants is a
   platform-level operation, guarded by the admin permission), and the worker
   cursors `webhook_cursor` and `policy_cursor`, which are platform processing
   state rather than customer data.

5. **`idempotency_key` is tenant-scoped.** An idempotency key presented by one
   tenant must never replay another tenant's stored response.

## Consequences

**Deployment changes.** Two connection strings are required instead of one, and
the roles must be provisioned before the application starts. This is a
migration-time and Helm-secret change, and it is the main cost of this
decision. It is accepted because the alternative — one role, forced RLS, and a
policy escape hatch — reintroduces the failure mode being designed out.

**The system tenant remains.** Rows written before tenancy belong to `system`,
and a token asserting no tenant resolves there, so single-tenant deployments
keep working unchanged.

**Audit history is unaffected in kind.** `audit_event` gains `tenant_id` like
every other table. Its append-only triggers are untouched: RLS restricts which
rows a connection may see, and says nothing about whether they may be changed.
The two controls are independent and both remain in force.

**Partitioning is unaffected.** `audit_event` stays LIST-partitioned by
`chain_id`. RLS on a partitioned parent propagates to its partitions, so no
change to the partitioning scheme is required.

**This ADR is not self-executing.** It records the model. The schema change,
the role split, the pool split and the negative tests that prove isolation are
separate changes, and none of them may be described as complete until a test
demonstrates that a connection bound to tenant A retrieves zero rows belonging
to tenant B.
