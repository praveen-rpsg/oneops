# ADR-IAC-003 — Admin invitation endpoints: the target org comes from the caller's context, and the token is shown exactly once

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Decider** | Acting CTO |
| **Related** | ADR-IDENTITY-002 (invitation is tenant-owned, RLS-confined; token is a bearer credential stored only as a hash), ADR-IAC-001/002 (Identity & Access console; context-only org resolution), `internal/domain/invitation.go`, `internal/store/postgres/invitation_store.go`, `internal/httpapi/handlers_invitations.go`, `docs/PLATFORM-BUILD-PLAN.md` E-ID.4a. **Followed by E-ID.4b** (the unauthenticated redeem path — deliberately a separate, security-agent-gated story). |

## Context

The invitation domain and store were fully built (`NewInvitation` mints a
32-byte token and stores only its SHA-256 hash; `Create`/`ListByOrg`/`Revoke`
persist it) but had no HTTP surface. E-ID.4a wires the ADMIN side —
create/list/revoke — so a tenant admin can invite someone by email into their
own organization. The unauthenticated redeem-by-token path is explicitly NOT
in this story (see below).

## Decision

Three `PermAdmin` endpoints, org/tenant resolved from context, token shown once:

- `POST /v1/admin/invitations` — body is `{email}` ONLY. The invitation's
  `OrgID`/`TenantID` come from `domain.TenantFrom(ctx)` + `GetByTenant` (the
  E-ID.2a pattern), **never the request** — so a tenant admin can only ever
  invite into their own org. Returns `201` + an invitation DTO **plus the
  one-time plaintext token** in a dedicated `token` field on the create
  response, and nowhere else, ever.
- `GET /v1/admin/invitations` — lists the caller's own org's invitations
  (context-resolved org, one bounded page).
- `DELETE /v1/admin/invitations/{id}` — revokes; a cross-tenant id matches no
  row under the tenant-scoped pool and returns `404` (never `403` — no
  existence oracle). Safe to call twice (status predicate in the store).

TTL is a server-side constant (`invitationTTL = 7 days`); the client cannot set
it.

### Two load-bearing security properties

1. **Target org is context-only.** `createInvitationRequest` has no
   `org_id`/`tenant_id` field to decode, and the handler resolves the org from
   the authenticated tenant. A two-tenant integration test asserts a smuggled
   `org_id` is ignored and the invitation always lands in the caller's org.
2. **The token is write-once, read-never.** `invitationDTO` has no token or
   token_hash field; the plaintext exists only as the create response's `token`
   and is never logged. `NewInvitation` returns it once and it is unrecoverable
   thereafter (only the hash is stored). Losing it means revoke + reissue.

## Consequences

**Guaranteed.** A tenant admin can issue, review, and revoke invitations scoped
to their own org; the invite token is surfaced exactly once for the admin to
deliver out-of-band; no admin can invite into, list, or revoke another tenant's
invitations (proven by test). Additive, contract-compatible (bijection tests
pass with the new routes).

**Not claimed / deferred to E-ID.4b.** An invitation cannot yet be REDEEMED —
there is no HTTP path onto the store's `Consume` yet. Redeem is unauthenticated
(authorized by token possession) and must provision the invited email's
membership, which is the single most security-sensitive endpoint in the
platform. It gets its own story with the Trust-Register discipline (build →
live-attack: token enumeration, double-redeem, expired, cross-tenant
provisioning → guard) rather than being folded in here.

## Enforcement

- `TestInvitationsAPI_TenantIsolation` / `_CreateLandsInCallersOwnOrgAndReturnsTokenOnce`
  / `_NoPermAdminIsForbidden` / `_RevokeIsIdempotentSafe` / `_RevokeUnknownIsNotFound`
  (`internal/httpapi/invitations_integration_test.go`, `//go:build integration`,
  run with `TEST_DATABASE_URL`) — the isolation + token-once assertions are the
  security gate; they must never be weakened.
- Contract bijection keeps the routes and `openapi.yaml` in lockstep; the
  `Invitation` schema must never gain a token/token_hash field.
- Any change that makes `createInvitation` read the org/tenant from the request,
  or that surfaces the token outside the create response or in a log,
  contradicts this ADR and must supersede it.
