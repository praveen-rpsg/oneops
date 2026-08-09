# ADR-IAC-005 — The authenticated principal reconciles to the OneOps user by email; JIT account-linking is deferred to real-IdP integration

| | |
|---|---|
| **Status** | Accepted (design decision; implementation deferred) |
| **Date** | 2026-08-08 |
| **Decider** | Acting CTO |
| **Related** | ADR-IAC-004 (raised this open question — "how a later SSO login for a redeemed email links to the account"), ADR-IDENTITY-002 (user is global, membership tenant-owned), ADR-IDENTITY-003 (issuer→tenant binding), ADR-AUTHZ-001 (roles→permissions), ADR-IAC-001 (relying-party model), `internal/auth/jwt.go` (Claims), `docs/PILOT-AUTH.md`. Resolves the reconciliation question E-AUTH.1 left open. |

## Context

E-AUTH.1 made real OIDC login work: a token's `roles` grant permissions and its
`tenant` binds the tenant — **authorization is fully self-contained in the
JWT**, so a pilot user can log in and use the platform today. E-ID.4b's redeem
provisions an OneOps `app_user` + membership by email. ADR-IAC-004 flagged the
open question: when that invited person later logs in via the IdP, how does the
authenticated session **reconcile** to the OneOps identity — for the Members
screen, on-call paging, audit attribution, and "who am I"?

Two facts shape the answer:
- **Authz does not need reconciliation** — it comes from the JWT's `roles`/
  `tenant`. Nothing is blocked today.
- **Identity coherence does** — the audit actor is currently the raw JWT `sub`
  (the IdP's subject id), which is not the OneOps `user_id`, and the identity
  surfaces key on `app_user` (by email). Without a link, "the logged-in person"
  and "the managed OneOps user" are disjoint records.

## Decision

**Email is the reconciliation key. The pilot model is invite-first (no blind
JIT auto-provisioning). Full session→account linking and audit-attribution-by-
OneOps-user is implemented when a real customer IdP is integrated, not
speculatively against the bundled demo Keycloak.**

### The decided design (to implement at real-IdP integration)
1. **Link by normalized `email`.** OneOps reads the standard OIDC `email` claim
   and resolves the authenticated principal to the `app_user` whose
   `NormalizeEmail(email)` matches (the same normalization redeem/invitation
   already use, so an invitation and the account it becomes agree on identity).
   `email` is the durable, IdP-portable key; `sub` is IdP-specific and only
   meaningful within one issuer.
2. **Invite-first, not JIT-create.** A user reaches a tenant through the
   ratified invite→redeem flow (an admin invites; the `app_user` + membership
   exist before first login). So first login **resolves** an existing user; it
   does not auto-create one. A token whose email matches no active member of the
   bound tenant is authenticated (valid token) but has no OneOps identity to act
   as beyond its JWT roles — the same "authorized by claims, unknown as a
   managed user" state that exists today. Auto-provisioning on first login is a
   larger policy choice (who may self-provision into a tenant) deliberately NOT
   made here.
3. **Audit attribution** shifts from the raw `sub` to the resolved OneOps
   `user_id` when a match exists (falling back to `sub` when it doesn't) — so an
   action by a logged-in member is attributed to their OneOps identity. This is
   the load-bearing implementation step and touches the audit actor path; it is
   done with a real IdP so the `email`/`sub` claim shapes are known, not guessed.

### Why deferred, not built now
The bundled Keycloak (E-AUTH.1) is a demo/turnkey IdP a real customer will not
use. Building JIT/linking against it risks encoding assumptions (which claim is
durable, whether email is verified, the provisioning policy) that the customer's
actual IdP (Okta/Azure AD/…) breaks. The pilot functions without it; the
correct, non-speculative move is to **decide the design** (this ADR) and
implement it against the first real IdP. This is consistent with the standing
charter: don't build unvalidated identity surface ahead of the customer.

## Consequences

**Guaranteed now.** A pilot user logs in and gets exactly their role-based,
tenant-scoped access (proven, E-AUTH.1). The reconciliation design is decided
and its pilot requirement documented (`docs/PILOT-AUTH.md`): the IdP must emit a
stable `email` claim for the invited addresses.

**Not yet.** The logged-in principal is not yet resolved to the `app_user`
(who-am-I shows the JWT `sub`; audit actor is the `sub`); on-call paging already
uses the membership email independently, so paging is unaffected. These are
identity-coherence refinements, not access gaps.

## Enforcement / implementation checklist (when a real IdP is integrated)
- Add `Email` to `auth.Claims` (read the `email` claim) + thread it through the
  request context.
- Resolve `app_user` by `NormalizeEmail(email)` within the bound tenant;
  surface the resolved identity in "who am I" and use its `user_id` for audit
  attribution (fallback to `sub`).
- Decide the auto-provisioning policy explicitly (default: none — invite-first)
  and, if ever enabled, gate it (a tenant setting) with tests.
- Two-tenant isolation test: an email present in two tenants resolves to the
  member of the *bound* tenant only.
