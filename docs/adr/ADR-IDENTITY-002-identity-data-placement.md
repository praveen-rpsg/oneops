# ADR-IDENTITY-002: Physical placement of identity data relative to row-level security

| | |
|---|---|
| **Status** | **RATIFIED** |
| **Date** | 2026-07-29 |
| **Author** | Principal Architect / Lead Engineer |
| **Resolves** | **G-2** — identity data placement relative to RLS · story `OPS-S014` |
| **Derives from** | **ADR-IDENTITY-001 (ratified, immutable)** — §8.2 fixes placement; this ADR transcribes it into physical schema |
| **Related** | ADR-TENANCY-001 (row-level isolation), ADR-TENANCY-002 (isolation is a property of wiring), ADR-TENANCY-007 (schema invariants validated at startup) |
| **Makes no new architectural decision** | Every placement here is dictated by ADR-IDENTITY-001 §8.2 |

---

# 0. Two corrections to the inputs

Reported rather than worked around, because an ADR written against either would be
unimplementable.

### 0.1 ⛔ There is no Prisma schema in this repository

The brief names *"Existing Prisma schema"* as an authoritative input. **It does not exist.**

```
find . -name "schema.prisma"        → no results
grep -ci prisma go.mod              → 0
grep -ci prisma web/package.json    → 0
```

The schema is **raw PostgreSQL DDL** in `internal/store/migrate/sql/` — 15 migrations, checksummed
by **Atlas** (`atlas.sum`, validated in CI by `atlas migrate validate`). **This ADR is written
against that.** No ORM is introduced.

### 0.2 ⚠️ ADR-IDENTITY-001 §7.2 names the wrong session mechanism

The ratified sequence diagram shows `SET LOCAL app.tenant_id = <tenant_id>`. The implementation is:

```go
// internal/store/postgres/pool.go:68
"SELECT set_config('app.tenant_id', $1, false)"   // on acquire
"SELECT set_config('app.tenant_id', '', false)"   // on release
```

Session-scoped via `set_config(..., false)` on connection acquire/release in
`NewTenantScopedPool`, **not** transaction-scoped `SET LOCAL`.

**G-1 is immutable and is not reopened.** The isolation property it asserts is unaffected — the
setting is scoped and cleared either way. **This ADR transcribes the mechanism that exists**, and
records the discrepancy so it is not propagated into code. A correction to G-1's diagram is a
documentation matter for its author, not a blocker here.

---

# 1. Executive Summary

ADR-IDENTITY-001 §8.2 settled placement: **only `organization`, `tenant` and `user` are global;
every other identity table carries `tenant_id` and joins `TenantOwnedTables`.** This ADR turns
that into DDL, constraints, indexes and a migration order.

**The consequence that matters:** because every tenant-scoped identity table joins
`TenantOwnedTables`, the existing registry-derived guards and the RLS bootstrap cover them the
moment they are added. **No guard is written, changed or weakened by this ADR.**

**One naming decision is forced by PostgreSQL:** `user` is a reserved word — `CREATE TABLE user`
is a syntax error. The physical table is **`app_user`**. The domain entity remains `User`.

---

# 2. Physical Data Model

Conventions are taken from the existing migrations, not invented: `text` primary keys,
`timestamptz NOT NULL DEFAULT now()`, `row_version bigint NOT NULL DEFAULT 1`, checks named
`ck_<table>_<field>`, uniques `uq_<...>`, indexes `ix_<abbrev>_<cols>`.

## 2.1 `organization` — global

```sql
CREATE TABLE IF NOT EXISTS organization (
    org_id      text        PRIMARY KEY,
    tenant_id   text        NOT NULL REFERENCES tenant(tenant_id),
    slug        text        NOT NULL,
    name        text        NOT NULL,
    status      text        NOT NULL DEFAULT 'active',
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_org_tenant  UNIQUE (tenant_id),
    CONSTRAINT uq_org_slug    UNIQUE (slug),
    CONSTRAINT ck_org_status  CHECK (status IN ('active', 'suspended')),
    CONSTRAINT ck_org_slug    CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$')
);
```

**`uq_org_tenant` is the 1:1 enforcement** required by ADR-IDENTITY-001 §4 (AC-1). The slug regex
mirrors `ck_tenant_slug` exactly so the two cannot diverge.

