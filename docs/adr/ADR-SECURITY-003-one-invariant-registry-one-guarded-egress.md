# ADR-SECURITY-003 — One invariant registry; one guarded egress

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-SECURITY-002 (invariants verified continuously — **class reopened by this ADR**), ADR-SECURITY-001 (SSRF — **class reopened by this ADR**), ADR-TENANCY-004/006 (ownership authority, recovery as a verification boundary), ADR-TENANCY-007 (schema invariants) |

## Context

The programme's law is that a defect is closed only when the architectural class
is eliminated. Two classes recorded as eliminated each had a surviving instance.

### Class 1 — a boundary verified only at boot (ADR-SECURITY-002)

ADR-SECURITY-002 established the principle and stated it precisely:

> Fail-closed at boot and fail-open at runtime is not a policy, it is an accident
> of where the check happened to be called.

It then applied that principle to **one** of the platform's two startup
validators. `SchemaValidator` went under a sentinel; `OwnershipValidator` — which
proves the audit log and the governed object agree about who owns a chain, the
root authority ADR-TENANCY-003/004 rest on — was left at startup only.

Proven live. With a governed object's `tenant_id` moved to a second tenant so the
audit log and the object disagreed:

| | result |
|---|---|
| running instance, `/readyz` | **200** |
| running instance, `/v1/artifacts` | **200** |
| sentinel breaches logged | **0** |
| **restarting that same binary on that same database** | **refuses to start** — *"refusing to start: the ownership graph is inconsistent (1 problem(s))"* |

The platform treated an identical condition as fatal at 09:00 and invisible at
09:01 — the exact inconsistency ADR-SECURITY-002 named and claimed to have
removed.

The operational consequence is worse than the detection gap. An instance already
running serves indefinitely on a broken ownership graph, but **will not come back
if it restarts**. A routine deploy or a pod reschedule converts a silent problem
into an outage, one replica at a time, with no prior warning.

### Class 2 — an unguarded outbound client (ADR-SECURITY-001)

ADR-SECURITY-001 routed webhook delivery and policy actions through `safehttp`,
whose dialer refuses non-public addresses. Its wording — *"applied to both
outbound clients"* — was accurate when written and became a claim about a set
that had since grown. The JWKS fetch used a bare `http.Get(c.url)`: no guard, and
no timeout, so a hung JWKS endpoint stalls every RS256 verification behind it.

Exploitability here is lower than the original SSRF finding: the JWKS URL is
operator configuration, not tenant input. It is still the same shape — the
platform dialling a configured URL from inside the trust boundary with no guard —
and under the class-elimination law the class was open.

## Decision

Both fixes eliminate the class by construction rather than by patching the
instance.

### 1. One invariant registry, read by both enforcement points

`ops.Invariant` is a named check; `ops.CheckAll` evaluates a list **in order**,
stopping at the first that reports problems or cannot run, and prefixes each
problem with the invariant's name. `main.go` declares one
`platformInvariants` slice, and:

- the **startup gate** evaluates it and refuses to boot on any problem;
- the **sentinel** evaluates the same slice for the life of the process.

Registering an invariant therefore gives it both enforcement points at once.
There is no way to add one to a single point, which is what makes the omission
structurally impossible instead of merely corrected.

Order is load-bearing and short-circuiting is deliberate: the ownership check
queries columns the schema check proves exist, so running it against a schema
already known broken yields a database error instead of a finding and buries the
real problem under a spurious one.

### 2. One guarded egress, enforced by a whole-tree sweep

The JWKS cache takes an `*http.Client` and defaults to
`safehttp.Client(10s, false)`. `NewVerifierWithClient` lets a test inject its own
(the guarded default correctly refuses a loopback test server, which is how the
fix proved itself).

The architecture test no longer names call sites. It walks every non-test `.go`
file outside `internal/safehttp` and fails on `http.Get(`, `http.Post(`,
`http.PostForm(`, `http.Head(`, `http.DefaultClient` or a hand-rolled
`&http.Client{}`. Naming call sites is what let the class survive: the guard
checked the two clients someone remembered.

## Consequences

**What is now guaranteed.** Every platform invariant is enforced at boot *and*
continuously, from one definition. Every outbound HTTP request in the tree goes
through the SSRF-guarded client.

**What is *not* claimed.**

- **Detection, not prevention** (unchanged from ADR-SECURITY-002). An operator
  with database access can still break the ownership graph; the platform notices
  within one sentinel interval and stops serving. Reads inside that window are
  not prevented.
- **The invariant set is what it is.** This ADR guarantees that everything *in*
  the registry is enforced at both points, and that every validator the platform
  defines is in it. It does not claim the registry is a complete enumeration of
  everything worth checking.
- **The JWKS guard does not authenticate the endpoint.** It refuses non-public
  addresses; TLS verification and issuer trust are separate concerns, unchanged.
- **A breach now takes instances out of service on ownership problems too.** That
  is a wider availability trade than ADR-SECURITY-002 made, taken deliberately:
  the alternative is serving tenant data across a boundary the platform itself
  refuses to boot on.

## Evidence

Live exploit, before: ownership divergence introduced at runtime →
`readyz=200`, `/v1=200`, 0 breaches; the same database refused to boot.

Live re-attack, after:

| | before breach | breached | after repair |
|---|---|---|---|
| `/readyz` | 200 | **503** | 200 |
| `/v1/artifacts` | 200 | **503** | 200 |
| `/healthz` | 200 | 200 | 200 |
| `oneops_invariant_breached` | 0 | **1** | 0 |

with the breach naming the failing boundary:

```
SENTINEL BREACH … invariant="platform invariants" problems=1
detail="ownership graph consistency: audit ownership diverges from the governed
object on 1 event(s) (e.g. chain 01KYPKNVFGGNY6BX6Z6MXH0T7Z)"
```

JWKS: the guarded default refuses a loopback JWKS server
(`safehttp: refusing to connect to non-public address 127.0.0.1`).

Full suite green under `-race` against real PostgreSQL, all 19 packages.

## Enforcement

- `arch.TestPlatformInvariants_AreEnforcedAtBothPoints` — the registry exists,
  both the startup gate and the sentinel evaluate it, and nothing is validated
  outside it (`.Validate(rootCtx)` in `main.go` fails the build).
- `arch.TestEveryValidator_IsRegisteredAsAnInvariant` — every `New*Validator`
  constructor the platform defines appears in the registry; fails if none are
  found, so it cannot go vacuous.
- `arch.TestCheckAll_ShortCircuitsInOrder` — ordered evaluation, first-failure
  short circuit, and the invariant named in each problem.
- `arch.TestNoUnguardedOutboundHTTP` — whole-tree sweep for unguarded outbound
  HTTP.
- `auth.TestJWKSFetchIsSSRFGuarded` — the default JWKS client refuses a
  non-public address.

Mutation-verified: removing the ownership invariant from the registry fails with
*"NewOwnershipValidator is defined but never registered as a platform
invariant"*; restoring `http.Get` in the JWKS fetch fails the whole-tree sweep.

Two pre-existing ADR-SECURITY-002 guards asserted on the old identifier
`schemaSentinel` and correctly caught the rename to `invariantSentinel`; they
were updated, and the leadership-wiring allowlist (`perReplicaSupervisors`) with
them.
