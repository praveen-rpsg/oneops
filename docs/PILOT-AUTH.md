# OneOps — Real OIDC Login (demo & pilot auth)

The demo flow runs with auth **disabled** (single-operator). This doc turns on
**real OIDC browser login with role-based access**, two ways:

1. **Turnkey** — a bundled Keycloak (`docker-compose.auth.yml`) pre-seeded with
   users and roles, so you can show a genuine login + RBAC locally in minutes.
2. **Your IdP** — point OneOps at a design partner's real IdP (Okta, Azure AD,
   Keycloak, …). Same wiring; only config changes.

OneOps is an OIDC **relying party**: an external IdP authenticates the user and
issues a JWT; OneOps validates it and enforces the roles it carries. OneOps
never stores passwords.

---

## 1. Turnkey: bundled Keycloak

Prereqs: `make up` (deps) is running.

```bash
# a. Start the bundled IdP (realm auto-imported from deploy/keycloak/oneops-realm.json)
make up-auth

# b. Seed the demo tenant (auth still disabled for this one-time setup)
make db-reset && make seed-demo          # skip if already seeded

# c. One-time: trust the Keycloak issuer for the demo (system) tenant
#    (the control plane must be running — e.g. `make run` — for this call)
make auth-bind

# d. Run the control plane with REAL auth against Keycloak
make run-auth

# e. Open the console — you'll be redirected to Keycloak to sign in
open http://localhost:8080
```

**Demo logins** (username / password → role):

| User | Password | Role → access |
|---|---|---|
| `priya.nair` | `oneops-demo-pw1` | `oneops-admin` + `oneops-platform-admin` — full access, incl. Administration |
| `raj.shah` | `oneops-demo-pw2` | `oneops-editor` — read/write, no admin |
| `mei.lin` | `oneops-demo-pw3` | `oneops-reader` — read-only |

Sign in as **priya** to show full access; sign in as **mei** to show a
read-only user who is denied admin actions (403) — RBAC is real, enforced
server-side.

> Keycloak admin console (if needed): http://localhost:8081 — `admin` / `admin`.
> The bundled client is browser-flow (Auth-Code + PKCE) only, which is the
> correct posture for a public SPA client.

### Proof it works (no browser needed)
The full chain is verifiable end-to-end. A token minted by Keycloak for priya
carries `iss=http://localhost:8081/realms/oneops`, `aud=oneops-console`,
`roles=[oneops-platform-admin, oneops-admin]`, `tenant=system`; OneOps validates
it (JWKS) and enforces RBAC:

- priya → `GET /v1/admin/users` → **200**; mei (reader) → **403**.
- No token → **401**.

---

## 2. Point OneOps at YOUR IdP (a real pilot)

Nothing above is Keycloak-specific. For a partner's IdP, set these on the
control plane and bind the tenant to the IdP's issuer:

| Env var | Meaning |
|---|---|
| `ONEOPS_AUTH_ENABLED=true` | enforce auth |
| `ONEOPS_JWT_ISSUER` | the IdP's issuer URL (the token `iss`) |
| `ONEOPS_JWKS_URL` | the IdP's JWKS endpoint (RS256 public keys) |
| `ONEOPS_JWT_AUDIENCE` | the audience your client is configured to request |
| `ONEOPS_OIDC_CLIENT_ID` | the public SPA client id (served to the console via `/auth/config`) |
| `ONEOPS_JWKS_ALLOW_PRIVATE_TARGETS` | **only** if the IdP's JWKS is on a private/loopback address (bundled or on-prem-VPN IdP). Default `false` keeps the SSRF egress guard on for public IdPs. |

Then bind each tenant to its issuer (multi-tenant, per ADR-IDENTITY-003):
`PATCH /v1/admin/tenants/{id}` with `{"allowed_issuers":["<issuer>"]}` — a token
is only accepted for a tenant that lists its `iss`. Multiple IdPs are supported
via `ONEOPS_ADDITIONAL_IDPS` (different tenants, different IdPs).

### What the IdP's tokens MUST carry
OneOps reads three things from the access token (see the Keycloak protocol
mappers in `deploy/keycloak/oneops-realm.json` as the worked example):

- **`sub`** — the subject (standard).
- **`roles`** — a JSON array of OneOps roles: `oneops-reader` | `oneops-editor`
  | `oneops-admin` | `oneops-platform-admin`. Map the customer's groups/roles to
  these (Keycloak: a realm-role mapper → claim `roles`; Okta/Azure: a groups or
  app-roles claim mapped to `roles`).
- **`tenant`** — the OneOps tenant id the user belongs to, matching a tenant
  whose `allowed_issuers` includes this IdP. (Keycloak: a hardcoded or
  attribute-based claim mapper → claim `tenant`.)

Roles map to permissions in `internal/auth/rbac.go` (exact match, no wildcard);
platform operations additionally require the system tenant (ADR-AUTHZ-001).

### Invited users (identity reconciliation)
An invited email that redeems an invitation (`/redeem`) is provisioned in
OneOps (an `app_user` + membership); for them to log in, the IdP must
authenticate that email and issue a token with the matching `tenant` + `roles`.
**Requirement:** the IdP must emit a stable **`email`** claim for invited
addresses — email is the reconciliation key (ADR-IAC-005). The pilot model is
**invite-first**: an admin invites and the account exists before first login, so
login *resolves* an existing member (no blind self-provisioning). Resolving the
authenticated session to the OneOps `app_user` by email (for who-am-I + audit
attribution by OneOps user id) is the decided design in ADR-IAC-005, implemented
against the first real IdP so the claim shapes are known rather than guessed —
authorization (roles + tenant from the token) already works without it.

---

## Notes
- `docker-compose.auth.yml` is additive — the default `make up` and the
  auth-disabled demo flow are unchanged. Stop the IdP with `make down-auth`.
- The demo realm passwords are non-secret and for local use only.