## 2.2 `app_user` — global

```sql
CREATE TABLE IF NOT EXISTS app_user (
    user_id     text        PRIMARY KEY,
    email       citext      NOT NULL,
    display_name text       NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'invited',
    row_version bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_user_email  UNIQUE (email),
    CONSTRAINT ck_user_status CHECK (status IN ('invited','active','suspended','deactivated'))
);
```

Requires `CREATE EXTENSION IF NOT EXISTS citext;` — case-insensitive email equality belongs in
the type, not in every query. **No `tenant_id`**: ADR-IDENTITY-001 §8.2 places `app_user` global,
because a person exists before and independently of any membership.

## 2.3 `membership` — tenant-scoped

```sql
CREATE TABLE IF NOT EXISTS membership (
    membership_id text        PRIMARY KEY,
    tenant_id     text        NOT NULL DEFAULT 'system' REFERENCES tenant(tenant_id),
    org_id        text        NOT NULL REFERENCES organization(org_id),
    user_id       text        NOT NULL REFERENCES app_user(user_id),
    status        text        NOT NULL DEFAULT 'active',
    row_version   bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_membership_org_user UNIQUE (org_id, user_id),
    CONSTRAINT ck_membership_status   CHECK (status IN ('active','revoked'))
);
```

**`tenant_id` is denormalised and is the RLS key** (ADR-IDENTITY-001 §6). `DEFAULT 'system'`
follows the existing convention and its recorded reason: a write path that is ever missed lands
in the system tenant as greppable evidence, rather than failing with a not-null violation
surfacing as a 500.

`uq_membership_org_user` makes a user's membership of an organisation singular — the row's
`status` carries revocation, so revocation is a state change and never a delete
(ADR-IDENTITY-001 §8.3).

## 2.4 `invitation` — tenant-scoped

```sql
CREATE TABLE IF NOT EXISTS invitation (
    invitation_id text        PRIMARY KEY,
    tenant_id     text        NOT NULL DEFAULT 'system' REFERENCES tenant(tenant_id),
    org_id        text        NOT NULL REFERENCES organization(org_id),
    email         citext      NOT NULL,
    token_hash    text        NOT NULL,
    status        text        NOT NULL DEFAULT 'pending',
    expires_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    redeemed_at   timestamptz,
    CONSTRAINT uq_invitation_token  UNIQUE (token_hash),
    CONSTRAINT ck_invitation_status CHECK (status IN ('pending','redeemed','revoked','expired'))
);
```

**`token_hash`, never the token.** The plaintext is returned once at issuance and is not
recoverable from the table — the same rule ADR-IDENTITY-001 §9.2 applies to API keys, applied
here because an invitation token is a bearer credential.

⚠️ **`status` must not default to `'pending'` on a table intended as a work queue.** It does
here, and `invitation` is **not** a queue — but the schema-derived guard
`TestEveryWorkQueue_HasAFencingToken` discovers queues by exactly that signature. See §7, C-4.

## 2.5 Sprint 2 and Sprint 3 tables — placement fixed, columns owned by their stories

| Table | Placement | Mandatory columns | Story |
|---|---|---|---|
| `team` | tenant-scoped | `tenant_id`, `org_id` | `OPS-E06` |
| `team_membership` | tenant-scoped | `tenant_id`, FK `team`, FK `membership` | `OPS-E06` |
| `role` | tenant-scoped | `tenant_id`, `org_id` | `OPS-E07` |
| `grant` | tenant-scoped | `tenant_id` | `OPS-E07` |
| `role_assignment` | tenant-scoped | `tenant_id` | `OPS-E07` |
| `session` | tenant-scoped | `tenant_id`, FK `membership` | `OPS-E10` |
| `api_key` | tenant-scoped | `tenant_id`, FK `membership`, `key_hash` | `OPS-E11` |

**This is not a deferral.** G-2 asks where identity data sits relative to RLS; that is answered
completely above — every one is tenant-scoped, carries `tenant_id`, and joins
`TenantOwnedTables`. Their remaining columns are their own stories' to choose, and choosing them
here would be inventing detail for tables whose behaviour is not yet specified.

---

# 3. Table Placement

