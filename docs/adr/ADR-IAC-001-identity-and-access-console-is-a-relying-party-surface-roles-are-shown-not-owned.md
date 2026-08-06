# ADR-IAC-001 — The Identity & Access console is a relying-party surface: OneOps shows identity and roles, it does not own or edit them

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO (founder-directed model choice) |
| **Related** | ADR-IDENTITY-001/002/003 (org↔tenant, user/membership/invitation model, issuer→tenant OIDC binding), ADR-AUTHZ-001 (permission scopes; PermAdmin is exact, not wildcard), ADR-UI-001/002 (Cloudscape + self-contained embedded bundle), `internal/auth/rbac.go` (the role→permission map this restates), `docs/PLATFORM-BUILD-PLAN.md` E-ID |

## Context

The founder asked to "add login / register / role / permissions UI". Taken
literally that spans two features that **contradict OneOps' ratified
architecture** and three that fit it. The CTO surfaced the conflict before
building, and the founder chose the **relying-party** model:

- **Authentication is OIDC** (Authorization Code + PKCE against an external
  IdP; ADR-IDENTITY-003, fails closed, no local credentials). Login already
  exists — there is nothing to "add" unless one introduces in-app passwords,
  which would be a security-architecture reversal, not a UI story.
- **Roles are a JWT `roles` claim, not a database column.** The four roles
  (`oneops-reader`/`editor`/`admin`/`platform-admin`) are minted by the IdP and
  map — exact match, no wildcard (ADR-AUTHZ-001) — to permissions in
  `internal/auth/rbac.go`. OneOps has no per-membership role to write; changing
  a member's role means re-issuing their token at the IdP.
- **Registration is invitation-only** (ADR-IDENTITY-002). The invitation
  domain + store exist; their HTTP endpoints do not yet.

## Decision

**The Identity & Access console is a relying party. It surfaces and (later)
manages the identity the IdP and the existing backend own; it never becomes
the authority for credentials or roles.** Two things are explicitly excluded
from the epic as architecture-contradicting — they may only enter through a
new ADR + security decision, never smuggled in as "UI":

1. In-app password login / self-service signup (contradicts OIDC-only).
2. Editable per-member roles inside OneOps (contradicts IdP-owned roles).

### E-ID.1 — the foundation this ADR lands

- A new **"Administration"** section in the console (`web/src/Shell.tsx`),
  visible to any signed-in user for now (its content is the user's own
  identity + a static reference table; admin-gated management screens arrive in
  E-ID.2+).
- A **"Who am I"** panel: the caller's subject, the roles their token carries,
  and the resulting effective permissions. It degrades gracefully to an
  explicit notice when auth is disabled (local dev) or the token has no `roles`
  claim — it never blanks or throws.
- A read-only **Roles × permissions matrix** restating `internal/auth/rbac.go`
  (4 roles × 5 permissions, with each permission's scope), under an explicit
  notice: *roles are assigned by your identity provider; OneOps enforces them
  but does not create, assign, or edit roles here.*

### The client-side role read is display-only, and says so

`web/src/auth.ts`'s new `getRoles()` decodes the token's `roles` claim purely
to label the UI; `web/src/rbac.ts` restates the role→permission map. Both carry
comments stating they are **display-only and never an authorization boundary** —
every enforcement decision still round-trips to the server
(`internal/auth.HasPermission`), which is the sole authority. A tampered token
changes what the browser *draws*, never what the server *allows*. `rbac.ts` is
a restated mirror (like the `alertRules.ts` enums), not generated; a
`rbac.test.ts` pins it to the documented values so drift from `rbac.go` is
caught in `make web-test`.

### Reduced-concept discipline

This is a view over existing claims plus a static reference table. No `Role` or
`Permission` entity is reified in the store or the API — consistent with the
corpus's false-noun treatment.

## Consequences

**Guaranteed.** A signed-in user can see who they are, the roles their token
grants, and what those roles can do, in a themed (light/dark) Cloudscape screen
that stays inside the self-contained `go:embed` bundle (no new dependency, no
runtime CDN — ADR-UI-001/002). Nothing here can be mistaken for a place to
manage credentials or mint roles; the honest notice is shown, not implied.

**Not claimed.** No management actions yet (grant/revoke membership, invite,
user CRUD arrive in E-ID.2–.5 over the endpoints that already exist, plus the
invitation endpoints E-ID.4 wires). The matrix is the restated static contract,
not a live read of "which permission is this deployment enforcing."

**Future guard-rail.** If a later story proposes app-owned passwords or
editable roles, it contradicts this ADR and ADR-IDENTITY-003 / ADR-AUTHZ-001
and must supersede them through the governance process — it is not an
incremental console feature.

## Enforcement

- `web/src/rbac.test.ts` + `web/src/routes/AdministrationPage.test.tsx` under
  `make web-test` pin the restated map to `rbac.go`'s values and prove the
  graceful no-token branch and the matrix render.
- CTO visual gate: the screen was rendered live (auth-disabled) in light and
  dark mode and matches this ADR (identity notice + honest IdP-owned-roles
  note + a matrix whose grants equal `rbac.go`'s).
- The self-contained-bundle CI check (ADR-UI-002) covers the new code like any
  other console code.
