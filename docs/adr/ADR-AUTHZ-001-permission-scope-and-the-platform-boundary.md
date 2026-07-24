# ADR-AUTHZ-001 — Permission scope, and the platform boundary

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-24 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-001 (row-level isolation), ADR-TENANCY-002 (isolation is a property of wiring) |

## Context

Four cycles established tenant isolation: row-level security, tenant-scoped
connection wiring, cross-tenant read and write isolation, governance mutation
isolation, and tenant-scoped idempotency. Each was verified by attacking the
running service.

None of it mattered, because the boundary was administered by anyone inside it.

An ordinary tenant administrator — a role a customer's own identity provider
issues — could, against the running service:

- **enumerate every customer**, obtaining each tenant's slug and the
  `external_id` its identity provider asserts;
- **suspend a different customer**, which returned HTTP 200 and locked that
  customer out of the entire platform (`403 tenant is suspended` on every
  subsequent request);
- **register tenants** binding external identifiers of its choosing.

Tenant isolation is worth nothing if any tenant can suspend any other.

## Root cause

Two defects, one shallow and one structural.

**The shallow one.** Operations on the tenant registry were routed under
`PermAdmin` — the same permission that administers webhooks and policies
*inside* a tenant. The registry is exempt from row-level security by necessity
(ADR-TENANCY-001 §4: resolving a token to a tenant must precede binding one), so
the middleware was the only control, and it was the wrong control.

**The structural one.** `HasPermission` returned true whenever a role held
`PermAdmin`, regardless of which permission was requested:

```go
if p == perm || p == PermAdmin { return true }
```

`PermAdmin` was a wildcard over every permission that existed **or would ever be
defined**. Adding a permission for a platform capability would have granted it
to every tenant administrator retroactively, silently, without anyone choosing
it. The tenant-registry exposure was one instance; the wildcard guaranteed a
supply of others.

The permission model had no scope dimension at all. Nothing distinguished
"administers things inside a tenant" from "administers the tenants".

## Decision

**Permissions carry a scope, and scope is checked, not implied.**

1. **No permission is implied by another.** `HasPermission` matches exactly. A
   grant exists only where somebody wrote it down.

2. **`PermPlatformAdmin` is separate from `PermAdmin`.** `oneops-admin` holds
   complete authority inside one tenant and none over the set of tenants.
   `oneops-platform-admin` — held by the operator of the installation, by
   nobody's customer — holds both.

3. **Platform operations require two independent conditions**: the platform
   permission, *and* resolution to the system tenant.

   The second exists because roles arrive in a bearer token. A customer's
   identity provider can put `oneops-platform-admin` in the roles array and the
   platform cannot stop it — it does not issue those tokens. What the customer
   cannot forge is the tenant: the `tenant` claim is resolved against the
   registry at the authentication boundary, so a token asserting a customer's
   external id resolves to that customer's tenant no matter what roles
   accompany it. The tenant condition therefore holds even when the roles claim
   is fully attacker-controlled.

   The converse is equally required: resolving to the system tenant is the
   default for a token asserting no tenant, so that condition alone would grant
   platform administration to any unscoped reader.

4. **The refusal does not distinguish which condition failed.** Reporting
   "wrong tenant" rather than "missing permission" would tell an attacker
   holding a forged role exactly what remained to be changed.

## Consequences

**Authentication-disabled runs hold the platform role.** That branch is
local-development only — `validateProduction` hard-fails when authentication is
disabled in production — so the synthetic identity is the operator running the
stack. In production the branch cannot execute.

**Deployments must issue the new role.** Existing `oneops-admin` holders keep
everything they had except tenant administration, which they should never have
had. An operator identity mapping to `oneops-platform-admin` has to exist before
tenants can be registered.

**Enforced mechanically, in two places.** `TestNoPermissionIsImpliedByAnother`
fails if any role grants an undeclared permission, which is the wildcard class
rather than the wildcard instance. `TestPlatformRoutesRequirePlatformAdmin`
parses the router and fails if a tenant-registry handler is reachable through
anything but `requirePlatformAdmin`. Both were verified by reintroducing the
original defect and confirming they fail.

**The generalisation.** ADR-TENANCY-002 established that isolation is a property
of wiring. This adds: **authorization is a property of scope, and scope is not
visible in a permission check.** `requirePermission(PermAdmin)` reads correctly
at every call site; nothing in it reveals whether the resource it guards belongs
to one tenant or to all of them. Where a resource is exempt from row-level
security, the middleware is the entire control, and it must be chosen against
the resource's scope rather than its apparent sensitivity.

**What this does not fix.** Roles are still asserted by an external identity
provider and mapped through a hardcoded table. There is no user, organization or
team model, no way to grant a role to a principal within the platform, and no
record of who granted what. That is the identity platform, and it remains the
highest-priority missing capability — but it is now missing on top of a boundary
that holds, rather than one that does not.