| Table | Placement | RLS | In `TenantOwnedTables` | Authority |
|---|---|---|---|---|
| `tenant` | **Global** | ⛔ no | no | ADR-TENANCY-001 §4 — the registry itself |
| `organization` | **Global** | ⛔ no | no | ADR-IDENTITY-001 §8.2 |
| `app_user` | **Global** | ⛔ no | no | ADR-IDENTITY-001 §8.2 |
| `membership` | Tenant-scoped | ✅ | ✅ | ADR-IDENTITY-001 §8.2 |
| `invitation` | Tenant-scoped | ✅ | ✅ | ADR-IDENTITY-001 §8.2 |
| `team`, `team_membership` | Tenant-scoped | ✅ | ✅ | ADR-IDENTITY-001 §8.2 |
| `role`, `grant`, `role_assignment` | Tenant-scoped | ✅ | ✅ | ADR-IDENTITY-001 §8.2 |
| `session`, `api_key` | Tenant-scoped | ✅ | ✅ | ADR-IDENTITY-001 §8.2 |

## 3.1 Why the three global tables are not a hole in isolation

`organization` and `app_user` are reachable **only through the privileged pool**. They hold no
tenant data: an organisation row is a name, a slug and a tenant pointer; a user row is an email
and a status. **Every privileged mutation over them is already covered** by
`TestPrivilegedMutations_AreScopedToAnOwner`, which derives its subject set from
`TenantOwnedTables` and fails any privileged mutation keyed only on a caller-supplied id set.

**The resolution order is why they cannot be tenant-scoped.** `tenant_id` is discovered *by*
reading membership for a principal; a table needed to discover the key cannot itself be gated on
the key. Making `organization` tenant-scoped is a chicken-and-egg at every request.

---

# 4. RLS Design

## 4.1 The policy, unchanged

Identity tables adopt the existing policy verbatim — same name, same predicate, same
fail-closed behaviour:

```sql
ALTER TABLE %I ENABLE ROW LEVEL SECURITY;
ALTER TABLE %I FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON %I;
CREATE POLICY tenant_isolation ON %I
  USING      (tenant_id = current_setting('app.tenant_id', true))
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
```

**`FORCE` is not optional** — without it the table owner bypasses the policy, which the existing
migration's own header records. **`USING` governs read; `WITH CHECK` governs write**, so a tenant
cannot insert a row labelled with another tenant's id.

**Fail-closed is preserved.** `current_setting('app.tenant_id', true)` returns NULL when unset,
and NULL matches no row. The existing migration explicitly refused the
`OR current_setting(...) IS NULL` variant; identity tables inherit that refusal.

## 4.2 Bootstrap is a new migration, not an edit

The RLS bootstrap in `20260729000001_rls_policies.sql` iterates a **literal array of twelve table
names inside a `DO` block**. Atlas migrations are checksummed and immutable — `atlas.sum` and the
`migrations` CI job both fail on an edited file.

**Therefore M5 is a new migration running the same `DO` loop over the identity tables.** It is
not an amendment of the existing one.

## 4.3 Policy attachment points

Per Vol IV C6, Policy attaches to a boundary. Physically there are exactly three enforcement
points, and identity adds none:

| Point | Mechanism |
|---|---|
| **Database** | `tenant_isolation` RLS policy on every `TenantOwnedTables` member |
| **Connection** | `set_config('app.tenant_id', …, false)` on acquire; `''` on release (`pool.go:68,85`) |
| **Application** | `requirePermission` / `requirePlatformAdmin` middleware (ADR-AUTHZ-001) |

## 4.4 Authentication boundary

Ends at a verified OIDC subject. Verification is unchanged: RS256, JWKS through the SSRF-guarded
client, issuer and audience validated. **Authentication yields a subject, never a `tenant_id`.**

## 4.5 Authorization boundary

Begins where authentication ends and has one rule:

> **`tenant_id` is resolved from `membership`, never from the request.**

A tenant identifier in a header, path or body is a claim; the membership row is the fact. This is
ADR-TENANCY-004 — *authoritative ownership must be consistent* — applied to identity, and it is
enforced by AC-6.

---

# 5. Migration Plan

