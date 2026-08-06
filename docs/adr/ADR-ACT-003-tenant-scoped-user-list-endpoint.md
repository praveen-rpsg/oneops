# ADR-ACT-003 — A tenant-scoped user-directory projection, `PermAdmin`-gated, joins RLS-confined membership to the global app_user table for display

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO (implementer session) |
| **Related** | ADR-IDENTITY-001/002 (`app_user` global, `membership` tenant-scoped — the two tables this story joins), ADR-ONCALL-001 (`OnCallScheduleRepository.OnCall`'s `LEFT JOIN app_user`, the join-safety precedent this story mirrors), ADR-NOC-001 (the read-only-projection / RLS-only-isolation pattern — "isolation is a property of wiring, not of a predicate"), ADR-NOC-006 (the same pattern applied to `AssetStore.Graph`, including its mutation "bites when loosened" isolation proof, which this story reproduces for `MembershipStore`), `docs/PLATFORM-BUILD-PLAN.md` E-ACT.0, E-ACT.1 (the confirmed contract gap this story closes) |

## Context

E-ACT.1 (ADR-ACT-001) wired the incident board's Assign action against
`GET /v1/admin/users` for its assignee picker, and recorded a confirmed gap
at the time: that route is `requirePlatformAdmin` — a strictly more
privileged, cross-tenant tier than the `PermAdmin` tenant-administration
permission every incident endpoint uses. A real tenant operator with
`PermAdmin` cannot reach it; only the local-dev `AuthEnabled=false` identity
(which happens to be platform-admin) makes the picker work out of the box.
E-ACT.4's on-call roster picker will need the identical capability. Both are
blocked on the same missing primitive: a way for a tenant administrator to
see who is available in *their own* tenant, without being handed the
platform-wide registry.

`GET /v1/admin/memberships?org_id=` already exists and is already
`PermAdmin`-gated and RLS-confined — but it requires the caller to already
know an `org_id`, and it returns bare `membership` rows (`user_id`, `status`,
timestamps) with no display name or email, because `membership` carries no
profile data of its own. A picker needs a name to show, not just an
identifier.

## Decision

### 1. One endpoint, one method, one narrow interface — no new table

`GET /v1/admin/tenant-users`, `requirePermission(auth.PermAdmin)` — the same
tier `GET /admin/memberships` and every other tenant-scoped administration
route in this package uses, and deliberately **not** `requirePlatformAdmin`:
`membership` is TENANT-OWNED and carries row-level security
(`postgres.TenantOwnedTables`), so administering or reading it is tenant
administration, exactly the reasoning `GET /admin/memberships` already
established. The handler (`Server.listTenantUsers`,
`internal/httpapi/handlers_tenant_users.go`) calls one new method,
`MembershipStore.ListActiveDirectory`, and assembles a narrower DTO
(`tenantUserDTO`: `user_id`/`email`/`display_name` only — no `status`, no
`row_version`, no timestamps) than `GET /admin/users`'s `userDTO`. Nothing is
written; nothing is persisted beyond the existing `membership`/`app_user`
rows. No new table, no new `internal/domain` entity beyond a plain
projection struct (`domain.TenantUserSummary`) — the census
(`internal/kg/extract/schema.TestCorpusCensus`) is unchanged, confirming
nothing was reified.

Following `nocEscalationReader`'s precedent (ADR-NOC-001 §3) rather than
widening `domain.MembershipRepository`: a narrow, package-local interface
(`tenantUserDirectory`, one method) is what the handler declares it needs,
wired via `Server.SetTenantUserDirectory`. This keeps the broad
`MembershipRepository` port (and its one existing fake,
`handlers_memberships_test.go`'s `fakeMemberships`) untouched, and keeps the
new capability's surface exactly as wide as its one consumer. Unlike
`nocEscalationReader`, this needed no *second* store instance:
`MembershipStore` has no privileged role anywhere in this codebase — it is
appPool-scoped by construction — so `main.go` wires the SAME
`membershipStore` variable for both `SetMemberships` and
`SetTenantUserDirectory`.

### 2. Row-level security is the ONLY isolation mechanism — no explicit tenant predicate, no privileged pool

Exactly ADR-NOC-001 §2 and ADR-NOC-006 §2's reasoning, applied to
`MembershipStore` instead of the NOC stores or `AssetStore`.
`ListActiveDirectory` runs on the same tenant-scoped `appPool`
(`postgres.NewTenantScopedPool`) `MembershipStore` already uses for every
other method; `membership` carries `FORCE ROW LEVEL SECURITY` with the
`tenant_id = current_setting('app.tenant_id', true)` policy
(`20260804000004_membership.sql`). The query carries no `WHERE tenant_id =
...` of its own, for the same reason no other `MembershipStore` method does:
the database supplies the filter before `app_user` is ever joined.

Proven live, not just argued:
`TestTenantUsersAPI_TenantIsolation` (`internal/httpapi/tenant_users_integration_test.go`)
seeds two tenants with their own organizations, users and active
memberships and asserts each tenant's directory contains exactly its own
members — never the other's, even though both users' `app_user` rows live
in the same global table. `TestTenantUsersAPI_TenantIsolation_BitesWhenLoosened`
re-runs the identical fixture against a router whose `MembershipStore` is
built over the PRIVILEGED pool (`postgres.NewPool`, no `app.tenant_id`
binding) instead of `appPool`, and requires the leak to occur there — the
mutation proof that the "real" isolation test is not vacuous
(ADR-NOC-006 §2's own pattern, reproduced here).

### 3. The `app_user` join is safe for the same reason `OnCall`'s already is

`app_user` carries no `tenant_id` and no row-level security by design
(ADR-IDENTITY-002 §3.1) — a person exists independently of any membership.
Joining it onto a query that reads a ROW-LEVEL-SECURED, tenant-owned table
first (`membership`) discloses nothing about `app_user` rows outside that
already-confined set: the join can only ever attach a profile to a `user_id`
the caller's own RLS-scoped `membership` read already surfaced.
`OnCallScheduleRepository.OnCall` established exactly this join
(`on_call_participant` LEFT JOIN `app_user`) for the identical reason,
already reviewed and already live. This story's version is a plain `INNER
JOIN`, not a defensive `LEFT JOIN` like `OnCall`'s: `membership.user_id`
carries its own `REFERENCES app_user (user_id)` foreign key
(`20260804000004_membership.sql`), so — unlike `on_call_participant`'s
roster, which `OnCall`'s own doc comment treats defensively — there is no
orphan case here to tolerate.

### 4. "Active member" requires BOTH an active membership AND an active account

`membership.status` is `active`/`revoked` only; it has no `suspended` state
of its own. `app_user.status` has `invited`/`active`/`suspended`/
`deactivated` (ADR-IDENTITY-001 §8.3). The query filters on
`m.status = 'active' AND u.status = 'active'` — narrower than the
CTO-authored story brief's literal text ("membership … filter status=
'active'"), and recorded here as a deliberate implementation decision rather
than silently assumed: a membership can be left active while the
underlying platform account is suspended (an administrator suspending a
person's login without walking every tenant revoking their memberships
individually), and such a person cannot currently act — they must not
populate an assignee/roster picker as if they could. If this reading is
wrong, revert to a plain `membership.status = 'active'` filter; the query
sites are `MembershipStore.ListActiveDirectory`'s single `WHERE` clause and
the flip is a one-line change with no schema impact either way.

### 5. Bound and ordering: keyset over `user_id`, the same shape `ListByOrg` already uses

`limit`/`after` keyset pagination, reusing `MembershipStore`'s existing
`clampMembershipPage` (default 50, cap 500 — the same bound `ListByOrg`
already enforces for the sibling `/admin/memberships` route). Ordered by
`m.user_id` ascending: a ULID, globally unique, and — because
`uq_membership_org_user` and `app_user`'s own FK together make "two
memberships for the same user in the same tenant" impossible — already the
natural, deterministic order for a collection of *people*, not memberships.
Proven against a real cursor walk with `limit=1`
(`TestTenantUsersAPI_Pagination`): three active members come back one at a
time in ascending order, and a page past the end is a clean empty list, not
an error.

## Alternatives considered

- **Widen `domain.MembershipRepository` with the new method.** Rejected:
  every existing implementer (`*postgres.MembershipStore`, and the handler
  test package's `fakeMemberships`) would need updating for a capability
  only one new handler consumes — the same over-broadening a narrow
  interface (`nocEscalationReader`, `escalation.MembershipChecker`) already
  avoids elsewhere in this codebase.
- **Reuse `GET /admin/memberships?org_id=` as-is, and resolve display names
  client-side via N `GET /admin/users/{id}` calls.** Rejected on three
  counts: it requires the caller to already know an `org_id` (a tenant
  administrator populating a picker should not need to discover their own
  organisation's identifier first), it is an N+1 pattern driven by roster
  size, and each of those N calls would hit the SAME `requirePlatformAdmin`
  gate this story exists to route around.
- **A second, privileged `AssetGraph`-style bulk join across every
  organisation's memberships.** Rejected: there is exactly one tenant to
  answer for per request (the caller's own), so a privileged, cross-tenant
  read would trade a correctly-scoped connection this story already has for
  a strictly more dangerous one, for no benefit (ADR-TENANCY-002).

## Consequences

**What is now guaranteed.** A caller with `PermAdmin` in a tenant gets, in
one bounded request, every user who is BOTH an active member of their own
tenant AND holds an active platform account — `user_id`, `email`,
`display_name` only — confined to their own tenant by row-level security
alone, proven live and proven to bite when that scoping is removed
(`TestTenantUsersAPI_TenantIsolation`,
`TestTenantUsersAPI_TenantIsolation_BitesWhenLoosened`). A suspended
account with an otherwise-active membership, and a revoked membership with
an otherwise-active account, are both excluded
(`TestTenantUsersAPI_ReturnsExactlyActiveMembers`). A brand-new, empty
tenant gets a clean `200` with `{"items":[]}` — never `null`, never a `500`
(`TestTenantUsersAPI_EmptyTenantIsCleanEmpty`). The contract is additive:
`/v1/admin/tenant-users` and its two new schemas (`TenantUser`,
`TenantUserList`) are the only change to `internal/httpapi/openapi.yaml`,
and `make contract-breaking` against `master` reports no breaking change.
`GET /v1/admin/users` (platform-wide, `requirePlatformAdmin`) is entirely
unchanged — this is a new, narrower, tenant-scoped sibling, not a
replacement.

**What is not claimed.** This is a v1 directory read, not a live feed —
nothing is cached, and two calls a millisecond apart can return different
answers if membership changes between them, the same property every other
projection in this package already has. The `u.status = 'active'` half of
the filter (Decision 4) is a judgment call made during implementation, not
a pre-approved product decision; it is called out explicitly here rather
than folded silently into the query so it can be revisited on review. The
frontend rewire of `web/src/users.ts`/`IncidentDetail.tsx`'s assignee picker
and E-ACT.4's roster picker onto this endpoint is explicitly DEFERRED to
E-ACT.4 rather than bundled here — this story's deliverable is the Go
endpoint E-ACT.1 and E-ACT.4 both depend on, not the frontend rewire itself.

## Evidence

- `internal/domain/membership.go` (`TenantUserSummary`) — the new,
  deliberately narrow projection type.
- `internal/store/postgres/membership_store.go` (`ListActiveDirectory`) —
  the query shape Decisions 2–5 describe.
- `internal/httpapi/handlers_tenant_users.go` — the narrow interface
  (`tenantUserDirectory`), the wiring method (`SetTenantUserDirectory`), the
  DTO, and the handler (`listTenantUsers`).
- `internal/httpapi/handlers_tenant_users_test.go` — DTO-assembly and
  authorization-boundary unit tests against a fake: 501-until-wired,
  field-for-field DTO assembly, the narrower-than-`userDTO` shape, the
  empty-tenant clean list, limit validation/pass-through, and the
  `PermAdmin`-admits/`reader`-refused/no-token-refused authorization matrix
  (with a proof the store was never reached on refusal).
- `internal/httpapi/tenant_users_integration_test.go` — the real-Postgres
  proof: exact-active-members correctness including the suspended-account
  and revoked-membership exclusions
  (`TestTenantUsersAPI_ReturnsExactlyActiveMembers`), the empty-tenant clean
  list (`TestTenantUsersAPI_EmptyTenantIsCleanEmpty`), the two-tenant
  isolation proof and its mutation counter-proof
  (`TestTenantUsersAPI_TenantIsolation`,
  `TestTenantUsersAPI_TenantIsolation_BitesWhenLoosened`), and the keyset
  pagination walk (`TestTenantUsersAPI_Pagination`).
- `internal/httpapi/openapi.yaml` — the additive contract
  (`/v1/admin/tenant-users`, `TenantUser`, `TenantUserList`).
- `internal/kg/extract/schema.TestCorpusCensus` — unchanged; no table added.

## Enforcement

- `arch.TestServerWiringUsesTenantScopedPool` — `SetTenantUserDirectory`
  receives the same appPool-scoped `membershipStore` variable
  `SetMemberships` does; no exemption needed.
- `arch.TestPrivilegedReads_AreScopedToATenant` — `MembershipStore` has no
  privileged-pool instance anywhere in this codebase, so this story adds no
  candidate to that guard's sweep; still green, still non-vacuous (its own
  canary is unrelated to this store).
- `arch.TestEveryServerCapability_IsWiredAtTheCompositionRoot` —
  `SetTenantUserDirectory` is called from `cmd/controlplane/main.go`.
- `httpapi.TestOpenAPIContract_CoversEveryServedRoute` /
  `..._PromisesNothingItDoesNotServe` — the one new route is exactly the
  published contract, no more, no less.
- `httpapi.TestTenantUsersAPI_TenantIsolation` /
  `..._TenantIsolation_BitesWhenLoosened` (real Postgres) — Decision 2's
  isolation claim, proven live and proven to fail when the scoping it
  depends on is removed.
- `internal/kg/extract/schema.TestCorpusCensus` — stays exact; a future
  change that reifies this projection into a table fails this test first.
