# ADR-IDENTITY-003 — A token's issuer must be authorised for the tenant it claims

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-31 |
| **Decider** | Acting CTO (Security office) |
| **Story** | OPS-S047b (follows OPS-S047a, the multi-IdP verifier) |
| **Related** | ADR-IDENTITY-001 (organization/tenant; external_id resolution), ADR-IDENTITY-002 (identity data placement), ADR-TENANCY-001 (tenant is the isolation boundary), ADR-TENANCY-003 (ownership re-derived, not trusted from a label), ADR-AUDIT-007 §6.13 (administrative-operation vocabulary is extensible) |

## Context

OPS-S047a introduced multi-IdP verification: the platform trusts a *set* of
identity providers, keyed by the token's `iss`, so enterprise tenants can bring
their own IdP. Each IdP's signature, audience and expiry are verified against
that IdP alone — a genuine improvement, and correct as far as it goes.

It does not go far enough. The verifier answers *"is this a genuine token from a
configured IdP?"* The authentication boundary then resolves the tenant the token
**claims** — `resolveTenant` in `internal/httpapi/middleware.go` — purely by
`tenants.GetByExternalID(claims.Tenant)`. Nothing checks that the IdP which
signed the token is authorised to authenticate that tenant. The
`domain.Tenant` model carried no issuer at all.