Expand → migrate → contract. Each ships its `rollback/` counterpart, as every migration here
already does. Order is mandatory: **M5 cannot precede M4**, because enabling forced RLS on a table
before the application sets `app.tenant_id` for it takes the platform down — the lesson recorded
in `20260728000001_tenant_columns.sql`.

| # | File | Content | Rollback |
|---|---|---|---|
| **M1** | `20260804000001_organization.sql` | `citext` extension; `organization`; indexes | drop table |
| **M2** | `20260804000002_organization_backfill.sql` | One `organization` per existing `tenant`; **idempotent** (`ON CONFLICT DO NOTHING`) | delete backfilled rows |
| **M3** | `20260804000003_app_user.sql` | `app_user`; indexes | drop table |
| **M4** | `20260804000004_membership.sql` | `membership`, `invitation`; indexes | drop tables |
| **M5** | `20260804000005_identity_rls.sql` | `DO` loop enabling RLS on `membership`, `invitation` | drop policies |
| **M6** | *(Go)* | `OrganizationTenantValidator` in `platformInvariants` | unregister |

**Same-commit requirements**, each enforced by an existing guard rather than by review:

- **M4 must add `membership` and `invitation` to `TenantOwnedTables` in the same commit.** The
  registry-derived guards then cover them immediately.
- **Any new endpoint must update `openapi.yaml` in the same commit** — the contract guard fails
  the build otherwise.
- **`atlas.sum` must be regenerated** (`make migrate-hash`), or the `migrations` CI job fails.

## 5.1 Backward compatibility

**No existing table is altered. No existing column changes type or nullability. No existing row
is rewritten.** Every migration is additive.

- **The system tenant** receives an organisation from M2 like any other — no special case, so no
  branch in the resolution path.
- **Pre-identity deployments keep working**: with no `membership` rows, resolution falls back to
  the system tenant exactly as today.
- **Rollback of M1–M5 returns the schema to its present state**, because nothing present was
  modified.

---

# 6. Constraints

## 6.1 Required constraints

| Constraint | Table | Enforces |
|---|---|---|
| `uq_org_tenant UNIQUE (tenant_id)` | `organization` | **The 1:1** — AC-1 |
| `FK organization.tenant_id → tenant` | `organization` | No organisation on a non-existent tenant |
| `uq_org_slug UNIQUE (slug)` | `organization` | Addressable, unambiguous slug |
| `ck_org_slug` | `organization` | Mirrors `ck_tenant_slug` |
| `uq_user_email UNIQUE (email)` *(citext)* | `app_user` | Platform-wide identity, case-insensitive |
| `FK membership.tenant_id → tenant` | `membership` | Ownership key points at a real tenant |
| `FK membership.org_id → organization` | `membership` | Membership of a real organisation |
| `FK membership.user_id → app_user` | `membership` | Membership by a real user |
| `uq_membership_org_user UNIQUE (org_id, user_id)` | `membership` | One membership per user per org |
| `uq_invitation_token UNIQUE (token_hash)` | `invitation` | Single-use token |
| **All `ck_*_status`** | all | Lifecycle states of ADR-IDENTITY-001 §8.3 |

## 6.2 Required indexes

| Index | Purpose |
|---|---|
| `ix_org_tenant ON organization(tenant_id)` | Resolution path org → tenant |
| `ix_membership_user ON membership(user_id, status)` | "which orgs may this user enter" — the login path |
| `ix_membership_tenant ON membership(tenant_id)` | RLS predicate support |
| `ix_membership_org ON membership(org_id, status)` | Member listing |
| `ix_invitation_email ON invitation(email, status)` | Redemption lookup |
| `ix_invitation_tenant ON invitation(tenant_id)` | RLS predicate support |

`ix_membership_user` is the one on the hot path: it is read on **every authenticated request**
that resolves a tenant. Its absence would put a sequential scan in front of the whole API, and
ADR-IDENTITY-001's `< 5ms p95` budget (AC via `OPS-S052`) would be spent before authorisation
began.

## 6.3 Prohibited

| Prohibition | Why | Guard |
|---|---|---|
| **No `org_id` on any `TenantOwnedTables` member other than identity tables** | Would create a second ownership key — CMR-D04 | AC-4 |
| **No `tenant_id` on `organization` used as an RLS key** | It is the mapping target, not the row's owner | §3.1 |
| **No plaintext token or key material in any column** | Bearer credentials are hash-only | AC-9 |
| **No `workspace` or `project` table or type** | ADR-IDENTITY-001 §4.1 — refused | AC-12 |

