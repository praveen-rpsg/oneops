# ADR-IAC-002 — A tenant admin resolves their own organization through a context-only `GET /admin/tenant-org`, not by handling a ULID

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-IAC-001 (the Identity & Access console), ADR-IDENTITY-001/002 (org↔tenant 1:1; organization is global, outside RLS), ADR-AUTHZ-001 (PermAdmin is tenant-scoped, exact), ADR-ACT-001 §2 (named the same org_id-discovery gap for a different endpoint), `docs/PLATFORM-BUILD-PLAN.md` E-ID.2 / E-ID.2a, `internal/httpapi/handlers_organizations.go` (`getTenantOrganization`), `internal/httpapi/server.go` (route), `web/src/memberships.ts` / `web/src/routes/MembersPage.tsx` |

## Context

The Members screen (E-ID.2) drives the existing membership endpoints, which
require an explicit `org_id`:

- `GET /v1/admin/memberships?org_id=…`
- `POST /v1/admin/memberships` (body `{org_id, user_id}`)

A tenant-admin console session had **no way to discover that `org_id`**. It is
not a JWT claim, and the only capability that could resolve it —
`OrganizationRepository.GetByTenant` — had no HTTP route; every
`/admin/organizations/*` route is `requirePlatformAdmin` (a strictly more
privileged, cross-tenant gate) than the `PermAdmin` tier the membership
endpoints use. The first E-ID.2 cut worked around this by asking the admin to
paste their Organization ID (a ULID) and remembering it per tab. That is below
the product's UX bar: a tenant administrator should never have to know, let
alone type, their own org's opaque identifier.

## Decision

**Add `GET /v1/admin/tenant-org` (E-ID.2a): a `PermAdmin`-gated endpoint that
returns the caller's OWN organization, with the tenant resolved from the
authenticated context alone.** The console calls it on load and uses the
returned `org_id` automatically; the manual-entry field remains only as a
degraded fallback if the call fails for a non-authorization reason.

### The context-only sourcing is the load-bearing security property

`organization` is **global and outside row-level security** (ADR-IDENTITY-002
§3.1). For the tenant-scoped read paths RLS is the backstop; here there is no
RLS backstop, so **this handler is the sole isolation control** in front of the
org registry. Therefore the tenant id must come exclusively from
`domain.TenantFrom(r.Context())` — never a query param, path, or body. A
caller-chosen tenant id would let any tenant administrator resolve, and thus
learn the `org_id` of, every other customer's organization. The handler uses
the verified context and nothing else; a two-tenant integration test
(`TestTenantOrgAPI_TenantIsolation`) proves tenant A's call returns A's org and
never B's — and, because the request carries zero parameters, that test would
fail immediately if the handler were ever changed to resolve the org from an
input instead of the context.

### Scope and shape

- `requirePermission(PermAdmin)` — tenant-scoped admin, the same tier as the
  membership endpoints it serves; explicitly NOT `requirePlatformAdmin`.
- Reuses the existing `organizationDTO`/`toOrganizationDTO`; no new schema, no
  new DTO, no migration, no new repository — additive route only.
- `404` if the 1:1 is ever violated (unreachable in a consistent DB), `403` if
  no tenant is bound, `501` if the org registry is unconfigured — mirroring the
  sibling handlers' conventions.

## Consequences

**Guaranteed.** A tenant admin opens the Members screen and it loads their org's
members immediately — no ULID handoff. The org registry cannot be enumerated
across tenants through this route: the only tenant it will ever resolve is the
caller's own, proven by test, not inspection. The change is purely additive and
contract-compatible (contract bijection tests pass with the new route in
`openapi.yaml`).

**Not claimed.** This does not make `organization` RLS-confined (that is a
larger, separate decision); it makes the one new read path context-safe by
construction. The manual-entry fallback still exists for non-authorization
failures, so a transient error degrades rather than dead-ends.

**Supersedes** the interim "enter and remember the Organization ID per tab"
posture the first E-ID.2 cut shipped; that manual path is now a fallback, not
the primary flow.

## Enforcement

- `TestTenantOrgAPI_TenantIsolation` / `_ReturnsCallersOwnOrg` /
  `_NoPermAdminIsForbidden` / `_NoOrgForTenantIsNotFound`
  (`internal/httpapi/tenant_org_integration_test.go`, `//go:build integration`,
  run with `TEST_DATABASE_URL`) — the isolation assertion is the security gate;
  it must never be weakened to a single-tenant test.
- The contract bijection tests keep the route and `openapi.yaml` in lockstep.
- Any future change that makes `getTenantOrganization` read a tenant/org
  identifier from the request contradicts this ADR and must supersede it — the
  context-only rule is not an implementation detail, it is the isolation
  control.