So with a default IdP plus one additional IdP (idp-B, some tenant's provider),
an actor controlling idp-B can mint a **validly-signed** token
`{iss: idp-B, aud: aud-B, sub: user, tenant: <tenant X's ExternalID>}`.
`Verify()` accepts it (it is a real idp-B token); `resolveTenant` resolves
tenant X by its external id; and the request runs inside **tenant X's**
row-level-security boundary. This is a cross-tenant authorization bypass — the
exact class the whole tenancy model exists to prevent — reachable by anyone who
operates any one of the configured additional IdPs.

Single-IdP deployments are **not** affected: with one trusted issuer, that issuer
is by definition the authority for every tenant claim. The defect is specific to
the multi-IdP configuration OPS-S047a made possible.

### Live evidence (before)

An end-to-end middleware test (`internal/httpapi/middleware_issuer_binding_test.go`),
run against the fully-wired router with a real multi-IdP `auth.Verifier` (default
issuer + idp-B) and tenant X bound to neither: an idp-B token claiming X's
external id was **admitted** — `GET /v1/artifacts` returned **200**, inside X's
boundary. With the enforcement reverted the same test reports
`idp-B token claiming tenant X = 200, want 403`.

## Decision

Bind each tenant to the issuer(s) allowed to authenticate it, and enforce that
binding at the single place a claim becomes a tenant.

1. **`tenant.allowed_issuers text[]`** (migration
   `20260813000001_tenant_allowed_issuers.sql`), `NOT NULL DEFAULT '{}'`.
   `tenant` is the registry, not a tenant-owned table (excluded from
   `TenantOwnedTables` by design), so no RLS policy is added.

2. **Empty ⇒ default IdP only.** An empty `allowed_issuers` means the tenant may
   authenticate **only** via the default issuer, `ONEOPS_JWT_ISSUER`. This is
   safe-by-default and backward compatible: every tenant that predates this
   change has an empty set and keeps working with the default IdP; a tenant that
   wants an additional IdP must **explicitly** list that IdP's issuer.

3. **`domain.Tenant.AllowsIssuer(issuer, defaultIssuer)`** encodes the rule —
   empty set ⇒ `issuer == defaultIssuer`, non-empty ⇒ membership — and fails
   closed on an empty issuer or an empty default. An explicit set does **not**
   implicitly include the default: an operator that narrows a tenant to one IdP
   means it.

4. **`auth.Claims.Issuer`**, set in `Verify` from the issuer `jwt.Parse`
   validated (`WithIssuer(iss)` required the token's own `iss` to equal it), not
   the unverified peeked value.

5. **Enforcement in `resolveTenant`**, fail closed: after resolving the tenant
   by external id and checking it is active, require `t.AllowsIssuer(claims.Issuer,
   s.cfg.JWTIssuer)`. Otherwise reject with **403**, shaped identically to the
   unknown-tenant refusal so the response is not an oracle for which tenants
   exist or which issuers they have bound. The tenant boundary is never entered.

6. **Tenant management.** `allowed_issuers` is settable at create and via a patch
   that replaces the set, both through the existing administrative-audit
   chokepoint (`withAdminAudit`). A patch carrying `allowed_issuers` is audited as
   the new `tenant.issuers_changed` operation — a new value in the disjoint dotted
   vocabulary and the database `ck_admin_audit_operation` CHECK, per
   ADR-AUDIT-007 §6.13, with the domain `AdminOperation.Valid` pre-check kept in
   agreement.

### Live evidence (after)

Same test, enforcement in place: idp-B token claiming X → **403**, boundary not
entered. Backward compat: default-IdP token for an empty-binding tenant → **200**.
Legitimate additional IdP: tenant Y with `allowed_issuers=[idp-B]`, idp-B token
for Y → **200**. Discrimination: in one server idp-B is admitted for Y and
refused for X. Store round-trip and the audited, row-version-guarded
`SetAllowedIssuers` verified live on PostgreSQL (:5435).

## Guarantee (stated, not overstated)

What this eliminates: **a genuine token from one configured IdP can no longer
authenticate a tenant that has not authorised that IdP.** The issuer used in the
decision is the verified `iss`, and the check is at the sole chokepoint where a
claim becomes a tenant, so it holds for every `/v1` path, not one route.

What it does **not** claim:

- **The binding trusts the operator's configuration.** `allowed_issuers` is
  administrator-supplied. If an operator lists an issuer they do not control, or
  configures a hostile IdP as an additional provider, this control authorises
  exactly what they configured. It binds issuer→tenant; it does not judge whether
  an issuer is trustworthy — that is the operator's decision and the verifier's
  signature check.
- **It is not authentication of the IdP endpoint.** RS256 IdPs are still trusted
  via their JWKS; the JWKS guard refuses non-public addresses (ADR-SECURITY-003)
  but does not authenticate the endpoint beyond that.
- **Per-user / per-subject issuer rules are out of scope.** The binding is
  tenant-level: any subject from an allowed issuer may claim the tenant. Finer
  identity rules (which users, which roles) are a separate concern.
- **SAML and dynamic IdP registration are out of scope.**
- **Direct SQL bypasses this, as it bypasses every application control**
  (ADR-SECURITY-002's sentinel is the backstop for the data plane, not this
  check).

## Consequences

- A tenant using an additional IdP must have that IdP's issuer listed in
  `allowed_issuers` before its tokens are accepted — a one-time administrative
  step, and the safe-by-default reason existing tenants need no change.
- `PatchTenantRequest` gains an optional `allowed_issuers`; when present it is the
  operation (status not read). `Tenant` responses always include
  `allowed_issuers` (empty array, never null).
- One new administrative-audit operation, `tenant.issuers_changed`.

## Enforcement

- `httpapi.TestIssuerBinding_*` — the exploit (403), backward compat (200),
  legitimate additional IdP (200), per-tenant discrimination, and
  explicit-set-excludes-default. The exploit test **bites**: reverting the
  `AllowsIssuer` check in `resolveTenant` admits the request (200) and fails it.
- `auth.TestVerifyCarriesVerifiedIssuer` — `Claims.Issuer` is the verified value.
- `domain` unit coverage of `AllowsIssuer` (empty⇒default, membership, fail-closed).
- `postgres.TestTenantStore_AllowedIssuersRoundTrip`,
  `postgres.TestTenantStore_SetAllowedIssuersOptimistic` (integration, live DB) —
  column round-trip, empty-non-null default, optimistic concurrency, audited path.