---

# 7. Verification Checklist — consistency against existing artefacts

| # | Check | Result |
|---|---|---|
| **C-1** | Placement matches ADR-IDENTITY-001 §8.2 exactly | ✅ three global, all others tenant-scoped |
| **C-2** | `tenant_id` remains the sole ownership key | ✅ no competing key introduced |
| **C-3** | RLS policy identical to the existing one | ✅ same name, predicate, `FORCE`, fail-closed |
| **C-4** | ⚠️ **`invitation.status` defaults to `'pending'`** — the exact signature `TestEveryWorkQueue_HasAFencingToken` uses to discover work queues from the schema | **Expect the guard to fire on M4.** It is correct to fire: the signature *is* ambiguous. `invitation` is not claimed work and needs no fencing token. **Resolution: add it to the guard's justification map** — the pattern EVR-005 established, where a derived subject set carries a justified residue and a stale justification also fails. **Do not weaken the guard, and do not rename the column to evade it.** |
| **C-5** | RLS bootstrap is a new migration, not an edit | ✅ §4.2 — Atlas checksums forbid editing |
| **C-6** | No existing table, column or row altered | ✅ additive only |
| **C-7** | `user` reserved-word collision | ✅ physical table is `app_user` |
| **C-8** | Excluded tables still excluded (ADR-TENANCY-001 §4) | ✅ `schema_migrations`, `tenant`, `webhook_cursor`, `policy_cursor` untouched |
| **C-9** | Session mechanism as implemented | ⚠️ `set_config(..., false)`, not `SET LOCAL` — §0.2 |
| **C-10** | Input names a Prisma schema | ⛔ **does not exist** — §0.1 |

**Two contradictions reported (C-9, C-10); one predicted guard interaction (C-4). No guard is
weakened by any of them.**

---

# 8. Engineering Acceptance Criteria

| # | Criterion | Verified by |
|---|---|---|
| **AC-1** | Exactly one organisation per tenant and one tenant per organisation | `uq_org_tenant`; integration test asserting a second insert fails |
| **AC-2** | Organisation and tenant are created in one transaction or not at all | Integration test: induced failure after the tenant insert leaves zero rows |
| **AC-3** | **A member of org A cannot read or mutate org B's rows** | Integration test that **attempts the breach** through the tenant pool and asserts failure |
| **AC-4** | No second ownership key exists | New architecture guard: no `org_id` column on a non-identity `TenantOwnedTables` member; **mutation-tested both directions** |
| **AC-5** | `membership` and `invitation` are in `TenantOwnedTables` and RLS-protected | Existing registry-derived guards, unmodified |
| **AC-6** | `app.tenant_id` is set from resolved membership, never from the request | New architecture guard: no handler-derived value reaches `set_config`; **mutation-tested both directions** |
| **AC-7** | `membership.tenant_id` always equals its organisation's tenant | `OrganizationTenantValidator` — boot gate **and** continuous sentinel |
| **AC-8** | Organisation administration requires platform admin | Existing route-derived guard, extends automatically |
| **AC-9** | No invitation token is recoverable from the database | Table inspection test: `token_hash` only, no plaintext column |
| **AC-10** | Invitation responses do not disclose whether an email is registered | Integration test comparing known and unknown addresses |
| **AC-11** | Migrations apply and roll back on a **populated** database | Migration test |
| **AC-12** | No `workspace` or `project` table or type exists | New architecture guard over the migration directory; **mutation-tested both directions** |
| **AC-13** | `invitation` is justified in the work-queue guard, not exempted by renaming | Guard justification map; a stale justification fails the build |
| **AC-14** | `atlas.sum` is current | `migrations` CI job |
| **AC-15** | Tenant resolution adds **< 5 ms p95** | Benchmark on the login path with `ix_membership_user` present |

**AC-4, AC-6 and AC-12 are the new guards.** Each must be mutation-tested in both directions
before merge: revert the fix and it must fail; break the detector and it must fail as vacuous,
never pass by finding nothing.

---

**STATUS: RATIFIED**
