# ADR-TENANCY-007 — The schema invariants the ownership model depends on are validated at startup

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-001 (row-level isolation), ADR-TENANCY-004/006 (audit authority, recovery) |

## Context

Every ownership guarantee rests on the schema. Row-level security confines the
request path; a mandatory `tenant_id` makes ownership total; the resolver's joins
assume those columns exist. Migration safety asks whether schema evolution can
weaken any of this without anyone noticing.

It can. Attacked against the running service: with `ROW LEVEL SECURITY DISABLED`
on `configuration_object` — the shape a bad migration or a single operator
`ALTER` produces — a tenant-scoped connection read another tenant's rows. Total
cross-tenant disclosure, and nothing at runtime noticed: the platform started
normally and served. Confidentiality depended entirely on the schema retaining a
property that no code checked.

The same class covers a nullable `tenant_id` (ownership becomes optional), a
dropped policy, an owning role that stops being FORCEd, and a binary deployed
ahead of its migrations by a rolling upgrade or an interrupted migration —
querying columns and policies that do not yet exist.

## Decision

**The binary validates, at startup, that the schema still enforces the ownership
model, and refuses to run when it does not.**

`SchemaValidator.Validate` runs before the server binds and checks, for every
table in the canonical `TenantOwnedTables`:

- row-level security is **enabled** (else isolation is off) **and forced** (else
  the owning role bypasses it), with a policy present;
- `tenant_id` exists and is **NOT NULL** (else ownership is optional);

and, once for the database:

- every embedded migration is **applied** (`migrate.Pending` is empty), so a
  binary is never running ahead of its schema.

Any problem refuses the boot, each logged with the offending table. This runs
before the ownership-graph validation (ADR-TENANCY-006), because that validation
queries columns this one proves exist. Runtime resolution continues to fail
closed independently, so a schema weakened after startup still cannot resolve an
owner it should not.

## The list is one source of truth, cross-checked against the live schema

`TenantOwnedTables` is defined once in production code and used by both startup
validators and the tests. That alone is not enough — a list can be edited to
match a mistake. So an integration test enumerates every table that carries a
`tenant_id` in the live schema and fails the build if any is absent from the list
or unprotected. A future migration that adds a tenant-owned table but forgets its
row-level security cannot pass CI, whether or not someone remembers to update the
list.

This closed a gap in the first version of this very change: the validators
originally listed six of the twelve protected tables. The live cross-check is
what surfaced the other six — the guard against schema drift caught drift in the
guard itself.

## Consequences

**Startup does a fixed set of catalogue queries** — cheap, and independent of
data volume, unlike the ownership-graph scan.

**Supported and unsupported upgrade paths are now explicit.** Applying all
migrations then starting the new binary is supported. Starting a binary ahead of
its migrations is refused (`migrate.Pending`). A migration that drops a required
invariant is refused at the next boot. A migration rolled back below what the
binary requires is refused. None silently degrades.

**Runtime never compensates for an unsupported schema.** The validator does not
repair — it cannot know whether a disabled policy was a mistake or an
in-progress change — it names the problem and stops, and the operator decides
with the platform down.

**Residual risk.** The check is a point-in-time snapshot at boot: an operator who
disables RLS on a *running* platform is not caught until the next start. The
integration suite proves the leak such a change causes, and the guidance is that
schema changes affecting these tables are deployed as migrations, which run at
boot behind this validation. A continuous catalogue check could be added later;
it is out of scope here, where the boundary is migration safety.

## The invariant

The ownership model is only as strong as the schema beneath it. The binary knows
which schema properties it requires — row-level security enabled and forced, a
mandatory tenant_id, its migrations applied — and refuses to run against a schema
that does not provide them, rather than serve on a foundation that has silently
given way.
