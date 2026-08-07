# ADR-IAC-004 — Invitation redeem is an unauthenticated, token-only endpoint that provisions in the invitation's own tenant; security-reviewed

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Decider** | Acting CTO (built by implementer, adversarially reviewed by the security agent) |
| **Related** | ADR-IAC-003 (admin invitation endpoints, which reserved this story), ADR-IDENTITY-002/003 (invitation model; OIDC binding), ADR-TENANCY-001 §4 (privileged-path exception ordering), ADR-SECURITY-002 (invariant gate), ADR-SECURITY-004 (rate limiting deferred to ingress; no pre-auth per-IP limiter), OPS-S026 (the pre-existing `Redemption` domain/store this endpoint exposes), `internal/httpapi/handlers_redeem.go`, `internal/store/postgres/redemption_store.go`, `docs/PLATFORM-BUILD-PLAN.md` E-ID.4b |

## Context

Admin-side invitations (ADR-IAC-003) let a tenant admin issue a one-time token
to an email. E-ID.4b is the other half — "register, the OneOps way": the
invitee redeems the token and becomes an active member. The atomic
consume+provision transaction already existed (`RedemptionStore.Redeem`,
OPS-S026, with its own integration suite); what was missing was the HTTP
transport. This ADR records the transport's security-relevant decisions.

## Decision

### An unauthenticated, rate-limited, token-only endpoint

`POST /auth/invitations/redeem` — registered at the TOP LEVEL, outside `/v1`
(whose `authenticate` middleware would reject an invitee who has no session
yet), in its own group carrying `invariantGate` + `rateLimit` but **not**
`authenticate`. Authorization is possession of the token; the request body
carries **only** `{token}`. The response is minimal — the organization name as
a courtesy so the invitee knows what they joined — and nothing else.

### Provisioning is derived entirely from the consumed invitation

In one transaction on the privileged pool (there is no request tenant — the
invitee holds no membership yet, the ADR-TENANCY-001 §4 ordering exception):
consume the invitation (atomic `UPDATE … WHERE status='pending' AND
expires_at>now()`), find-or-create the user by the invitation's normalized
email (refusing — and rolling back — a `suspended`/`deactivated` account), grant
active membership in the invitation's own org/tenant, and audit as
`InvitationBearerActor`. Org, tenant, and email come only from the consumed
row; no request input can steer them. Every failure cause returns the single
generic `ErrTokenNotRedeemable` → `400`, byte-identical, to avoid an
enumeration oracle.

### Body-size cap (security review finding #1, fixed here)

The handler wraps the body in `http.MaxBytesReader(w, r.Body, 4 KiB)` before
decoding — the token is ~43 chars, so 4 KiB is generous — returning `413` for
anything larger. Without it, this unauthenticated endpoint would read and hash
an arbitrarily large body pre-provision (a DoS/OOM path the count-based,
shared-bucket rate limiter does not close). `TestADV_OversizedBody_NoSizeCap`
asserts `413` and is the build-failing guard for this class.

## Security review outcome

The security agent adversarially attacked the endpoint (findings and proofs in
`internal/httpapi/redeem_adversarial_test.go`, kept as regression guards).
**No merge-blocking defect** in the four critical classes:

- **Cross-tenant provisioning — CLOSED.** Smuggled `org_id`/`tenant_id`/`email`/
  `user_id` in the body are ignored; membership lands only in the invitation's
  tenant (`TestADV_SmuggledBodyFieldsAreIgnored`, `..._MembershipLandsOnlyInTheInvitationsTenant`).
- **Enumeration (content) — CLOSED.** Unknown/expired/revoked/redeemed are
  byte-identical responses.
- **Single-use / race — CLOSED.** 8 concurrent redeems → exactly one success,
  one membership (`TestADV_ConcurrentDoubleRedeem_ExactlyOneWins`).
- **Account reactivation / takeover — CLOSED.** Suspended/deactivated accounts
  abort the redemption (rollback); email case/whitespace fold to one account,
  no duplicate; homoglyphs are distinct bytes and cannot collide.
- **Privileged write is invariant-gated — CLOSED.** With the isolation
  invariant breached, the route returns `503` and never touches the store
  (`TestADV_InvariantBreach_RefusesBeforeStoreIsTouched`).
- **Atomicity — CLOSED.** Consume + user + membership + audit are one
  transaction; a mid-transaction refusal rolls back the consume.
- **Input abuse — CLOSED** (malformed/empty/null-byte/oversized handled;
  oversized now `413`).

### Two accepted residuals (documented, not blocking)

1. **Timing side-channel (Low).** A *valid* token naming a suspended account is
   ~350µs slower than a guessed/unknown token (the former consumes then rolls
   back). It is not a mass-enumeration oracle — guessed tokens all fail fast and
   uniformly; the slow path requires already holding a valid token. Accepted.
2. **Shared rate-limit bucket (residual DoS).** All anonymous callers bucket to
   `SystemTenantID` (one bucket) — a single caller can exhaust redemption
   capacity for every invitee on the instance. Per ADR-SECURITY-004 a pre-auth
   per-IP limiter is deferred to the ingress (the app cannot trust a forwarded
   client IP). Accepted as an ingress concern; noted here so it is not
   rediscovered as new.

## Consequences

**Guaranteed.** An invitee with a valid token becomes an active member of
exactly the invitation's org, in one atomic step, over an endpoint that leaks
no cause, no existence, and no cross-tenant reach, and that refuses oversized
bodies and gates its privileged write on the isolation invariant.

**Open question — OIDC reconciliation (NOT resolved here).** Redeem provisions
the *OneOps* side (a local `app_user` + membership by email), independent of any
OIDC issuer. How a later real SSO login for the same email reconciles with this
account — does the issuer's subject link to it, or diverge into a second
account? — is unresolved and belongs to an IdP-integration story, not this
transport. In dev (auth disabled) redeem is fully exercisable; in production its
end-to-end value depends on the tenant's IdP recognizing the invited address.

## Enforcement

- `internal/httpapi/redeem_integration_test.go` + `redeem_adversarial_test.go`
  (`//go:build integration`, `TEST_DATABASE_URL`) pin every closed class and the
  `413` body-cap guard; they must not be weakened.
- The request struct must stay token-only; the response must stay minimal; the
  route must never gain `authenticate` (it would break invitees) nor lose
  `rateLimit`/`invariantGate`.
- `internal/store/postgres/redemption_store_integration_test.go` (OPS-S026)
  remains the atomicity/isolation proof for the transaction beneath.
