# ADR-TENANCY-002 — Isolation is a property of wiring

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-24 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-001 (row-level isolation model), ADR-AUDIT-005 (atomic constitutional mutation) |

## Context

ADR-TENANCY-001 put tenant isolation in the database: `tenant_id` on every
governed table, `FORCE ROW LEVEL SECURITY`, fail-closed policies, and a
tenant-scoped pool that assumes a non-privileged role and binds the request's
tenant on connection acquire.

It worked. Seven integration tests proved cross-tenant reads returned nothing,
cross-tenant writes were refused, unbound connections saw no rows, and every
table carried an enforced policy. The suite was green, the lint was clean, the
migrations validated, and the design was sound.

**Three cross-tenant disclosures shipped anyway**, and all three were found by
attacking the running service rather than by any test:

1. **Webhook administration.** The admin API and the delivery workers shared one
   store instance. The workers legitimately hold the privileged pool, so the API
   inherited its RLS bypass. A second tenant could list every tenant's endpoint
   URLs, `PATCH` with `rotate_secret` and receive another tenant's HMAC signing
   key in the response — enough to forge signed deliveries — and disable their
   delivery entirely.

2. **Execution timeline.** Built on the privileged pool. Any authenticated
   caller who knew a configuration id could read another tenant's governance
   history: actor, operation, timestamps, correlation ids. The governance
   endpoints refused the same id, because they resolve the object through the
   scoped repository first.

3. **Governance mutation.** The engine was built on the privileged pool. A
   second tenant could `POST /v1/governance/{id}/suspend` against another
   tenant's ratified artifact and receive HTTP 200: the lifecycle changed, the
   row version advanced, and an entry was written into the victim's
   **append-only** chain attributed to the attacker's operation id. The read
   endpoints refused that id; the write path went straight to the engine and
   never resolved the object at all. A `withdraw` attempt failed only on a
   domain state-machine rule — not on any authorization check.

Every one was a single token: `pool` where `appPool` belonged. Every one
compiled, read correctly, and passed the entire suite.

## The reason the tests could not see it

The RLS tests exercised the scoped pool **directly**. They proved the policies
were correct. Nothing asserted *which pool each subsystem was actually handed*,
because both pools are `*pgxpool.Pool` and the type system cannot tell them
apart.

The property under test was never "isolation holds." It was "isolation holds on
a connection we constructed correctly in the test." Those are different claims,
and only the second was ever verified.

## Decision

**Isolation is a property of wiring, not of schema.** A correct policy on a
connection that is not subject to it protects nothing.

This is now an enforced architectural rule, not guidance:

1. **Anything reachable from an HTTP handler must be built from the
   tenant-scoped pool.** Background workers are unconstrained — they process
   every tenant by design and are not reachable by a request.

2. **`internal/arch/wiring_test.go` enforces it statically.** It parses the
   composition root, resolves which pool each dependency was constructed from —
   including through wrapper constructors — and fails the build when a
   privileged dependency reaches the server.

3. **Exemptions are explicit, few, and justified by tenancy.** Four exist:
   tenant resolution (which necessarily precedes tenant binding), audit
   integrity verification (a platform-wide operator signal that would report
   healthy precisely because it could no longer see anything to check), and two
   diagnostics surfaces that describe the process rather than tenant rows. A
   second test caps the list, because a growing exemption list means the rule is
   being worked around rather than followed.

4. **Where a subsystem serves both directions, construct two instances** over
   the same tables — one per pool — rather than sharing one. Naming makes the
   privilege visible at the call site.

5. **ADR-AUDIT-005 constrains this.** The governance state change and its audit
   append must commit in one transaction, and a transaction cannot span two
   pools, so the engine's governance store and audit appender must share a pool.
   Both are therefore scoped together.

## Consequences

**The static check is the control, and it had the same blind spot as the bug.**
Its first version matched only the outermost call expression, so
`timeline.NewService(postgres.NewTimelineStore(pool), …)` passed cleanly — the
test was written to prevent the timeline disclosure and did not detect it. It
was corrected to walk the whole expression tree, and only then found the
governance mutation, which no one had looked for. A security control is not
verified by writing it; it is verified by reintroducing each historical bug and
confirming it fails. That procedure is now part of adding any such control.

**Green tests are evidence of what was tested, nothing more.** Every one of
these defects survived a suite at 75% coverage with 500+ tests. The useful
question is not "do the tests pass" but "what could still be catastrophically
wrong while they do."

**Security properties must be demonstrated against the running system.** All
three were found by issuing real requests as a second tenant. None was found by
reading code, and the code read correctly in all three cases.

**This rule generalises beyond tenancy.** Any capability carried by a
connection, a client or a credential — not just RLS — is subject to the same
failure: the capability is correct, and the wrong holder is handed it. Future
privilege distinctions (read replicas, restricted API clients, per-tenant
encryption keys) should be enforced by the same static check rather than by
convention.
